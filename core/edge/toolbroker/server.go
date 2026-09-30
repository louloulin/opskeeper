// Package toolbroker is the host's end of the node tool-broker protocol.
//
// The gate decides whether a call may run; this runs it. The two are
// separate servers over separate sockets answering to separate host state,
// and the duplication is deliberate. The gate is reached through an
// extension inside the agent process, which means a package that replaced
// that extension would silence the check. The broker is not: it is host
// code, reached only by name, and it consults the same registry before it
// dispatches. A tool therefore has to survive being permitted by a check
// the agent could have suppressed.
//
// Running tools here rather than in the agent is the other half of the
// design. The agent process has the node's privileges; the host already
// holds tested implementations of every operation OpsKeeper needs on a
// host, with permission classes and spill handling already written. So the
// agent process holds no operational code at all — only routing — and
// "isolated subprocess" stops being a description and becomes a boundary.
//
// Like the gate, every failure path returns a refusal. A broker that errored
// has executed nothing, and a caller that cannot distinguish an error from a
// refusal must not be able to proceed on one.
package toolbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/wire"
)

// maxLine bounds one request line. 1 MiB is far above any legitimate tool
// call and far below anything worth defending against; an unbounded reader
// is an unbounded allocation waiting for a caller that sends garbage.
const maxLine = 1 << 20

// defaultCallTimeout bounds one tool call whose caller carried no deadline.
//
// This is a ceiling, not a latency budget: host_strace and host_sosreport
// legitimately take minutes, and the gate's much longer ceiling is for a
// different thing entirely — a human deciding. What this bounds is the
// case where a caller forgot to carry a context, so that a forgotten
// deadline closes the connection instead of pinning a goroutine for ever.
const defaultCallTimeout = 5 * time.Minute

// writeTimeout bounds one reply. It is short because the work is already
// done by the time a reply is written.
const writeTimeout = 30 * time.Second

// Call is one dispatched tool invocation, as the host sees it.
type Call struct {
	// SessionID is the conversation the call came from.
	SessionID string
	// ToolName is the tool to run.
	ToolName string
	// Arguments is the re-encoded argument object. Always valid JSON, and
	// always what the host parsed rather than what the agent claimed.
	Arguments json.RawMessage
	// Actor is who the host resolved the session to. The agent does not
	// get to supply it; an empty actor is a session the host does not
	// know, which resolves to read-only.
	Actor string
}

// Invoker runs a permitted call.
//
// It is an interface so the socket can be tested without a real host, and
// so the resolution strategy — local skill registry, reverse call to the
// control plane, both — lives in the edge rather than in the protocol
// layer.
type Invoker interface {
	Invoke(ctx context.Context, c Call) (json.RawMessage, error)
}

// Authorizer is the second check, run by host code the agent cannot reach.
//
// It returns whether the tool is permitted for this actor and, when it is
// not, a reason written for the model to read back into its transcript. It
// is satisfied by the same policygate registry the gate reads, narrowed to
// the caller's role.
type Authorizer func(actor, toolName string) (bool, string)

// ActorResolver answers "who is this conversation acting for".
//
// The agent does not get to answer this. An extension that could name its
// own privilege would name the top of the ladder, so the host resolves the
// actor from the session it minted. Returning an empty actor for a session
// the host does not know is the correct answer, and it is read-only at the
// gate and here.
type ActorResolver func(sessionID string) string

// Options configures a Server.
type Options struct {
	// Authorize is the second allow-list check. Required.
	Authorize Authorizer
	// Invoke runs a permitted call. Required.
	Invoke Invoker
	// Actor resolves the caller for a session. Optional; without it every
	// call is judged as having no role, which is read-only.
	Actor ActorResolver
	// Log receives warnings. Optional.
	Log Logger
	// CallTimeout bounds one tool call. Default defaultCallTimeout.
	CallTimeout time.Duration
}

// Logger is the slice of a logger the server uses.
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Server runs tool calls on behalf of the agent process.
type Server struct {
	authorize Authorizer
	invoke    Invoker
	actor     ActorResolver
	log       Logger
	within    time.Duration

	mu       sync.Mutex
	listener net.Listener
	path     string
}

