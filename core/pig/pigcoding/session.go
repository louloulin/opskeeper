package pigcoding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
)

// Start describes the session one OpsKeeper turn runs in.
//
// Every field is PiG's. There is no OpsKeeper-shaped session request, and
// the absence is the design: a caller that can express a turn in
// SessionStartOptions can express it in PiG's own terms, so a PiG feature
// that is not in this struct is still reachable by whoever needs it —
// which is the property a wrapper erases.
type Start struct {
	// Model is the resolved model for this turn. Required.
	Model *ai.Model

	// SystemPrompt is the fully assembled base prompt, persona and skill
	// text included. OpsKeeper composes it because it is policy: what the
	// agent is allowed to say is a control-plane decision, not an agent
	// runtime's.
	SystemPrompt string

	// SystemPromptSections carries a structured prompt when OpsKeeper has
	// one. It wins over SystemPrompt when both are set, because PiG treats
	// it as the same prompt in a richer form rather than as an addition.
	SystemPromptSections ai.OrderedSections

	// Tools are the turn's tool bag, already filtered by role and profile.
	// OpsKeeper does not let the session widen it.
	Tools []agent.AgentTool

	// BeforeToolCall is where OpsKeeper's policy lives. A hook that returns
	// a block reason stops the call and the model is told why, so the
	// refusal is visible to the turn rather than only to the audit log.
	BeforeToolCall []agent.BeforeToolCallHook

	// ThinkingLevel overrides the model default for this turn.
	ThinkingLevel ai.ThinkingLevel

	// ScopedModels is the model cycle the console can step through.
	// Nil means the session may use any configured model.
	ScopedModels []coding.ScopedModel

	// SessionLog selects the transcript log. Nil uses the runtime's
	// in-memory log.
	SessionLog *coding.SessionManager

	// SkipBuiltinTools omits PiG's read/write/edit/bash tools. OpsKeeper
	// sets it: an operations agent's capabilities are its own tool
	// catalogue, and a general-purpose filesystem editor on a production
	// node is an attack surface nobody asked for.
	SkipBuiltinTools bool

	// NoSession disables persistence for this turn. OpsKeeper sets it
	// whenever its own session table is the transcript of record, which is
	// always.
	NoSession bool

	// CWDOverride is the working directory tools run in. OpsKeeper sets it
	// to a per-session scratch directory so a tool with a relative path
	// cannot walk into the manager's own source tree.
	CWDOverride string
}

// Session is one live turn's PiG session.
//
// The wrapper is four methods and a Close. It exists so the close ordering
// and the "subscribe before you send" rule are enforced in one place, not
// so the type can be renamed.
type Session struct {
	sess   *coding.Session
	closed bool
}

// Start opens a session.
//
// A nil Model is refused here rather than at the first prompt: a session
// with no model can only fail later, inside a turn that has already been
// persisted and billed to an operator's console.
func (r *Runtime) Start(start Start) (*Session, error) {
	if r == nil || r.closed || r.rt == nil {
		return nil, ErrClosed
	}
	if start.Model == nil {
		return nil, errors.New("pigcoding: Start requires a Model")
	}

	opts := coding.SessionStartOptions{
		Model:                start.Model,
		SystemPrompt:         start.SystemPrompt,
		SystemPromptSections: start.SystemPromptSections,
		// PiG calls this ExtraTools because it adds them on top of whatever
		// the extensions contributed. OpsKeeper skips extension tools
		// separately (below), so the two sets are disjoint and the name is
		// only a source-order detail.
		ExtraTools:       start.Tools,
		BeforeToolCall:   start.BeforeToolCall,
		ThinkingLevel:    start.ThinkingLevel,
		ScopedModels:     start.ScopedModels,
		SessionManager:   start.SessionLog,
		SkipBuiltinTools: start.SkipBuiltinTools,
		NoSession:        start.NoSession,
	}
	if start.CWDOverride != "" {
		cwd := start.CWDOverride
		opts.CWDOverride = &cwd
	}

	sess, err := r.rt.New(opts)
	if err != nil {
		return nil, fmt.Errorf("pigcoding: start session: %w", err)
	}
	return &Session{sess: sess}, nil
}

// Send runs one turn to completion and returns the messages it produced.
//
// It blocks, exactly as PiG documents. A caller that needs the answer on
// another goroutine owns the goroutine and the context; wrapping Send in an
// unowned goroutine is how a turn outlives the HTTP request that asked for
// it and keeps billing after the operator has navigated away.
func (s *Session) Send(ctx context.Context, prompt string) ([]agent.AgentMessage, error) {
	if s == nil || s.sess == nil {
		return nil, ErrClosed
	}
	return s.sess.Send(ctx, prompt)
}

// Events is the agent loop's stream.
//
// Subscribe before Send. A subscription opened afterwards misses every
// frame of the first turn, which is the turn with the tool calls in it —
// so the console shows a reply that appears from nowhere.
func (s *Session) Events() <-chan agent.AgentEvent {
	if s == nil || s.sess == nil {
		return nil
	}
	return s.sess.Events()
}

// Steer injects a message into a turn already in flight.
func (s *Session) Steer(ctx context.Context, text string) error {
	if s == nil || s.sess == nil {
		return ErrClosed
	}
	return s.sess.Steer(ctx, text, nil, nil)
}

// Abort cancels the turn in flight. It is safe to call when nothing is
// running, and safe to call twice.
func (s *Session) Abort(ctx context.Context) error {
	if s == nil || s.sess == nil {
		return ErrClosed
	}
	return s.sess.Abort(ctx)
}

// Running reports whether a turn is in flight.
func (s *Session) Running() bool {
	if s == nil || s.sess == nil {
		return false
	}
	return s.sess.IsStreaming()
}

// WaitIdle blocks until the session has no turn in flight.
//
// A caller that abandons a turn without this is racing: the next prompt is
// queued behind a run the caller believes has finished, and the tool calls
// of both turns interleave in one transcript.
func (s *Session) WaitIdle(ctx context.Context) error {
	if s == nil || s.sess == nil {
		return ErrClosed
	}
	return s.sess.WaitForIdle(ctx)
}

// Messages is a snapshot of the loop's in-memory history.
func (s *Session) Messages() []agent.AgentMessage {
	if s == nil || s.sess == nil {
		return nil
	}
	return s.sess.Messages()
}

// ID is the PiG session id. OpsKeeper records it beside its own session
// row so a transcript can be traced back to the agent that produced it.
func (s *Session) ID() string {
	if s == nil || s.sess == nil {
		return ""
	}
	return s.sess.ID()
}

// Close releases the session.
//
// Closing a session with a turn still in flight cancels it. That is the
// right default on a server: the alternative is holding a provider
// connection and an extension process alive for a turn whose operator is
// gone.
func (s *Session) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	// Bounded, so a provider that ignores cancellation cannot hold up
	// shutdown. PiG's Abort is already context-aware; this deadline is the
	// backstop for a tool that is not.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.sess.Abort(ctx)
	return s.sess.Close()
}
