package pirpc

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Process is a running Pi child plus its RPC session.
type Process struct {
	// Session speaks RPC to the child.
	Session *Session

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	waited chan error
}

// Spawn starts Pi in RPC mode and attaches a Session to its pipes.
//
// The caller owns shutdown: call Close. Restart policy, backoff and
// crash-loop gating stay in pisupervisor — this helper exists so the
// supervisor (and the E2E test) can obtain a live session without
// duplicating pipe wiring.
//
// Stderr is not captured here; the caller sets cmd.Stderr through
// Configure so edge logging keeps ownership of it.
func Spawn(ctx context.Context, opts LaunchOptions, sess SessionOptions, configure func(*exec.Cmd)) (*Process, error) {
	bin, args, err := opts.Command()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if configure != nil {
		configure(cmd)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pirpc: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pirpc: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pirpc: start %s: %w", bin, err)
	}

	sess.Stdin = stdin
	sess.Stdout = stdout
	session, err := NewSession(sess)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}

	p := &Process{Session: session, cmd: cmd, stdin: stdin, waited: make(chan error, 1)}
	go func() { p.waited <- cmd.Wait() }()
	return p, nil
}

// PID reports the child's process id, or 0 once it is reaped.
func (p *Process) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Close shuts the child down: close the session, close Pi's stdin
// (which is how Pi learns to exit), then wait up to grace before
// killing it. Returns the child's exit error, if any.
func (p *Process) Close(grace time.Duration) error {
	_ = p.Session.Close()
	_ = p.stdin.Close()

	if grace <= 0 {
		grace = 5 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-p.waited:
		return err
	case <-timer.C:
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		return <-p.waited
	}
}

// Wait blocks until the child exits.
func (p *Process) Wait() error { return <-p.waited }
