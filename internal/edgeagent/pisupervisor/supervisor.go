// Package pisupervisor is the edge-side lifecycle controller for
// the Pi-coding-agent sidecar process. It owns:
//
//   - Spawn — launch `pi --mode rpc` as a child process with the
//     configured env (LLM keys, edge endpoint, skill roots) and
//     argv built by pirpc.LaunchOptions.
//   - Health — round-trip a `get_state` RPC command over the child's
//     stdio on a steady interval; on missing / unhealthy, declare the
//     process down. (Earlier revisions probed
//     `http://127.0.0.1:<port>/health`. Pi ships no HTTP mode and no
//     health endpoint — see Config.Attach and pirpc's package doc.)
//   - Restart — when the process exits or stays unhealthy past a
//     threshold, kill (if alive) and respawn with exponential
//     backoff. Restart count is bounded per rolling window so a
//     crash-loop can't spin forever.
//   - Upgrade — on operator trigger (or auto-upgrade policy), shell
//     out to scripts/sync-pi.sh, then restart Pi with the new
//     version.
//   - Install — apply OPSKEEPER_PI_EXTRA_PACKAGES once at boot
//     before the first spawn; failure aborts the supervisor start
//     so the operator notices a misconfigured package list.
//
// Design pillars (kept terse; the long form lives in plan1.0.md
// §P-2):
//
//   1. Pi runs as a child of edge — when edge dies, Pi dies. There
//      is no orphan / re-parent scenario because we use
//      Setpgid + a process-group kill on shutdown.
//
//   2. The supervisor is the single writer to Pi's env. Operators
//      edit `.env` / config; the supervisor reads at boot and never
//      mutates the live env afterwards (re-spawn is the only
//      mechanism for config changes).
//
//   3. Auto-upgrade is OFF by default. The operator must opt in
//      with OPSKEEPER_PI_AUTO_UPGRADE=true AND must have already
//      pinned the tag with OPSKEEPER_PI_TAG_LOCK. Without the
//      pin, Upgrade() refuses — protects against tag-flipping in
//      upstream.
//
//   4. All public methods are safe for concurrent use. Status()
//      returns a snapshot without taking the long-held lock.
//   5. The package never imports Pi's runtime types. The supervisor
//      treats Pi as a black box that speaks the documented JSONL RPC
//      protocol on stdio and writes structured logs to stderr. This
//      keeps the upgrade path decoupled from Pi version bumps.
package pisupervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Config is the supervisor's static configuration. Constructed at
// edge boot from env / yaml; once handed to New, fields are not
// mutated by the supervisor.
type Config struct {
	// Bin is the path to the Pi executable (or any HTTP-server
	// binary for tests). Required.
	Bin string

	// Args is the argv passed to Bin (excluding argv[0]). Build it
	// with pirpc.LaunchOptions.Command so the flags match what the
	// pinned Pi release actually accepts; for the vendored bundle
	// that is ["<cli.js>", "--mode", "rpc", ...].
	Args []string

	// Env is the environment block passed to Bin. Use os.Environ()
	// for the host's env + add OPSKEEPER_PI_* keys + LLM keys +
	// traceparent context.
	Env []string

	// Attach, when set, makes the supervisor own the child's stdio
	// and hand it to a protocol client after every successful spawn.
	// It returns the liveness probe for that child plus a teardown
	// func run when the child exits.
	//
	// This is the production path: the returned probe round-trips a
	// real RPC command, which proves Pi is reading stdin, parsing
	// JSONL and answering — something neither a PID check nor the
	// HTTP endpoint Pi does not have could show. Use
	// RPCAttach to build one.
	//
	// When nil, the supervisor falls back to HealthURL + the legacy
	// HTTP probe, which only fits a local shim (see health.go).
	Attach func(ctx context.Context, stdin io.WriteCloser, stdout io.Reader) (probe func(context.Context) error, teardown func(), err error)

	// HealthURL is the legacy HTTP probe target, used only when
	// Attach is nil. Defaults to
	// "http://127.0.0.1:19000/health" when empty.
	HealthURL string

	// HealthInterval is how often to probe. Default 5s.
	HealthInterval time.Duration

	// HealthTimeout is the per-probe deadline. Default 2s.
	HealthTimeout time.Duration

	// UnhealthyThreshold is the number of consecutive failed probes
	// before declaring the process down and triggering restart.
	// Default 3.
	UnhealthyThreshold int

	// RestartBackoffMin / RestartBackoffMax bound the exponential
	// backoff between restart attempts. Defaults 1s / 30s.
	RestartBackoffMin time.Duration
	RestartBackoffMax time.Duration

	// RestartWindow bounds the crash-loop detector. If we restart
	// RestartMaxBurst times within RestartWindow, the supervisor
	// stops trying and surfaces Status=CrashLoop. Defaults 5
	// restarts within 5 minutes.
	RestartMaxBurst int
	RestartWindow   time.Duration

	// AutoUpgrade, when true, calls Upgrade() before the first
	// spawn. Default false. OPSKEEPER_PI_AUTO_UPGRADE env wires
	// this. When true, TagLock MUST also be set.
	AutoUpgrade bool

	// TagLock is the exact Pi version the supervisor will run.
	// Required when AutoUpgrade=true. Without it Upgrade() refuses
	// to avoid upstream tag-flipping.
	TagLock string

	// ExtraPackages is the comma-separated list of Pi packages to
	// install before first spawn. Empty string → skip. The install
	// runner is just `pi install <pkg1> <pkg2> ...`; if any
	// package fails to install, the supervisor fails to start.
	ExtraPackages []string

	// SyncPiScript is the path to scripts/sync-pi.sh. Required when
	// AutoUpgrade=true (Upgrade shells out to it).
	SyncPiScript string

	// Logger is the structured log sink. Nil → slog.Default().
	Logger *slog.Logger

	// NowFn is the wall-clock source for backoff / window math.
	// Tests override. Nil → time.Now.
	NowFn func() time.Time

	// CmdFactory builds the *exec.Cmd. Tests override to inject
	// fake binaries; production leaves it nil and uses the default.
	CmdFactory func(bin string, args []string, env []string) *exec.Cmd
}

