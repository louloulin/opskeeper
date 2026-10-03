//go:build e2e

package testenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Edge is one running node process under test, with the credentials it
// dialled home with.
//
// It is a process, not a simulation. That is the entire point of the
// delivery acceptance: the questions being answered are "does a node boot,
// connect, and serve a turn", and a harness that answers them with an
// in-process stand-in answers a different set of questions — the ones the
// nodefleet e2e already answers.
type Edge struct {
	// ID is the manager's row id for this node, i.e. the number the console
	// routes a conversation by.
	ID uint64
	// AccessKey / SecretKey are the tunnel credential pair. The agent is
	// configured with the *same* pair as its model credential, which is the
	// whole gateway design: a node holds no provider key, and the only
	// secret on it is the one that identifies it to its own manager.
	AccessKey string
	SecretKey string

	// ConfigDir is the node's agent scope (models.json, the piglet
	// profile). It is a temp dir, and the test asserts on what is in it.
	ConfigDir string
	// WorkDir is the agent's working directory, where its packages would be
	// unpacked.
	WorkDir string

	cmd     *exec.Cmd
	logBuf  *bytes.Buffer
	stopped sync.Once
}

var (
	edgeBinOnce sync.Once
	edgeBinPath string
	edgeBinErr  error

	pigBinOnce sync.Once
	pigBinPath string
	pigBinErr  error
)

// EdgeBinary builds cmd/opskeeper-edge once per `go test`.
func EdgeBinary(t *testing.T) string {
	t.Helper()
	edgeBinOnce.Do(func() {
		repo := repoRoot()
		if repo == "" {
			edgeBinErr = fmt.Errorf("cannot locate repo root from testenv source")
			return
		}
		dir, err := os.MkdirTemp("", "opskeeper-e2e-edge-")
		if err != nil {
			edgeBinErr = err
			return
		}
		out := filepath.Join(dir, "opskeeper-edge")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/opskeeper-edge")
		cmd.Dir = repo
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			edgeBinErr = fmt.Errorf("go build ./cmd/opskeeper-edge: %w\n%s", err, buf.String())
			return
		}
		edgeBinPath = out
	})
	if edgeBinErr != nil {
		t.Fatalf("testenv: %v", edgeBinErr)
	}
	return edgeBinPath
}

// PigBinary builds the node's agent once per `go test`, from core/pig with
// the workspace off.
//
// GOWORK=off is not a detail: that is how the node's agent is built for
// real (Makefile build-pig-*), and building it through the workspace would
// link the repository's own packages into a binary that ships without
// them. A harness that built the agent the easy way would pass against a
// binary no node ever runs.
func PigBinary(t *testing.T) string {
	t.Helper()
	pigBinOnce.Do(func() {
		repo := repoRoot()
		pigDir := filepath.Join(repo, "core", "pig")
		if _, err := os.Stat(filepath.Join(pigDir, "go.mod")); err != nil {
			pigBinErr = fmt.Errorf("core/pig module not found: %w", err)
			return
		}
		dir, err := os.MkdirTemp("", "opskeeper-e2e-pig-")
		if err != nil {
			pigBinErr = err
			return
		}
		out := filepath.Join(dir, "pig")
		cmd := exec.Command("go", "build", "-trimpath", "-o", out, "github.com/MichaelKinsy/PiG/cmd/pig")
		cmd.Dir = pigDir
		cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			pigBinErr = fmt.Errorf("go build pig (GOWORK=off): %w\n%s", err, buf.String())
			return
		}
		pigBinPath = out
	})
	if pigBinErr != nil {
		t.Fatalf("testenv: %v", pigBinErr)
	}
	return pigBinPath
}

// EdgeOptions is what a node needs to be a node.
type EdgeOptions struct {
	// FrontierEdgeAddr is the broker's edgebound listener.
	FrontierEdgeAddr string
	// AccessKey / SecretKey are the credentials from POST /api/v1/edges.
	AccessKey string
	SecretKey string
	// GatewayBaseURL is the manager's OpenAI-compatible endpoint, root
	// form (http://host:port/v1). The node's agent is pointed at this and
	// nowhere else, which is the property the whole edge-holds-no-provider-
	// key design rests on.
	GatewayBaseURL string
	// Model is the slug the gateway will serve. Must be a model the
	// manager's registry can resolve, or the turn fails at the gateway
	// with a message about the cluster.
	Model string
}

