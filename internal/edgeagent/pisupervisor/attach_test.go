package pisupervisor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/pirpc"
)

// fakePiScript writes a shell script that speaks just enough of the
// RPC protocol to be probed: it answers every get_state with a
// success response and echoes the id back.
func fakePiScript(t *testing.T, answerGetState bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Pi peer is a POSIX shell script")
	}
	body := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"type":"get_state"'*)
`
	if answerGetState {
		body += `      printf '{"id":"%s","type":"response","command":"get_state","success":true,"data":{"sessionId":"fake","isStreaming":false}}\n' "$id"
`
	} else {
		body += `      : # deliberately silent: process alive but not answering
`
	}
	body += `      ;;
  esac
done
`
	path := filepath.Join(t.TempDir(), "fake-pi.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

type recordingSink struct {
	mu       sync.Mutex
	sessions int
	cleared  int
	current  *pirpc.Session
}

func (r *recordingSink) SetPiSession(s *pirpc.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions++
	r.current = s
}

func (r *recordingSink) ClearPiSession() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared++
	r.current = nil
}

func (r *recordingSink) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions, r.cleared
}

func TestRPCAttachProbeMarksChildRunning(t *testing.T) {
	bin := fakePiScript(t, true)
	sink := &recordingSink{}
	s, err := New(Config{
		Bin:            bin,
		Attach:         RPCAttach(sink, nil, time.Second),
		HealthInterval: 20 * time.Millisecond,
		HealthTimeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop(3 * time.Second) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().State == StateRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Status().State; got != StateRunning {
		t.Fatalf("State=%s; want running (RPC probe never succeeded)", got)
	}
	if s.Status().LastHealthyAt.IsZero() {
		t.Fatalf("LastHealthyAt not set by the RPC probe")
	}
	if set, _ := sink.counts(); set != 1 {
		t.Fatalf("session handed to sink %d times, want 1", set)
	}
}

func TestRPCAttachRestartsChildThatStopsAnswering(t *testing.T) {
	// The process stays alive and keeps reading stdin, but never
	// answers. A PID check would call this healthy; the RPC probe
	// must not.
	bin := fakePiScript(t, false)
	sink := &recordingSink{}
	s, err := New(Config{
		Bin:                bin,
		Attach:             RPCAttach(sink, nil, time.Second),
		HealthInterval:     20 * time.Millisecond,
		HealthTimeout:      60 * time.Millisecond,
		UnhealthyThreshold: 2,
		RestartBackoffMin:  10 * time.Millisecond,
		RestartBackoffMax:  20 * time.Millisecond,
		RestartMaxBurst:    2,
		RestartWindow:      time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop(3 * time.Second) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.RestartCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.RestartCount() == 0 {
		t.Fatalf("unresponsive child was never restarted; state=%s", s.Status().State)
	}
	if _, cleared := sink.counts(); cleared == 0 {
		t.Fatalf("session was not cleared when the child was replaced")
	}
}

func TestRPCAttachSurvivesNilSink(t *testing.T) {
	bin := fakePiScript(t, true)
	s, err := New(Config{
		Bin:            bin,
		Attach:         RPCAttach(nil, nil, 0),
		HealthInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Stop(3 * time.Second) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().State == StateRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("State=%s; want running", s.Status().State)
}