// Supervisor is the running controller. New() returns a stopped
// supervisor; call Start to spawn the loop.
type Supervisor struct {
	cfg Config

	mu       sync.Mutex
	status   Status
	child    *exec.Cmd
	stopCh   chan struct{}
	doneCh   chan struct{} // closed when the run loop exits
	crashLog []time.Time   // restart timestamps within the rolling window
	startedAt time.Time

	// attachProbe is the per-child liveness probe returned by
	// Config.Attach. Non-nil only while an attached child is alive;
	// it takes precedence over healthProbe.
	attachProbe func(context.Context) error

	// hooks for tests
	healthProbe func(ctx context.Context, url string) error
}

// Status is the snapshot returned by Status(). The zero value
// means "supervisor exists but has never been started".
type Status struct {
	State          State
	PID            int
	Restarts       int
	LastError      string
	LastRestartAt  time.Time
	LastHealthyAt  time.Time
	StartedAt      time.Time
	TagLock        string
}

// State is the supervisor lifecycle state.
type State int

const (
	StateNew State = iota
	StateStarting
	StateRunning
	StateUnhealthy
	StateRestarting
	StateCrashLoop
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateUnhealthy:
		return "unhealthy"
	case StateRestarting:
		return "restarting"
	case StateCrashLoop:
		return "crash_loop"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// New constructs a Supervisor with defaults filled in. Returns an
// error when required fields are missing.
func New(cfg Config) (*Supervisor, error) {
	if cfg.Bin == "" {
		return nil, errors.New("pisupervisor: Bin required")
	}
	if cfg.HealthURL == "" {
		cfg.HealthURL = "http://127.0.0.1:19000/health"
	}
	if cfg.HealthInterval <= 0 {
		cfg.HealthInterval = 5 * time.Second
	}
	if cfg.HealthTimeout <= 0 {
		cfg.HealthTimeout = 2 * time.Second
	}
	if cfg.UnhealthyThreshold <= 0 {
		cfg.UnhealthyThreshold = 3
	}
	if cfg.RestartBackoffMin <= 0 {
		cfg.RestartBackoffMin = time.Second
	}
	if cfg.RestartBackoffMax <= 0 {
		cfg.RestartBackoffMax = 30 * time.Second
	}
	if cfg.RestartMaxBurst <= 0 {
		cfg.RestartMaxBurst = 5
	}
	if cfg.RestartWindow <= 0 {
		cfg.RestartWindow = 5 * time.Minute
	}
	if cfg.AutoUpgrade && cfg.TagLock == "" {
		return nil, errors.New("pisupervisor: AutoUpgrade=true requires TagLock")
	}
	if cfg.AutoUpgrade && cfg.SyncPiScript == "" {
		return nil, errors.New("pisupervisor: AutoUpgrade=true requires SyncPiScript path")
	}
	s := &Supervisor{
		cfg:  cfg,
		stopCh: make(chan struct{}),
	}
	if cfg.Logger != nil {
		s.cfg.Logger = cfg.Logger
	} else {
		s.cfg.Logger = slog.Default()
	}
	if cfg.NowFn != nil {
		// capture for backoff math
	}
	if cfg.CmdFactory == nil {
		s.cfg.CmdFactory = defaultCmdFactory
	}
	// default health probe uses the package's HTTPHealth (see health.go).
	s.healthProbe = HTTPHealthProbe(cfg.HealthTimeout)
	return s, nil
}

// Start kicks off the run loop in a goroutine. Idempotent: a
// second call on an already-started supervisor returns an error.
// Returns immediately; the caller can wait on Done() or poll
// Status().
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.status.State != StateNew {
		s.mu.Unlock()
		return fmt.Errorf("pisupervisor: Start called in state %s", s.status.State)
	}
	s.status.State = StateStarting
	s.status.StartedAt = s.now()
	s.mu.Unlock()

	// Optional pre-flight install.
	if len(s.cfg.ExtraPackages) > 0 {
		if err := s.installExtraPackages(ctx); err != nil {
			s.recordError(fmt.Errorf("install extras: %w", err))
			s.setState(StateCrashLoop) // fail-closed: misconfigured → don't pretend to be running
			return err
		}
	}

	// Optional auto-upgrade.
	if s.cfg.AutoUpgrade {
		if err := s.runUpgrade(ctx); err != nil {
			s.recordError(fmt.Errorf("auto-upgrade: %w", err))
			s.setState(StateCrashLoop)
			return err
		}
	}

	s.doneCh = make(chan struct{})
	go s.runLoop(ctx)
	return nil
}