// Listen creates the socket and starts serving.
//
// The socket is created with owner-only permissions inside a directory it
// creates with owner-only permissions, before binding. A tool socket another
// local account can reach is a socket any local process can use to run this
// node's tools — which is a wider door than the gate, not a narrower one,
// because here the answer is "done" rather than "permitted".
func Listen(opts Options) (*Server, error) {
	if opts.Authorize == nil {
		return nil, errors.New("toolbroker: Authorize is required")
	}
	if opts.Invoke == nil {
		return nil, errors.New("toolbroker: Invoke is required")
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	// The directory is unique per broker rather than per process. A
	// per-process path is a shared resource between two brokers in the
	// same process, and the second one to start would remove the first
	// one's socket and answer on it — a broker silently becoming the
	// wrong broker. MkdirTemp also makes the stale-socket case impossible
	// instead of handled: there is never a pre-existing file to clear,
	// and Close removes the directory it made.
	dir, err := os.MkdirTemp("", "opskeeper-tool-"+fmt.Sprint(os.Getpid())+"-")
	if err != nil {
		return nil, fmt.Errorf("toolbroker: create socket dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("toolbroker: chmod socket dir: %w", err)
	}
	path := filepath.Join(dir, "tool.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("toolbroker: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("toolbroker: chmod socket: %w", err)
	}
	s := &Server{
		authorize: opts.Authorize,
		invoke:    opts.Invoke,
		actor:     opts.Actor,
		log:       opts.Log,
		within:    opts.CallTimeout,
		path:      path,
		listener:  ln,
	}
	go s.serve()
	return s, nil
}

// Path is the socket path, for handing to the agent's environment.
func (s *Server) Path() string { return s.path }

// Close stops serving and removes the socket and its directory.
//
// Idempotent, because shutdown paths race: the supervisor is stopping, the
// gate socket is closing, and an agent that has already exited may still
// hold a connection. Closing twice is a fact about shutdown, not an error.
func (s *Server) Close() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.mu.Unlock()

	var err error
	if ln != nil {
		if cerr := ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			err = cerr
		}
	}
	if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	// The directory was created by this broker alone, so removing it
	// cannot take a sibling broker's socket with it.
	_ = os.Remove(filepath.Dir(s.path))
	return err
}

// serve accepts until the listener closes.
func (s *Server) serve() {
	for {
		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			return
		}
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.listener == nil
			s.mu.Unlock()
			if closed {
				return
			}
			if s.log != nil {
				s.log.Warn("tool socket accept failed", "err", err.Error())
			}
			return
		}
		go s.handle(conn)
	}
}

// handle serves one caller.
//
// One connection carries many calls, because the agent holds one per turn
// and re-dialling per tool would put a connect handshake between the model
// and every observation it makes. The read deadline is set once for the
// greeting and cleared after each reply, so a caller that goes quiet between
// turns is not a fault but a caller that connects and never speaks is.
func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(defaultCallTimeout))
	reader := bufio.NewReaderSize(conn, 64*1024)
	writer := bufio.NewWriter(conn)

	for {
		line, err := readLine(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && s.log != nil {
				s.log.Warn("tool socket read failed", "err", err.Error())
			}
			return
		}
		if len(line) == 0 {
			continue
		}
		reply := s.dispatch(line)
		body, err := json.Marshal(reply)
		if err != nil {
			// Marshalling a two-field struct cannot fail in practice, but
			// "cannot fail in practice" is how an unrecoverable write gets
			// shipped. Report the failure rather than closing with nothing
			// said.
			body = []byte(`{"error":"the host could not encode this tool's result"}`)
		}
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := writer.Write(append(body, '\n')); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
	}
}

// dispatch turns one request line into one reply.
//
// Every path out of here either invokes something the authoriser permitted,
// or returns an error. There is no path that reports success without the
// invoker having run.
func (s *Server) dispatch(line []byte) wire.ToolReply {
	var req wire.ToolRequest
	if err := json.Unmarshal(line, &req); err != nil {
		if s.log != nil {
			s.log.Warn("tool socket got an unreadable request", "err", err.Error())
		}
		return failed("the host could not read that tool call")
	}
	if req.ToolName == "" {
		return failed("a tool call with no name cannot be run")
	}

	// The arguments are re-encoded rather than relayed. What runs must be
	// what the host parsed: forwarding bytes the host never validated
	// would put an unchecked payload in front of every executor.
	args, err := json.Marshal(req.Arguments)
	if err != nil {
		return failed("that tool call's arguments could not be read")
	}

	actor := ""
	if s.actor != nil {
		actor = s.actor(req.SessionID)
	}

	permitted, reason := s.authorize(actor, req.ToolName)
	if !permitted {
		return failed(reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.within)
	defer cancel()

	out, err := s.invoke.Invoke(ctx, Call{
		SessionID: req.SessionID,
		ToolName:  req.ToolName,
		Arguments: args,
		Actor:     actor,
	})
	if err != nil {
		// A tool that failed is reported to the model as a failure it can
		// read and reason about, not as a transport fault it would retry.
		if s.log != nil {
			s.log.Warn("tool call failed", "tool", req.ToolName, "err", err.Error())
		}
		return failed(req.ToolName + " failed: " + err.Error())
	}
	return wire.ToolReply{Result: out}
}

func failed(reason string) wire.ToolReply { return wire.ToolReply{Error: reason} }

// readLine reads one newline-terminated line, bounded by maxLine.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(buf)+len(chunk) > maxLine {
			return nil, fmt.Errorf("toolbroker: request exceeds %d bytes", maxLine)
		}
		buf = append(buf, chunk...)
		if !isPrefix {
			return buf, nil
		}
	}
}
