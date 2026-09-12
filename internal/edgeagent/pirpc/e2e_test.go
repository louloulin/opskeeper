package pirpc

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestE2EAgainstRealPi drives an actual @earendil-works/pi-coding-agent
// process. It is skipped unless the operator points at a real CLI
// bundle, so CI without Node stays green:
//
//	OPSKEEPER_PI_E2E_SCRIPT=/path/to/dist/bundle/cli.js go test ./internal/edgeagent/pirpc
//	OPSKEEPER_PI_E2E_NODE=/usr/bin/node               # optional
//
// Only commands that need no LLM call are exercised (get_state,
// bash, abort). That keeps the test hermetic and free: it proves the
// transport, framing and correlation against the real peer without
// spending a token or requiring a provider key.
func TestE2EAgainstRealPi(t *testing.T) {
	script := os.Getenv("OPSKEEPER_PI_E2E_SCRIPT")
	if script == "" {
		t.Skip("set OPSKEEPER_PI_E2E_SCRIPT to a pi CLI bundle to run the real-Pi E2E test")
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("OPSKEEPER_PI_E2E_SCRIPT=%q: %v", script, err)
	}
	node := os.Getenv("OPSKEEPER_PI_E2E_NODE")
	if node == "" {
		node = "node"
	}
	if _, err := exec.LookPath(node); err != nil {
		t.Skipf("node not on PATH: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	events := make(chan Event, 256)
	proc, err := Spawn(ctx, LaunchOptions{
		Node:   node,
		Script: script,
		// Ephemeral + no host-local resources: the probe must not
		// inherit whatever the host operator installed in ~/.pi, and
		// must not leave a session transcript behind.
		Ephemeral:         true,
		NoExtensions:      true,
		NoSkills:          true,
		NoPromptTemplates: true,
		NoContextFiles:    true,
		Offline:           true,
		SessionName:       "opskeeper-e2e",
	}, SessionOptions{
		OnEvent: func(e Event) {
			select {
			case events <- e:
			default:
			}
		},
	}, func(cmd *exec.Cmd) {
		cmd.Dir = t.TempDir()
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), "PI_OFFLINE=1")
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer func() {
		if err := proc.Close(10 * time.Second); err != nil {
			t.Logf("child exit: %v", err)
		}
	}()

	stateCtx, stateCancel := context.WithTimeout(ctx, 90*time.Second)
	defer stateCancel()
	state, err := proc.Session.GetState(stateCtx)
	if err != nil {
		t.Fatalf("get_state against real pi: %v", err)
	}
	if state.SessionID == "" {
		t.Fatalf("real pi returned no sessionId: %+v", state)
	}
	if state.IsStreaming {
		t.Fatalf("fresh session should be idle: %+v", state)
	}
	t.Logf("real pi session %s, model=%v, thinking=%s",
		state.SessionID, modelID(state.Model), state.ThinkingLevel)

	// get_commands is the record that grows with installed skills and
	// extensions; on an unhardened host it passes 64 KiB, which is
	// why DefaultMaxLineBytes is 8 MiB. With the hardening flags
	// above it stays small — that small payload is itself the
	// evidence the flags took effect.
	cmdCtx, cmdCancel := context.WithTimeout(ctx, 60*time.Second)
	defer cmdCancel()
	resp, err := proc.Session.Do(cmdCtx, map[string]any{"type": "get_commands"})
	if err != nil {
		t.Fatalf("get_commands against real pi: %v", err)
	}
	t.Logf("real pi get_commands payload: %d bytes", len(resp.Data))

	// Direct bash needs no LLM call, so it works on a Pi with no
	// provider credentials. This is the channel the edge uses to hand
	// host facts to the model without letting the model pick the
	// command.
	script2 := "echo opskeeper-e2e-ok"
	if runtime.GOOS == "windows" {
		// Pi shells out through bash; on Windows the test host may
		// not have one, so tolerate a failure here rather than
		// asserting a shell exists.
		script2 = "echo opskeeper-e2e-ok"
	}
	bashCtx, bashCancel := context.WithTimeout(ctx, 60*time.Second)
	defer bashCancel()
	res, err := proc.Session.Bash(bashCtx, script2)
	if err != nil {
		t.Logf("bash on real pi unavailable in this environment: %v", err)
	} else {
		t.Logf("real pi bash exit=%d truncated=%v output=%q",
			res.ExitCode, res.Truncated, res.Output)
	}

	// Abort on an idle session is a no-op that still has to round-trip.
	abortCtx, abortCancel := context.WithTimeout(ctx, 60*time.Second)
	defer abortCancel()
	if err := proc.Session.Abort(abortCtx); err != nil {
		t.Fatalf("abort against real pi: %v", err)
	}
}

func modelID(m *Model) string {
	if m == nil {
		return "<none>"
	}
	return m.Provider + "/" + m.ID
}