// Stop signals the supervisor to terminate. Idempotent. Waits up
// to `timeout` for the run loop to exit cleanly; after that any
// remaining child is killed via SIGKILL.
func (s *Supervisor) Stop(timeout time.Duration) error {
	s.mu.Lock()
	if s.status.State == StateStopped || s.status.State == StateNew {
		s.mu.Unlock()
		return nil
	}
	close(s.stopCh)
	child := s.child
	s.mu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	select {
	case <-s.doneCh:
		// clean exit
	case <-deadline.C:
		// force-kill the child if still alive.
		if child != nil && child.Process != nil {
			_ = killProcessGroup(child.Process.Pid)
		}
		<-s.doneCh
	}
	return nil
}

// Done returns a channel closed when the run loop exits. Useful
// for tests.
func (s *Supervisor) Done() <-chan struct{} { return s.doneCh }

// Status returns a snapshot of the supervisor's current state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// RestartCount returns how many times we've respawned the child
// since Start. Useful for the audit chain.
func (s *Supervisor) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Restarts
}

// runLoop is the long-lived goroutine. It spawns the child, watches
// its exit, restarts with backoff on crash, and runs the health
// probe concurrently.
func (s *Supervisor) runLoop(ctx context.Context) {
	defer close(s.doneCh)
	s.log().Info("pisupervisor: run loop starting",
		slog.String("bin", s.cfg.Bin),
		slog.String("health_url", s.cfg.HealthURL))

	for {
		// Crash-loop gate.
		if s.isCrashLoop() {
			s.log().Error("pisupervisor: crash loop detected; stopping",
				slog.Int("restarts", s.status.Restarts),
				slog.Duration("window", s.cfg.RestartWindow))
			s.setState(StateCrashLoop)
			return
		}

		// Spawn.
		s.setState(StateStarting)
		child, teardown, err := s.spawn(ctx)
		if err != nil {
			s.recordError(err)
			if !s.sleepBackoff(ctx) {
				return
			}
			continue
		}

		// Start health probe in a side goroutine. It updates the
		// status's LastHealthyAt and signals "unhealthy" via
		// killing the child to force restart.
		probeDone := make(chan struct{})
		go s.probeLoop(ctx, child, probeDone)

		// Wait for the child in a side goroutine so we can also
		// observe stopCh / ctx. childExited closes when Wait()
		// returns; runLoop reads it together with the stop
		// signals and acts.
		childExited := make(chan error, 1)
		go func() {
			childExited <- child.Wait()
		}()

		var waitErr error
		select {
		case waitErr = <-childExited:
			// child exited on its own
		case <-s.stopCh:
			// supervisor stopping — kill the child and wait for
			// the exit goroutine to drain.
			_ = killProcessGroup(child.Process.Pid)
			waitErr = <-childExited
		case <-ctx.Done():
			_ = killProcessGroup(child.Process.Pid)
			waitErr = <-childExited
		}
		close(probeDone)
		teardown()
		s.mu.Lock()
		s.attachProbe = nil
		s.mu.Unlock()

		if waitErr != nil {
			s.log().Warn("pisupervisor: child exited with error",
				slog.Int("pid", child.Process.Pid),
				slog.Any("err", waitErr))
		} else {
			s.log().Info("pisupervisor: child exited cleanly",
				slog.Int("pid", child.Process.Pid))
		}
		s.recordRestart()

		// If we got here via stopCh / ctx, exit cleanly.
		select {
		case <-s.stopCh:
			s.setState(StateStopped)
			return
		case <-ctx.Done():
			s.setState(StateStopped)
			return
		default:
		}
		if !s.sleepBackoff(ctx) {
			return
		}
	}
}

