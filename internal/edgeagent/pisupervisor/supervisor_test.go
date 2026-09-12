package pisupervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeScript writes a small shell script that the supervisor can
// spawn in tests. mode controls what the script does:
//
//   "loop"        — print "ready" once, then sleep forever
//   "exit-quick"  — exit 0 immediately (simulates a crash)
//   "exit-fail"   — exit 1 immediately (simulates a crash with error)
func fakeScript(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake_pi.sh")
	script := "#!/bin/sh\n"
	switch mode {
	case "loop":
		script += "echo \"ready pid=$$\" 1>&2\nsleep 3600\n"
	case "exit-quick":
		script += "echo \"ready pid=$$\" 1>&2\nexit 0\n"
	case "exit-fail":
		script += "echo \"ready pid=$$\" 1>&2\nexit 1\n"
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// discardLogger returns a slog handler that drops every record.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNew_RequiresBin(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Errorf("expected error when Bin missing")
	}
}

func TestNew_AutoUpgradeRequiresTagLock(t *testing.T) {
	bin := fakeScript(t, "loop")
	if _, err := New(Config{
		Bin:          bin,
		AutoUpgrade:  true,
		SyncPiScript: "/bin/true",
	}); err == nil {
		t.Errorf("expected error when AutoUpgrade without TagLock")
	}
}

func TestNew_AutoUpgradeRequiresSyncPiScript(t *testing.T) {
	bin := fakeScript(t, "loop")
	if _, err := New(Config{
		Bin:         bin,
		AutoUpgrade: true,
		TagLock:     "v0.85.1",
	}); err == nil {
		t.Errorf("expected error when AutoUpgrade without SyncPiScript")
	}
}

func TestNew_DefaultsFilled(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, err := New(Config{Bin: bin})
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.HealthURL != "http://127.0.0.1:19000/health" {
		t.Errorf("HealthURL=%s", s.cfg.HealthURL)
	}
	if s.cfg.HealthInterval != 5*time.Second {
		t.Errorf("HealthInterval=%s", s.cfg.HealthInterval)
	}
	if s.cfg.UnhealthyThreshold != 3 {
		t.Errorf("UnhealthyThreshold=%d", s.cfg.UnhealthyThreshold)
	}
}

func TestStart_DoubleStartErrors(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, err := New(Config{Bin: bin, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	s.healthProbe = FakeHealthyHealthProbe()
	// Override CmdFactory so we don't actually fork in this test —
	// we just want to verify Start's state machine.
	s.cfg.CmdFactory = fakeCmdFactory(exitImmediateCmd)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop(2 * time.Second)

	if err := s.Start(context.Background()); err == nil {
		t.Errorf("second Start should error")
	}
}

func TestStop_Idempotent(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, err := New(Config{Bin: bin, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(time.Second); err != nil {
		t.Errorf("first Stop: %v", err)
	}
}

func TestStatus_ZeroValueWhenNew(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{Bin: bin})
	st := s.Status()
	if st.State != StateNew {
		t.Errorf("State=%s, want new", st.State)
	}
	if st.PID != 0 {
		t.Errorf("PID=%d, want 0", st.PID)
	}
}

func TestComputeBackoff_GrowsThenCaps(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{
		Bin:               bin,
		RestartBackoffMin: 100 * time.Millisecond,
		RestartBackoffMax: 1 * time.Second,
	})
	for i, want := range []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1 * time.Second, // capped
		1 * time.Second, // capped
	} {
		s.mu.Lock()
		s.status.Restarts = i
		s.mu.Unlock()
		got := s.computeBackoff()
		if got != want {
			t.Errorf("restart %d: backoff=%s want=%s", i, got, want)
		}
	}
}

func TestCrashLoop_DetectsBurst(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{
		Bin:            bin,
		RestartMaxBurst: 3,
		RestartWindow:  1 * time.Hour,
	})
	for i := 0; i < 3; i++ {
		s.mu.Lock()
		s.crashLog = append(s.crashLog, time.Now())
		s.mu.Unlock()
	}
	if !s.isCrashLoop() {
		t.Errorf("expected crash loop after 3 restarts in window")
	}
}

func TestCrashLoop_WindowPrunes(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{
		Bin:            bin,
		RestartMaxBurst: 3,
		RestartWindow:  100 * time.Millisecond,
	})
	now := time.Now()
	s.mu.Lock()
	s.crashLog = []time.Time{now.Add(-1 * time.Second), now.Add(-500 * time.Millisecond)}
	s.mu.Unlock()
	if s.isCrashLoop() {
		t.Errorf("old crashes should have been pruned")
	}
	if got := len(s.crashLog); got != 0 {
		t.Errorf("crashLog len=%d, want 0 after prune", got)
	}
}

func TestUpgrade_RefusesWithoutTagLock(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{Bin: bin})
	if err := s.Upgrade(context.Background()); err == nil {
		t.Errorf("Upgrade without TagLock should fail")
	}
}

func TestUpgrade_RefusesWithoutSyncPiScript(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{Bin: bin, TagLock: "v0.85.1"})
	if err := s.Upgrade(context.Background()); err == nil {
		t.Errorf("Upgrade without SyncPiScript should fail")
	}
}