// StartEdge spawns a node process and waits for it to answer for itself.
//
// Waiting is on the manager's view of the node's supervisor, not on a sleep
// and not on the tunnel connect. "The tunnel is up" and "the node's agent
// is running" are different facts, and only the second one means a
// conversation has anything to talk to.
func StartEdge(t *testing.T, env *Env, bearer string, opts EdgeOptions) *Edge {
	t.Helper()

	edge := &Edge{
		AccessKey: opts.AccessKey,
		SecretKey: opts.SecretKey,
		ConfigDir: t.TempDir(),
		WorkDir:   t.TempDir(),
	}
	// The node writes its agent scope and profile into ConfigDir; the
	// working directory is where packages are unpacked. Both are temp dirs
	// so the assertion "no cloud credential on this node" is about a
	// directory the test owns end to end.
	for _, d := range []string{edge.ConfigDir, edge.WorkDir} {
		if err := os.MkdirAll(filepath.Join(d, "packages"), 0o750); err != nil {
			t.Fatalf("testenv: prepare node dir: %v", err)
		}
	}

	edgeEnv := map[string]string{
		"OPSKEEPER_EDGE_CLOUD_ADDR":           opts.FrontierEdgeAddr,
		"OPSKEEPER_EDGE_ACCESS_KEY":           opts.AccessKey,
		"OPSKEEPER_EDGE_SECRET_KEY":           opts.SecretKey,
		"OPSKEEPER_EDGE_COLLECTOR_MODE":       "off",
		"OPSKEEPER_EDGE_TELEMETRY_WAL_DIR":    filepath.Join(edge.WorkDir, "telemetry"),
		"OPSKEEPER_EDGE_CHANGE_EVENT_WAL_DIR": filepath.Join(edge.WorkDir, "changes"),
		"OPSKEEPER_EDGE_UPGRADE_STAGE_DIR":    filepath.Join(edge.WorkDir, "upgrade"),
		"OPSKEEPER_EDGE_PLUGIN_WORK_DIR":      filepath.Join(edge.WorkDir, "plugins"),
		"OPSKEEPER_EDGE_PLUGIN_STORE_DIR":     filepath.Join(edge.WorkDir, "plugins"),
		// The agent's own scope. ConfigDir is the node's; the binary is
		// the one built above; the working dir is where its packages would
		// live. Nothing here is a cloud credential: the token is the node's
		// own tunnel pair, formatted the way the gateway authenticates.
		"OPSKEEPER_EDGE_AGENT_CONFIG_DIR": edge.ConfigDir,
		"OPSKEEPER_EDGE_AGENT_DIR":        edge.WorkDir,
		"OPSKEEPER_EDGE_AGENT_BIN":        PigBinary(t),
		"OPSKEEPER_EDGE_AGENT_BASE_URL":   opts.GatewayBaseURL,
		"OPSKEEPER_EDGE_AGENT_TOKEN":      opts.AccessKey + ":" + opts.SecretKey,
		"OPSKEEPER_EDGE_AGENT_MODEL":      opts.Model,
	}

	edge.logBuf = &bytes.Buffer{}
	cmd := exec.Command(EdgeBinary(t))
	cmd.Env = mergedEnv(edgeEnv)
	cmd.Stdout = edge.logBuf
	cmd.Stderr = edge.logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("testenv: start edge: %v", err)
	}
	edge.cmd = cmd
	t.Cleanup(edge.Stop)
	return edge
}

// Stop terminates the node process. Idempotent.
func (e *Edge) Stop() {
	e.stopped.Do(func() {
		if e.cmd == nil || e.cmd.Process == nil {
			return
		}
		_ = e.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = e.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = e.cmd.Process.Kill()
			<-done
		}
	})
}

// Logs returns everything the node has written, for a failure message.
func (e *Edge) Logs() string {
	if e.logBuf == nil {
		return ""
	}
	return e.logBuf.String()
}