// spawn launches the configured binary. Returns the *exec.Cmd and a
// teardown func for whatever Attach set up. On failure (binary
// missing, fork error) returns an error.
func (s *Supervisor) spawn(ctx context.Context) (*exec.Cmd, func(), error) {
	if _, err := exec.LookPath(s.cfg.Bin); err != nil {
		// Bin may be a relative path or a script — LookPath
		// rejects those. Fall back to a direct path probe.
		if _, statErr := os.Stat(s.cfg.Bin); statErr != nil {
			return nil, nil, fmt.Errorf("pisupervisor: bin %q not resolvable: %w", s.cfg.Bin, err)
		}
	}
	cmd := s.cfg.CmdFactory(s.cfg.Bin, s.cfg.Args, s.cfg.Env)
	// Force the child into its own process group so we can kill
	// the whole tree on shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr

	// Pi's stdio IS the RPC channel, so it can only be piped when
	// Attach owns it. Without Attach, stdout is passed through as
	// before (opskeeper-edge already routes it to the audit log).
	var stdin io.WriteCloser
	var stdout io.Reader
	if s.cfg.Attach != nil {
		var err error
		if stdin, err = cmd.StdinPipe(); err != nil {
			return nil, nil, fmt.Errorf("pisupervisor: stdin pipe: %w", err)
		}
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return nil, nil, fmt.Errorf("pisupervisor: stdout pipe: %w", err)
		}
	} else {
		cmd.Stdout = os.Stdout
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("pisupervisor: start %q: %w", s.cfg.Bin, err)
	}

	teardown := func() {}
	if s.cfg.Attach != nil {
		probe, tdown, err := s.cfg.Attach(ctx, stdin, stdout)
		if err != nil {
			_ = killProcessGroup(cmd.Process.Pid)
			_ = cmd.Wait()
			return nil, nil, fmt.Errorf("pisupervisor: attach: %w", err)
		}
		if tdown != nil {
			teardown = tdown
		}
		s.mu.Lock()
		s.attachProbe = probe
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.child = cmd
	s.status.PID = cmd.Process.Pid
	s.mu.Unlock()
	s.log().Info("pisupervisor: spawned child",
		slog.Int("pid", cmd.Process.Pid),
		slog.String("bin", s.cfg.Bin),
		slog.Bool("rpc_attached", s.cfg.Attach != nil))
	return cmd, teardown, nil
}