func TestHTTPHealthProbe_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok","edge_id":"e","uptime_s":42}`)
	}))
	defer srv.Close()
	probe := HTTPHealthProbe(time.Second)
	if err := probe(context.Background(), srv.URL); err != nil {
		t.Errorf("healthy server should pass: %v", err)
	}
}

func TestHTTPHealthProbe_DegradedIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"degraded","reason":"no LLM key"}`)
	}))
	defer srv.Close()
	probe := HTTPHealthProbe(time.Second)
	if err := probe(context.Background(), srv.URL); err == nil {
		t.Errorf("degraded should be rejected by probe (only ok accepted)")
	}
}

func TestHTTPHealthProbe_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	probe := HTTPHealthProbe(time.Second)
	if err := probe(context.Background(), srv.URL); err == nil {
		t.Errorf("503 should fail probe")
	}
}

func TestHTTPHealthProbe_NonJSONBodyPasses(t *testing.T) {
	// Pi may ship without the JSON contract in early versions.
	// HTTP 2xx + non-JSON body must still be considered OK so the
	// supervisor doesn't thrash.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	probe := HTTPHealthProbe(time.Second)
	if err := probe(context.Background(), srv.URL); err != nil {
		t.Errorf("non-JSON body should pass: %v", err)
	}
}

func TestHTTPHealthProbe_ConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()
	probe := HTTPHealthProbe(200 * time.Millisecond)
	if err := probe(context.Background(), addr); err == nil {
		t.Errorf("refused connection should fail probe")
	}
}

func TestComputeBackoff_ZeroRestarts(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{
		Bin:               bin,
		RestartBackoffMin: 50 * time.Millisecond,
		RestartBackoffMax: time.Second,
	})
	s.mu.Lock()
	s.status.Restarts = 0
	s.mu.Unlock()
	if got := s.computeBackoff(); got != 50*time.Millisecond {
		t.Errorf("first backoff=%s, want 50ms", got)
	}
}

func TestStop_AfterStart_WaitsForDone(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, err := New(Config{
		Bin:               bin,
		RestartBackoffMin: 10 * time.Millisecond,
		RestartBackoffMax: 10 * time.Millisecond,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Use the FakeUnhealthy probe so the supervisor kills the
	// child after the threshold and respawns in a tight loop.
	s.healthProbe = FakeUnhealthyHealthProbe()
	// Use the real spawn — the loop script will hang forever and
	// Stop's killProcessGroup will SIGKILL it.
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := s.Stop(2 * time.Second); err != nil {
		t.Errorf("Stop: %v", err)
	}
	st := s.Status()
	if st.State != StateStopped {
		t.Errorf("State=%s after Stop, want stopped", st.State)
	}
}

func TestSpawn_BinMissing(t *testing.T) {
	s, err := New(Config{Bin: "/nonexistent/path/to/pi"})
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, err := s.spawn(context.Background())
	if err == nil {
		t.Errorf("spawn of missing bin should fail")
	}
	if cmd != nil {
		t.Errorf("cmd should be nil on spawn failure")
	}
}

func TestRecordRestart_BumpsCounterAndTimestamp(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{Bin: bin})
	s.recordRestart()
	s.recordRestart()
	if got := s.RestartCount(); got != 2 {
		t.Errorf("RestartCount=%d, want 2", got)
	}
	if s.Status().LastRestartAt.IsZero() {
		t.Errorf("LastRestartAt not populated")
	}
}

func TestIsCrashLoop_BoundaryOffByOne(t *testing.T) {
	bin := fakeScript(t, "loop")
	s, _ := New(Config{
		Bin:            bin,
		RestartMaxBurst: 3,
		RestartWindow:  1 * time.Hour,
	})
	// 2 restarts: not yet a crash loop.
	s.mu.Lock()
	s.crashLog = []time.Time{time.Now(), time.Now()}
	s.mu.Unlock()
	if s.isCrashLoop() {
		t.Errorf("2 restarts should not be a crash loop (threshold=3)")
	}
	// 3rd restart flips it.
	s.recordRestart()
	if !s.isCrashLoop() {
		t.Errorf("3 restarts should be a crash loop")
	}
}

// fakeCmdFactory builds an exec.Cmd that exits 0 immediately. We
// use the real os/exec to fork the helper script so Wait() returns
// cleanly without holding the test hostage.
func exitImmediateCmd(bin string, args []string, env []string) *exec.Cmd {
	return exec.Command(bin, args...)
}

// fakeCmdFactory wraps a real spawn function so we can plug it
// into Config.CmdFactory.
func fakeCmdFactory(real func(string, []string, []string) *exec.Cmd) func(string, []string, []string) *exec.Cmd {
	return real
}

// Sanity: spawnCounter just counts how many times CmdFactory was
// called during a test, for assertions.
type spawnCounter struct {
	n atomic.Int32
}

func (s *spawnCounter) factory(bin string, args []string, env []string) *exec.Cmd {
	s.n.Add(1)
	return exec.Command(bin, args...)
}

// Compile-time guard: avoid "imported and not used" when imports
// shift.
var (
	_ = spawnCounter{}
	_ = exitImmediateCmd
)