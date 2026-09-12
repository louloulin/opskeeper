package pirpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePi is an in-memory stand-in for a Pi child in RPC mode. It
// records the commands the session wrote and lets a test push
// arbitrary records back, including out of order.
type fakePi struct {
	t *testing.T

	// toSession is what the session reads as Pi's stdout.
	toSession *io.PipeWriter

	mu       sync.Mutex
	commands []map[string]any
	stopped  bool
	stopCh   chan struct{}
}

func newFakePi(t *testing.T, opts SessionOptions) (*Session, *fakePi) {
	t.Helper()
	outR, outW := io.Pipe()
	f := &fakePi{t: t, toSession: outW, stopCh: make(chan struct{})}

	opts.Stdin = writerFunc(f.onCommand)
	opts.Stdout = outR
	s, err := NewSession(opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() {
		f.close()
		_ = s.Close()
	})
	return s, f
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func (f *fakePi) onCommand(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		var cmd map[string]any
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			f.t.Errorf("fakePi: session wrote non-JSON %q: %v", line, err)
			continue
		}
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
	}
	return len(p), nil
}

// write pushes a raw record to the session.
func (f *fakePi) write(record string) {
	f.mu.Lock()
	stopped := f.stopped
	f.mu.Unlock()
	if stopped {
		return
	}
	if _, err := f.toSession.Write([]byte(record + "\n")); err != nil {
		f.t.Errorf("fakePi write: %v", err)
	}
}

// close makes the session observe EOF on Pi's stdout, i.e. the child
// exited.
func (f *fakePi) close() {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return
	}
	f.stopped = true
	f.mu.Unlock()
	_ = f.toSession.Close()
}

func (f *fakePi) seen() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.commands))
	copy(out, f.commands)
	return out
}

// waitForCommand blocks until at least n commands have been written.
func (f *fakePi) waitForCommand(n int) []map[string]any {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.seen(); len(got) >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %d commands; saw %d", n, len(f.seen()))
	return nil
}

func TestDoCorrelatesByIDNotArrivalOrder(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})

	type result struct {
		state *State
		err   error
	}
	first := make(chan result, 1)
	second := make(chan result, 1)

	go func() {
		st, err := s.GetState(context.Background())
		first <- result{st, err}
	}()
	f.waitForCommand(1)
	go func() {
		st, err := s.GetState(context.Background())
		second <- result{st, err}
	}()
	cmds := f.waitForCommand(2)

	id1, _ := cmds[0]["id"].(string)
	id2, _ := cmds[1]["id"].(string)
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("ids not unique: %q %q", id1, id2)
	}

	// Answer the SECOND command first: the transport must not
	// assume FIFO.
	f.write(`{"id":"` + id2 + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"second"}}`)
	f.write(`{"id":"` + id1 + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"first"}}`)

	r1 := <-first
	r2 := <-second
	if r1.err != nil || r2.err != nil {
		t.Fatalf("errs: %v / %v", r1.err, r2.err)
	}
	if r1.state.SessionID != "first" {
		t.Fatalf("first got %q", r1.state.SessionID)
	}
	if r2.state.SessionID != "second" {
		t.Fatalf("second got %q", r2.state.SessionID)
	}
}

func TestDoReportsCommandFailure(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})

	errCh := make(chan error, 1)
	go func() {
		_, err := s.Do(context.Background(), map[string]any{"type": "set_model", "model": "nope"})
		errCh <- err
	}()
	cmds := f.waitForCommand(1)
	id, _ := cmds[0]["id"].(string)
	f.write(`{"id":"` + id + `","type":"response","command":"set_model","success":false,"error":"Model not found: nope"}`)

	err := <-errCh
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("want ErrCommandFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "Model not found") {
		t.Fatalf("error text lost: %v", err)
	}
}

func TestDoRejectsCommandWithoutType(t *testing.T) {
	s, _ := newFakePi(t, SessionOptions{})
	if _, err := s.Do(context.Background(), map[string]any{"message": "hi"}); err == nil {
		t.Fatal("want error for command without type")
	}
}

func TestDoOverwritesCallerSuppliedID(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})
	go func() { _, _ = s.Do(context.Background(), map[string]any{"type": "abort", "id": "attacker"}) }()
	cmds := f.waitForCommand(1)
	if got := cmds[0]["id"]; got == "attacker" {
		t.Fatalf("caller id was not replaced: %v", got)
	}
}

func TestEventsGoToHandler(t *testing.T) {
	var mu sync.Mutex
	var got []string
	s, f := newFakePi(t, SessionOptions{OnEvent: func(e Event) {
		mu.Lock()
		got = append(got, e.Type)
		mu.Unlock()
	}})

	f.write(`{"type":"agent_start"}`)
	f.write(`{"type":"tool_execution_end","toolName":"bash","isError":false}`)
	// A response with no id cannot be correlated; it must surface as
	// an event rather than vanish.
	f.write(`{"type":"response","command":"parse","success":false,"error":"bad json"}`)

	errCh := make(chan error, 1)
	go func() {
		_, err := s.GetState(context.Background())
		errCh <- err
	}()
	cmds := f.waitForCommand(1)
	id, _ := cmds[0]["id"].(string)
	f.write(`{"id":"` + id + `","type":"response","command":"get_state","success":true,"data":{}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("GetState: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"agent_start", "tool_execution_end", "response"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestNonJSONStdoutDoesNotKillSession(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})
	f.write(`(node:1234) Warning: something on stdout`)

	errCh := make(chan error, 1)
	go func() {
		_, err := s.GetState(context.Background())
		errCh <- err
	}()
	cmds := f.waitForCommand(1)
	id, _ := cmds[0]["id"].(string)
	f.write(`{"id":"` + id + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"alive"}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("session died on foreign stdout: %v", err)
	}
}

func TestDialogsAreAnsweredFailClosed(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})

	f.write(`{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Allow dangerous command?"}`)
	f.write(`{"type":"extension_ui_request","id":"ui-2","method":"select","title":"Pick","options":["Allow","Block"]}`)
	f.write(`{"type":"extension_ui_request","id":"ui-3","method":"notify","message":"fyi"}`)

	cmds := f.waitForCommand(2)
	if len(cmds) != 2 {
		t.Fatalf("fire-and-forget method was answered: %v", cmds)
	}
	if cmds[0]["type"] != "extension_ui_response" || cmds[0]["id"] != "ui-1" {
		t.Fatalf("unexpected first reply: %v", cmds[0])
	}
	if confirmed, ok := cmds[0]["confirmed"].(bool); !ok || confirmed {
		t.Fatalf("confirm must be denied, got %v", cmds[0])
	}
	if cancelled, ok := cmds[1]["cancelled"].(bool); !ok || !cancelled {
		t.Fatalf("select must be cancelled, got %v", cmds[1])
	}
	_ = s
}