// isCrashLoop returns true if the rolling-window restart count
// exceeds the configured threshold. Side-effect: evict timestamps
// outside the window so the count reflects the current window.
func (s *Supervisor) isCrashLoop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-s.cfg.RestartWindow)
	pruned := s.crashLog[:0]
	for _, t := range s.crashLog {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	s.crashLog = pruned
	return len(s.crashLog) >= s.cfg.RestartMaxBurst
}

// recordRestart bumps the restart count and appends a timestamp
// to the crash window.
func (s *Supervisor) recordRestart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Restarts++
	s.status.LastRestartAt = s.now()
	s.crashLog = append(s.crashLog, s.status.LastRestartAt)
}

// sleepBackoff waits the next backoff interval, returning false if
// the supervisor should stop (stop signal or ctx cancelled).
func (s *Supervisor) sleepBackoff(ctx context.Context) bool {
	s.setState(StateRestarting)
	d := s.computeBackoff()
	s.log().Info("pisupervisor: backoff before next spawn",
		slog.Duration("sleep", d))
	select {
	case <-time.After(d):
		return true
	case <-s.stopCh:
		s.setState(StateStopped)
		return false
	case <-ctx.Done():
		s.setState(StateStopped)
		return false
	}
}

// computeBackoff returns the backoff for the next attempt.
// Exponential capped at RestartBackoffMax, starting from
// RestartBackoffMin.
func (s *Supervisor) computeBackoff() time.Duration {
	s.mu.Lock()
	restarts := s.status.Restarts
	s.mu.Unlock()
	d := s.cfg.RestartBackoffMin
	for i := 0; i < restarts && d < s.cfg.RestartBackoffMax; i++ {
		d *= 2
	}
	if d > s.cfg.RestartBackoffMax {
		d = s.cfg.RestartBackoffMax
	}
	return d
}

// probeLoop polls the child's /health endpoint. On consecutive
// failures exceeding UnhealthyThreshold, it kills the child to
// force the parent run loop into a restart.
func (s *Supervisor) probeLoop(ctx context.Context, child *exec.Cmd, done chan struct{}) {
	consecutiveFailures := 0
	ticker := time.NewTicker(s.cfg.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
		pctx, cancel := context.WithTimeout(ctx, s.cfg.HealthTimeout)
		var err error
		if probe := s.currentProbe(); probe != nil {
			err = probe(pctx)
		} else {
			err = s.healthProbe(pctx, s.cfg.HealthURL)
		}
		cancel()
		if err == nil {
			consecutiveFailures = 0
			s.mu.Lock()
			s.status.LastHealthyAt = s.now()
			if s.status.State != StateRunning {
				s.log().Info("pisupervisor: child healthy",
					slog.Int("pid", child.Process.Pid))
				s.status.State = StateRunning
			}
			s.mu.Unlock()
			continue
		}
		consecutiveFailures++
		s.log().Warn("pisupervisor: health probe failed",
			slog.Int("consecutive_failures", consecutiveFailures),
			slog.Any("err", err),
			slog.Int("threshold", s.cfg.UnhealthyThreshold))
		if consecutiveFailures >= s.cfg.UnhealthyThreshold {
			s.setState(StateUnhealthy)
			s.log().Error("pisupervisor: unhealthy threshold reached; killing child",
				slog.Int("pid", child.Process.Pid))
			_ = killProcessGroup(child.Process.Pid)
			return
		}
	}
}