// AgentPIDs returns the pids of `pig` processes this node started.
//
// It reads the process table rather than asking the node, because the
// acceptance is "an operator running ps on the host sees an independent
// agent process" and asking the node whether it started a process cannot
// answer that — a supervisor that lost track of its child would happily
// report success.
func (e *Edge) AgentPIDs(t *testing.T) []int {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err != nil {
		t.Fatalf("testenv: ps: %v", err)
	}
	binary := PigBinary(t)
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Match the exact binary this node was told to run, not any pig on
		// the developer's machine: the test asserts about its own child.
		if strings.HasPrefix(fields[1], binary) {
			var pid int
			if _, err := fmt.Sscanf(fields[0], "%d", &pid); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// WaitForRunningAgent polls the manager until the node reports a running
// agent, and returns that report.
//
// The report is returned rather than discarded because a node that is
// running a crash-looping agent also eventually answers "running": the
// restarts and last_error fields are what distinguish a node with an agent
// from a node that keeps failing to start one, and a test that only checked
// the boolean would pass on the second one.
func (e *Edge) WaitForRunningAgent(t *testing.T, env *Env, bearer string, edgeID uint64, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]any
	var lastStatus int
	for time.Now().Before(deadline) {
		status, body, err := env.DoJSON("GET", fmt.Sprintf("/api/v1/node-agents/%d/health", edgeID), nil, bearer)
		if err == nil {
			lastStatus = status
			last = body
			if status == 200 {
				if running, _ := body["running"].(bool); running {
					if degraded, _ := body["degraded"].(bool); degraded {
						t.Fatalf("node agent is running but degraded: %s", MustJSON(body))
					}
					return body
				}
			}
		}
		if e.cmd.ProcessState != nil && e.cmd.ProcessState.Exited() {
			t.Fatalf("edge exited before its agent came up (code %d):\n%s",
				e.cmd.ProcessState.ExitCode(), e.Logs())
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("node agent did not come up within %s (last status %d: %s)\n=== edge logs ===\n%s\n=== end edge logs ===",
		timeout, lastStatus, MustJSON(last), e.Logs())
	return nil
}

// MustJSON renders a body for a failure message.
func MustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// CreateEdge registers a node through the console's own API and returns its
// id and credential pair.
//
// Through the API rather than by inserting a row: the credential pair is
// minted by the same code a real operator's is, and a harness that generated
// one itself would be testing a node that can never be created by the
// product.
func (e *Env) CreateEdge(t *testing.T, bearer, name string) (uint64, string, string) {
	t.Helper()
	status, body, err := e.DoJSON("POST", "/api/v1/edges", map[string]string{"name": name}, bearer)
	if err != nil {
		t.Fatalf("create edge: transport: %v", err)
	}
	if status != 201 {
		t.Fatalf("create edge: status=%d body=%s", status, MustJSON(body))
	}
	id := uint64(0)
	if raw, ok := body["id"].(float64); ok {
		id = uint64(raw)
	}
	access, _ := body["access_key_id"].(string)
	secret, _ := body["secret_key"].(string)
	if id == 0 || access == "" || secret == "" {
		t.Fatalf("create edge: incomplete response: %s", MustJSON(body))
	}
	return id, access, secret
}

// StreamConversation opens a conversation's SSE stream and returns the
// frames as they arrive on a channel, plus a stop function.
//
// The console's order is load-bearing and this reproduces it: attach first,
// then send. A nodeagent Service refuses a turn nobody is watching
// (ErrNoStream -> 409 not_streaming), which is the right product decision
// and means a harness that posts the message first is testing a path the
// console never takes.
func (e *Env) StreamConversation(t *testing.T, bearer, sessionID string) (<-chan map[string]any, func()) {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/node-agents/sessions/%s/stream", e.BaseURL(), sessionID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "text/event-stream")

	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatalf("stream: %v", err)
	}
	if resp.StatusCode != 200 {
		cancel()
		t.Fatalf("stream: status=%d", resp.StatusCode)
	}

	frames := make(chan map[string]any, 64)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				continue
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frames, cancel
}
