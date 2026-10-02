package nodefleet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/ports"

	"github.com/vincent-wuhan/opskeeper/internal/pkg/tunnel"
)

// ErrNoSession means no conversation with that id is open on this node.
//
// It is distinct from "the node refused" because nothing was asked of the
// node: the fleet simply has no route. A caller rendering an error should
// say the conversation is gone, not that the node is broken.
var ErrNoSession = errors.New("nodefleet: no open session")

// PromptRequest is one operator turn, addressed to a node.
type PromptRequest struct {
	// EdgeID is the node whose agent should answer.
	EdgeID uint64
	// SessionID scopes the conversation. The fleet mints it when a console
	// opens a conversation and reuses it for every turn after.
	SessionID string
	// UserText is the turn, after any mention rendering.
	UserText string
	// Role is the caller's system role. It is recorded on the request and
	// is the node's to enforce: the edge filters the agent's tool set by
	// it before the turn starts, so a viewer's turn cannot reach a
	// mutating tool no matter what the console asked for.
	Role string
	// Locale is the console language the reply must use.
	Locale string
	// Selection optionally pins the model for this conversation.
	Selection domain.ModelSelection
}

// Fleet holds one agent handle per open conversation and routes to it.
//
// It is the control plane's view of the node agent estate. It starts and
// stops nothing: a node's agent belongs to the node's supervisor, and the
// fleet's job is to say which node should hear about a turn and to get the
// reply back to the console that asked.
//
// A Fleet is safe for concurrent use. A console's request handler, its SSE
// stream, and the tunnel's inbound event path all touch it at once.
type Fleet struct {
	dial Dialer

	mu sync.RWMutex
	// sessions maps edge id to that node's open conversations. A node has
	// at most one agent process, and the process multiplexes conversations
	// by session id, so the edge is the right outer key.
	sessions map[uint64]map[string]*session
}

// session is one open conversation on one node.
type session struct {
	edgeID    uint64
	sessionID string
	handle    *TunelledProcess
	// sink receives the console frames for this conversation. It is the
	// console's SSE stream; a console that has gone away makes Emit fail,
	// and the session is closed rather than left buffering.
	sink ports.EventSink
	// keepAlive detaches this session's subscription from the handle when
	// the conversation closes. It is set after the session is stored, and
	// read only after that, so it needs no lock of its own.
	keepAlive func()
	// terminal latches when the agent reported the turn finished, so a
	// late frame cannot reopen a conversation the console has closed.
	terminal atomic.Bool
	// frames counts what reached the console.
	frames atomic.Int64
	// dropped counts frames the node could not translate, and which this
	// fleet therefore did not emit.
	dropped atomic.Int64
}

// Options configures a Fleet.
type Options struct {
	// Dial opens command channels to nodes. Required.
	Dial Dialer
}

// New returns an empty Fleet.
func New(opts Options) (*Fleet, error) {
	if opts.Dial == nil {
		return nil, errors.New("nodefleet: Dial is required")
	}
	return &Fleet{dial: opts.Dial, sessions: make(map[uint64]map[string]*session)}, nil
}

// Open registers a conversation and returns the handle to drive it.
//
// It is separate from Prompt because a console opens a conversation, then
// streams many turns through it, then closes it. Folding the three into one
// call would either leak the subscription or re-open it per turn and lose
// the frames that arrived in between.
func (f *Fleet) Open(req PromptRequest, sink ports.EventSink) (*TunelledProcess, error) {
	if req.EdgeID == 0 {
		return nil, errors.New("nodefleet: edge id is required")
	}
	if req.SessionID == "" {
		return nil, errors.New("nodefleet: session id is required: a conversation with no id cannot be demultiplexed")
	}

	handle := NewTunelledProcess(req.EdgeID, req.SessionID, f.dial)
	s := &session{edgeID: req.EdgeID, sessionID: req.SessionID, handle: handle, sink: sink}

	f.mu.Lock()
	if f.sessions[req.EdgeID] == nil {
		f.sessions[req.EdgeID] = make(map[string]*session)
	}
	if existing, clash := f.sessions[req.EdgeID][req.SessionID]; clash {
		f.mu.Unlock()
		// Two consoles claiming one conversation id would interleave two
		// assistants into a single agent session. Refusing is the only
		// safe answer; merging would produce a transcript neither
		// operator can read.
		_ = existing
		return nil, fmt.Errorf("nodefleet: session %q is already open on edge %d", req.SessionID, req.EdgeID)
	}
	f.sessions[req.EdgeID][req.SessionID] = s
	f.mu.Unlock()

	unsubscribe := handle.OnFrame(f.makeRelay(s))
	s.keepAlive = unsubscribe

	// An optional model pin is applied before the first turn, so the
	// conversation does not open on one model and answer on another.
	if req.Selection.Provider != "" || req.Selection.Model != "" {
		ctx, cancel := context.WithTimeout(context.Background(), pinTimeout)
		defer cancel()
		if err := handle.SetModel(ctx, string(req.Selection.Provider), req.Selection.Model); err != nil {
			f.Close(req.EdgeID, req.SessionID)
			// A refused pin is reported rather than swallowed: an operator
			// who chose a model deserves to know it was not used, and
			// silently answering on a different one would make the cost
			// line wrong with no explanation.
			return nil, fmt.Errorf("nodefleet: pin model for session %q: %w", req.SessionID, err)
		}
	}
	return handle, nil
}