// installExtraPackages invokes `pi install <pkg1> <pkg2> ...`.
// Failures abort the supervisor start (fail-closed).
func (s *Supervisor) installExtraPackages(ctx context.Context) error {
	args := append([]string{"install"}, s.cfg.ExtraPackages...)
	cmd := s.cfg.CmdFactory(s.cfg.Bin, args, s.cfg.Env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pisupervisor: install %v: %w", s.cfg.ExtraPackages, err)
	}
	return nil
}

// runUpgrade shells out to scripts/sync-pi.sh. The script enforces
// the TagLock (per plan §G-5) so we don't double-validate here.
func (s *Supervisor) runUpgrade(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, s.cfg.SyncPiScript, "--target", s.cfg.TagLock)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pisupervisor: upgrade via %s: %w", s.cfg.SyncPiScript, err)
	}
	return nil
}

// Upgrade triggers an out-of-band upgrade. The supervisor stops the
// child, runs sync-pi.sh, then respawns. Safe to call from a
// different goroutine than runLoop.
func (s *Supervisor) Upgrade(ctx context.Context) error {
	if s.cfg.TagLock == "" {
		return errors.New("pisupervisor: Upgrade requires TagLock (refusing to flip upstream tag)")
	}
	if s.cfg.SyncPiScript == "" {
		return errors.New("pisupervisor: Upgrade requires SyncPiScript path")
	}
	// Stop the child first so the upgrade's git pull + render-check
	// doesn't race with Pi reading its own skill files.
	if err := s.Stop(10 * time.Second); err != nil {
		return fmt.Errorf("pisupervisor: stop before upgrade: %w", err)
	}
	if err := s.runUpgrade(ctx); err != nil {
		return err
	}
	// Restart with a fresh stopCh / doneCh so Stop+Start are clean.
	s.mu.Lock()
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.status = Status{State: StateStarting, StartedAt: s.now(), TagLock: s.cfg.TagLock}
	s.mu.Unlock()
	go s.runLoop(ctx)
	return nil
}

// currentProbe returns the attached child's liveness probe, or nil
// when no Attach is configured or the child is gone.
func (s *Supervisor) currentProbe() func(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachProbe
}

func (s *Supervisor) setState(st State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != st {
		s.log().Info("pisupervisor: state change",
			slog.String("from", s.status.State.String()),
			slog.String("to", st.String()))
		s.status.State = st
	}
}

func (s *Supervisor) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastError = err.Error()
	s.log().Error("pisupervisor: error", slog.Any("err", err))
}

func (s *Supervisor) log() *slog.Logger {
	if s.cfg.Logger == nil {
		return slog.Default()
	}
	return s.cfg.Logger
}

func (s *Supervisor) now() time.Time {
	if s.cfg.NowFn != nil {
		return s.cfg.NowFn()
	}
	return time.Now()
}

// defaultCmdFactory builds a real *exec.Cmd. Production uses this;
// tests inject their own to capture argv / fork fakes.
func defaultCmdFactory(bin string, args []string, env []string) *exec.Cmd {
	return exec.Command(bin, args...)
}

// killProcessGroup sends SIGKILL to the negative PID, which the
// kernel interprets as "kill the whole process group". This
// guarantees Pi's children (skill subprocesses, the LLM stream)
// die with it.
func killProcessGroup(pid int) error {
	if pid <= 0 {
		return errors.New("pisupervisor: invalid pid")
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}