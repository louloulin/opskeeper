package pirpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
)

// Errors returned by Session.
var (
	// ErrSessionClosed means the transport is gone: Close was called
	// or Pi's stdout hit EOF / a read error. Pending and subsequent
	// commands fail with it rather than hanging.
	ErrSessionClosed = errors.New("pirpc: session closed")

	// ErrCommandFailed wraps a well-formed response with
	// success:false. Callers that only care about "did Pi accept
	// this" can errors.Is against it; callers that need the message
	// read Response.Error.
	ErrCommandFailed = errors.New("pirpc: command failed")
)

// Response is a Pi RPC command response. Data is left raw so each
// command's typed accessor decodes only the shape it needs; Pi grows
// fields over versions and an exhaustive struct would rot.
type Response struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Event is one asynchronous record from Pi's event stream
// (agent_start, tool_execution_end, bash_execution_update, ...).
// Raw is the whole record so callers can decode the variants they
// act on and forward the rest to the audit chain verbatim.
type Event struct {
	Type string
	Raw  json.RawMessage
}

// UIPolicy decides how the transport answers extension UI dialogs.
// Dialog methods block Pi until answered, so an unattended sidecar
// must answer all of them.
type UIPolicy int

const (
	// UIDenyAll cancels every dialog and answers confirm with
	// confirmed:false. This is the default and the only value an
	// unattended edge should use: an extension prompt is not an
	// approval channel, and auto-approving one would route around
	// the cloud reviewer double-sign required by plan1.0.md §6.5.
	UIDenyAll UIPolicy = iota

	// UIIgnore leaves dialogs unanswered. Pi blocks until its own
	// dialog timeout (when the request carries one) or forever.
	// Only useful when the caller answers dialogs itself via Send.
	UIIgnore
)

// SessionOptions configures NewSession.
type SessionOptions struct {
	// Stdin is Pi's standard input (commands are written here).
	// Required.
	Stdin io.Writer

	// Stdout is Pi's standard output (responses and events).
	// Required.
	Stdout io.Reader

	// MaxLineBytes bounds one record. Zero → DefaultMaxLineBytes.
	MaxLineBytes int

	// OnEvent receives every non-response record. It is called from
	// the reader goroutine, so it must not block: hand off to a
	// channel or a bounded worker. Nil → events are dropped.
	OnEvent func(Event)

	// UI selects how extension dialogs are answered. Zero value is
	// UIDenyAll (fail closed).
	UI UIPolicy

	// Logger is the structured log sink. Nil → slog.Default().
	Logger *slog.Logger

	// IDPrefix disambiguates ids when several sessions share a log.
	// Zero → "c".
	IDPrefix string
}

// Session is one Pi RPC conversation over an already-established
// pair of pipes. It does not spawn or reap processes — see Spawn.
type Session struct {
	w       io.Writer
	onEvent func(Event)
	ui      UIPolicy
	log     *slog.Logger
	prefix  string
	maxLine int

	nextID atomic.Uint64

	mu      sync.Mutex
	waiters map[string]chan *Response
	closed  bool

	doneCh  chan struct{}
	readErr error
}

// NewSession starts the reader goroutine and returns a ready
// session. The caller owns the pipes: closing them (or killing Pi)
// terminates the session.
func NewSession(opts SessionOptions) (*Session, error) {
	if opts.Stdin == nil {
		return nil, errors.New("pirpc: SessionOptions.Stdin required")
	}
	if opts.Stdout == nil {
		return nil, errors.New("pirpc: SessionOptions.Stdout required")
	}
	s := &Session{
		w:       opts.Stdin,
		onEvent: opts.OnEvent,
		ui:      opts.UI,
		log:     opts.Logger,
		prefix:  opts.IDPrefix,
		maxLine: opts.MaxLineBytes,
		waiters: make(map[string]chan *Response),
		doneCh:  make(chan struct{}),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.prefix == "" {
		s.prefix = "c"
	}
	go s.readLoop(opts.Stdout)
	return s, nil
}

// Done is closed when the reader goroutine exits, i.e. when Pi's
// stdout ends. Err reports why.
func (s *Session) Done() <-chan struct{} { return s.doneCh }

// Err returns the terminal read error, or nil while the session is
// live. io.EOF is reported as ErrSessionClosed so callers do not
// have to special-case a clean child exit.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readErr
}

// Close stops accepting commands and fails every pending one. It
// does not kill Pi; closing Pi's stdin (which Spawn does) is what
// makes Pi exit.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	waiters := s.waiters
	s.waiters = make(map[string]chan *Response)
	s.mu.Unlock()

	for id, ch := range waiters {
		close(ch)
		s.log.Debug("pirpc: dropping pending command on close", slog.String("id", id))
	}
	return nil
}

// Do sends a command and waits for the response carrying the same
// id. cmd must marshal to a JSON object; the "id" key is injected
// (any caller-supplied id is replaced so correlation cannot break).
//
// A response with success:false is returned together with an error
// wrapping ErrCommandFailed — callers that treat rejection as data
// inspect the Response, others just check err.
func (s *Session) Do(ctx context.Context, cmd any) (*Response, error) {
	id := s.prefix + strconv.FormatUint(s.nextID.Add(1), 10)
	line, err := encodeCommand(id, cmd)
	if err != nil {
		return nil, err
	}

	ch := make(chan *Response, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrSessionClosed
	}
	s.waiters[id] = ch
	s.mu.Unlock()

	if err := s.writeLine(line); err != nil {
		s.dropWaiter(id)
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			if rerr := s.Err(); rerr != nil {
				return nil, rerr
			}
			return nil, ErrSessionClosed
		}
		if !resp.Success {
			return resp, fmt.Errorf("%w: %s: %s", ErrCommandFailed, resp.Command, resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		s.dropWaiter(id)
		return nil, ctx.Err()
	case <-s.doneCh:
		if rerr := s.Err(); rerr != nil {
			return nil, rerr
		}
		return nil, ErrSessionClosed
	}
}

// Send writes a record without waiting for a response. Used for
// fire-and-forget records such as extension_ui_response.
func (s *Session) Send(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pirpc: marshal record: %w", err)
	}
	return s.writeLine(line)
}

func (s *Session) writeLine(line []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}
	// One Write per record keeps records atomic against a pipe with
	// concurrent writers.
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if _, err := s.w.Write(buf); err != nil {
		return fmt.Errorf("pirpc: write command: %w", err)
	}
	return nil
}

func (s *Session) dropWaiter(id string) {
	s.mu.Lock()
	delete(s.waiters, id)
	s.mu.Unlock()
}

// encodeCommand marshals cmd and forces the id field. Marshalling to
// a map keeps the caller free to pass a struct or a map without this
// package owning a type per command.
func encodeCommand(id string, cmd any) ([]byte, error) {
	raw, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("pirpc: marshal command: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("pirpc: command must be a JSON object: %w", err)
	}
	if _, ok := obj["type"]; !ok {
		return nil, errors.New("pirpc: command missing \"type\"")
	}
	obj["id"] = id
	line, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("pirpc: marshal command: %w", err)
	}
	return line, nil
}