// makeRelay turns a pushed frame into a console frame on the session's
// sink.
//
// Two rules, both about not inventing anything. A frame the node already
// translated is emitted as it stands. A frame it could not place is
// counted, not guessed at: a wrong frame is worse than a missing one,
// because the console would render it as fact.
func (f *Fleet) makeRelay(s *session) func(tunnel.AgentEventFrame) {
	return func(frame tunnel.AgentEventFrame) {
		if s.terminal.Load() {
			// The conversation is closed. A late frame belongs to a turn
			// nobody is watching, and delivering it would reopen a bubble
			// the operator already dismissed.
			return
		}
		if frame.Frame == nil {
			s.dropped.Add(1)
			return
		}
		if err := s.sink.Emit(context.Background(), *frame.Frame); err != nil {
			// The console has gone away. Closing the session is the honest
			// response: continuing to relay into a stream nobody reads
			// would buffer frames until the node was restarted.
			f.Close(s.edgeID, s.sessionID)
			return
		}
		s.frames.Add(1)
		if frame.Terminal {
			s.terminal.Store(true)
		}
	}
}

// Prompt sends a turn and returns as soon as the node accepts it.
//
// It does not wait for the reply. The reply arrives on the session's sink
// as frames the node already translated, and holding this call open for the
// length of an investigation would pin an HTTP handler for minutes and lose
// the whole turn if the console disconnected first.
func (f *Fleet) Prompt(ctx context.Context, req PromptRequest) error {
	s, err := f.lookup(req.EdgeID, req.SessionID)
	if err != nil {
		return err
	}
	// A new turn reopens a conversation that had finished. A console
	// asking a follow-up in the same session must not be silently ignored
	// because the previous turn set the terminal flag.
	s.terminal.Store(false)
	return s.handle.Prompt(ctx, req.UserText)
}

// Steer injects a message into the turn already running on the node.
func (f *Fleet) Steer(ctx context.Context, edgeID uint64, sessionID, text string) error {
	s, err := f.lookup(edgeID, sessionID)
	if err != nil {
		return err
	}
	return s.handle.Steer(ctx, text)
}

// Abort stops the current turn on a node.
//
// It does not close the session: the operator's intent is "stop this turn",
// not "end the conversation", and a console that keeps the bubble open must
// be able to send another turn.
func (f *Fleet) Abort(ctx context.Context, edgeID uint64, sessionID string) error {
	s, err := f.lookup(edgeID, sessionID)
	if err != nil {
		return err
	}
	return s.handle.Abort(ctx)
}

// State reports what a node's agent is doing.
func (f *Fleet) State(ctx context.Context, edgeID uint64) (*ports.ProcessState, error) {
	// A state poll is about the node, not about a conversation, so it goes
	// through a throwaway handle rather than requiring one to be open.
	return NewTunelledProcess(edgeID, "", f.dial).State(ctx)
}

// Health asks a node's supervisor how its process is doing.
func (f *Fleet) Health(ctx context.Context, edgeID uint64) (*tunnel.AgentHealthResponse, error) {
	return NewTunelledProcess(edgeID, "", f.dial).Health(ctx)
}

// Close ends a conversation and releases its subscription.
func (f *Fleet) Close(edgeID uint64, sessionID string) {
	f.mu.Lock()
	byID := f.sessions[edgeID]
	s, ok := byID[sessionID]
	if ok {
		delete(byID, sessionID)
		if len(byID) == 0 {
			delete(f.sessions, edgeID)
		}
	}
	f.mu.Unlock()
	if !ok {
		return
	}
	if s.keepAlive != nil {
		s.keepAlive()
	}
}

// CloseAll ends every conversation, for a manager shutdown.
func (f *Fleet) CloseAll() {
	f.mu.Lock()
	open := make([]*session, 0, len(f.sessions))
	for _, byID := range f.sessions {
		for _, s := range byID {
			open = append(open, s)
		}
	}
	f.sessions = make(map[uint64]map[string]*session)
	f.mu.Unlock()
	for _, s := range open {
		if s.keepAlive != nil {
			s.keepAlive()
		}
	}
}

// SessionCount reports how many conversations are open, for a fleet view.
func (f *Fleet) SessionCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n := 0
	for _, byID := range f.sessions {
		n += len(byID)
	}
	return n
}