func TestUIIgnoreLeavesDialogsUnanswered(t *testing.T) {
	_, f := newFakePi(t, SessionOptions{UI: UIIgnore})
	f.write(`{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"?"}`)
	time.Sleep(30 * time.Millisecond)
	if got := f.seen(); len(got) != 0 {
		t.Fatalf("UIIgnore answered a dialog: %v", got)
	}
}

func TestPendingCommandFailsWhenChildExits(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})

	errCh := make(chan error, 1)
	go func() {
		_, err := s.GetState(context.Background())
		errCh <- err
	}()
	f.waitForCommand(1)
	f.close() // child died mid-command

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("want ErrSessionClosed, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending command hung after child exit")
	}

	if _, err := s.GetState(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("post-exit command: want ErrSessionClosed, got %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after child exit")
	}
}

func TestContextCancelReleasesWaiter(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	if _, err := s.GetState(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	cmds := f.waitForCommand(1)
	id, _ := cmds[0]["id"].(string)

	// A late response for the abandoned id must not panic or block.
	f.write(`{"id":"` + id + `","type":"response","command":"get_state","success":true,"data":{}}`)

	errCh := make(chan error, 1)
	go func() {
		_, err := s.GetState(context.Background())
		errCh <- err
	}()
	next := f.waitForCommand(2)
	id2, _ := next[1]["id"].(string)
	f.write(`{"id":"` + id2 + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"ok"}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("session unusable after cancellation: %v", err)
	}
}

func TestBashDecodesResult(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})
	type result struct {
		res *BashResult
		err error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := s.Bash(context.Background(), "systemctl is-active nginx")
		ch <- result{r, err}
	}()
	cmds := f.waitForCommand(1)
	if cmds[0]["command"] != "systemctl is-active nginx" {
		t.Fatalf("command not forwarded: %v", cmds[0])
	}
	id, _ := cmds[0]["id"].(string)
	f.write(`{"id":"` + id + `","type":"response","command":"bash","success":true,"data":{"output":"active\n","exitCode":0,"cancelled":false,"truncated":true,"fullOutputPath":"/tmp/pi-bash-1.log"}}`)

	got := <-ch
	if got.err != nil {
		t.Fatalf("Bash: %v", got.err)
	}
	if got.res.Output != "active\n" || got.res.ExitCode != 0 || !got.res.Truncated {
		t.Fatalf("bad result: %+v", got.res)
	}
	if got.res.FullOutputPath != "/tmp/pi-bash-1.log" {
		t.Fatalf("fullOutputPath lost: %+v", got.res)
	}
}

func TestPromptSendsStreamingBehavior(t *testing.T) {
	s, f := newFakePi(t, SessionOptions{})
	go func() { _ = s.Prompt(context.Background(), "diagnose nginx", BehaviorSteer) }()
	cmds := f.waitForCommand(1)
	if cmds[0]["streamingBehavior"] != "steer" {
		t.Fatalf("behaviour not forwarded: %v", cmds[0])
	}

	go func() { _ = s.Prompt(context.Background(), "idle prompt", "") }()
	cmds = f.waitForCommand(2)
	if _, ok := cmds[1]["streamingBehavior"]; ok {
		t.Fatalf("empty behaviour must be omitted: %v", cmds[1])
	}
}

func TestLargeResponseRoundTrips(t *testing.T) {
	// get_commands on a host with skills installed exceeds the 64 KiB
	// a naive line reader allows.
	s, f := newFakePi(t, SessionOptions{})
	errCh := make(chan error, 1)
	go func() {
		_, err := s.Do(context.Background(), map[string]any{"type": "get_commands"})
		errCh <- err
	}()
	cmds := f.waitForCommand(1)
	id, _ := cmds[0]["id"].(string)
	pad := strings.Repeat("z", 200<<10)
	f.write(`{"id":"` + id + `","type":"response","command":"get_commands","success":true,"data":{"pad":"` + pad + `"}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("large response failed: %v", err)
	}
}

func TestCloseIsIdempotentAndFailsNewCommands(t *testing.T) {
	s, _ := newFakePi(t, SessionOptions{})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.GetState(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("want ErrSessionClosed, got %v", err)
	}
}

func TestNewSessionRequiresPipes(t *testing.T) {
	if _, err := NewSession(SessionOptions{Stdout: strings.NewReader("")}); err == nil {
		t.Fatal("want error without Stdin")
	}
	if _, err := NewSession(SessionOptions{Stdin: writerFunc(func(b []byte) (int, error) { return len(b), nil })}); err == nil {
		t.Fatal("want error without Stdout")
	}
}