// SessionStats describes one open conversation.
type SessionStats struct {
	EdgeID    uint64
	SessionID string
	// Frames counts what has reached the console.
	Frames int64
	// Dropped counts frames the node could not translate and this fleet
	// therefore did not emit. A non-zero value means the console is
	// showing a conversation with holes, and somebody debugging that
	// needs to know the gaps are translation, not silence from the agent.
	Dropped int64
	// Terminal is true once the agent reported the turn finished.
	Terminal bool
}

// Stats reports on one conversation.
func (f *Fleet) Stats(edgeID uint64, sessionID string) (SessionStats, bool) {
	s, err := f.lookup(edgeID, sessionID)
	if err != nil {
		return SessionStats{}, false
	}
	return SessionStats{
		EdgeID:    s.edgeID,
		SessionID: s.sessionID,
		Frames:    s.frames.Load(),
		Dropped:   s.dropped.Load(),
		Terminal:  s.terminal.Load(),
	}, true
}

// Stats reports on every open conversation.
func (f *Fleet) AllStats() []SessionStats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]SessionStats, 0, len(f.sessions))
	for _, byID := range f.sessions {
		for _, s := range byID {
			out = append(out, SessionStats{
				EdgeID:    s.edgeID,
				SessionID: s.sessionID,
				Frames:    s.frames.Load(),
				Dropped:   s.dropped.Load(),
				Terminal:  s.terminal.Load(),
			})
		}
	}
	return out
}

// lookup finds a session, without holding the lock while the caller works.
func (f *Fleet) lookup(edgeID uint64, sessionID string) (*session, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	byID, ok := f.sessions[edgeID]
	if !ok {
		return nil, fmt.Errorf("%w: %q on edge %d", ErrNoSession, sessionID, edgeID)
	}
	s, ok := byID[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: %q on edge %d", ErrNoSession, sessionID, edgeID)
	}
	return s, nil
}

// pinTimeout bounds the model pin applied at conversation open. It is short
// because the conversation is not usable until it resolves, and a node that
// is not answering should fail the open rather than hang it.
const pinTimeout = 10 * time.Second

// DeliverInbound routes one frame a node pushed to the conversation it
// belongs to.
//
// It is the counterpart to the tunnel registration: the manager's inbound
// handler is not the fleet and cannot reach into a session, so it hands
// every push here and the fleet resolves (edge, session) to a handle. The
// node stamps both on the frame because one agent process multiplexes
// several conversations and a frame that arrived without them would have
// nowhere honest to go.
//
// The bool reports whether a conversation took the frame. A false is not an
// error — it is the ordinary result of a frame for a conversation that has
// since closed, and of a node whose agent was restarted mid-turn. The
// caller answers the push with success either way: an edge must not be made
// to retry a frame the manager has no way to place.
func (f *Fleet) DeliverInbound(frame tunnel.AgentEventFrame) bool {
	if frame.SessionID == "" {
		// A frame with no conversation id cannot be attributed to one.
		// Emitting it to whatever console happens to be reading would put
		// one operator's turn in another's transcript.
		return false
	}
	s, err := f.lookup(frame.EdgeID, frame.SessionID)
	if err != nil {
		return false
	}
	s.handle.Deliver(frame)
	return true
}

// Decide applies a human's answer to a gated call on a node.
//
// The conversation is resolved first, because a pending request belongs to a
// live turn: routing by conversation rather than by a guessed edge means a
// decision cannot be delivered to a node that has no such request, and a
// console showing a request for a closed conversation is told the request
// is gone rather than watching a button spin.
func (f *Fleet) Decide(ctx context.Context, edgeID uint64, sessionID string, d Decision) error {
	if _, err := f.lookup(edgeID, sessionID); err != nil {
		return err
	}
	return NewTunelledProcess(edgeID, sessionID, f.dial).Decide(ctx, d.Wire())
}

// Decision is an operator's answer to one approval request.
//
// It is the control plane's copy of the answer, not the gate's: the gate
// recomputes the digest and refuses anything that does not match the call
// it holds, so nothing here is trusted. Keeping the two apart means a bug
// in this layer can fail to deliver a decision but cannot forge one.
type Decision struct {
	// RequestID is the handle the node minted for the pending call.
	RequestID string
	// Digest is echoed from the frame the console rendered. The node
	// refuses a decision whose digest does not match the request it names.
	Digest string
	// Grant is true to allow the call and false to refuse it. A bool rather
	// than a string, because the only two answers the gate accepts are
	// these two, and a third one should not be expressible here.
	Grant bool
	// DecidedBy is the operator's identity, for the node's audit ledger.
	DecidedBy string
	// Note is free text recorded with the decision.
	Note string
}

// Wire renders the decision as the tunnel body.
func (d Decision) Wire() tunnel.AgentDecideRequest {
	word := ports.ApprovalDenied
	if d.Grant {
		word = ports.ApprovalGranted
	}
	return tunnel.AgentDecideRequest{
		RequestID: d.RequestID,
		Digest:    d.Digest,
		Decision:  string(word),
		DecidedBy: d.DecidedBy,
		Note:      d.Note,
	}
}
