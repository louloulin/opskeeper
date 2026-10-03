//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/tests/e2e/testenv"
)

// TestTheGatewayServesAStreamToANodeCredential isolates the first hop.
//
// The delivery path has three hops — node agent to gateway, gateway to
// provider, frames back — and a failure in a full-topology test cannot say
// which one broke. This test cuts the node out entirely: it mints a node
// credential through the same API an operator uses and calls the gateway
// with it, which is exactly what a node's agent does over the wire.
//
// It is here because a node that cannot get a model answer has no
// conversation to have, so "the turn produced no frames" is only
// interpretable once this hop is known to work.
func TestTheGatewayServesAStreamToANodeCredential(t *testing.T) {
	env := testenv.Start(t)
	login := env.LoginAdmin()
	env.FakeLLM().SetLLMReply("网关直连探针。")

	_, access, secret := env.CreateEdge(t, login.AccessToken, "gateway-probe-node")

	// Streamed, because that is what the node's agent asks for, and the two
	// paths are different code: the gateway settles the provider's reply and
	// re-encodes it as frames.
	status, body, err := env.DoJSON("POST", "/v1/chat/completions", map[string]any{
		"model": "fake-gpt",
		"messages": []map[string]any{
			{"role": "user", "content": "ping"},
		},
		"stream": true,
	}, access+":"+secret)
	if err != nil {
		t.Fatalf("gateway stream: transport: %v", err)
	}
	if status != 200 {
		t.Fatalf("gateway stream: status=%d body=%s", status, testenv.MustJSON(body))
	}
	if env.FakeLLM().CallCount() == 0 {
		t.Fatalf("the gateway answered without calling a model; it is not proxying\n%s", env.ManagerLogs())
	}
	// The body, not the status line. This assertion was the one that would
	// have caught the bug this test was written next to: the gateway settled
	// the provider reply with no content blocks, wrote a well-formed stream
	// with nothing in it, and answered 200. A hop test that checks the
	// status and stops is a hop test that cannot tell a working hop from a
	// silent one.
	stream, err := env.StreamBody("/v1/chat/completions", map[string]any{
		"model": "fake-gpt",
		"messages": []map[string]any{
			{"role": "user", "content": "ping"},
		},
		"stream": true,
	}, access+":"+secret)
	if err != nil {
		t.Fatalf("gateway stream body: transport: %v", err)
	}
	if !strings.Contains(stream, "网关直连探针") {
		t.Fatalf("the stream carried no model text; a gateway that answers 200 with an "+
			"empty stream is indistinguishable, to every client, from a broken one\nbody: %s", stream)
	}
}

// The delivery acceptance, run against real processes.
//
// What is real: the manager binary, the node binary, the node's agent —
// the pig binary built from core/pig with the workspace off, the same way
// a release builds it — a frontier broker, the node's own sockets, the
// OpenAI-compatible gateway on the manager, and the console's SSE frame
// contract. Every frame this test asserts on was produced by a model call
// that left the node, crossed a broker, and came back.
//
// What is substituted: the model. The upstream is the harness's fake LLM,
// so this proves the delivery path and says nothing about how good the
// answers are. The distinction is stated in the failure messages too,
// because a future reader who finds this test green must not be left
// thinking a node agent has been shown to reason.
//
// Why a separate file rather than another case in the nodefleet e2e: that
// suite substitutes the transport (an in-process loopback) and the agent
// process (a scripted stand-in), which is the right trade for three
// operational scenarios and the wrong one for this question. The question
// here is precisely whether the real halves fit together, and a test that
// replaces both halves cannot answer it.
func TestNodeAgentDelivery(t *testing.T) {
	frontier := testenv.SharedFrontier(t)
	env := testenv.Start(t, testenv.WithFrontier(frontier))
	login := env.LoginAdmin()

	// The gateway has to serve a model the manager can resolve, and the
	// harness's fake is wired in as the openai provider. "fake-gpt" is the
	// same slug the manager's env declares, so the node names a model the
	// cluster actually has rather than one the gateway has to invent.
	const model = "fake-gpt"
	env.FakeLLM().SetLLMReply("节点 Agent 已通过网关完成一次对话。")

	edgeID, access, secret := env.CreateEdge(t, login.AccessToken, "delivery-node")

	edge := testenv.StartEdge(t, env, login.AccessToken, testenv.EdgeOptions{
		FrontierEdgeAddr: frontier.EdgeAddr,
		AccessKey:        access,
		SecretKey:        secret,
		GatewayBaseURL:   env.BaseURL() + "/v1",
		Model:            model,
	})
	edge.ID = edgeID

	health := edge.WaitForRunningAgent(t, env, login.AccessToken, edgeID, 3*time.Minute)
	t.Logf("node reports agent health: %s", testenv.MustJSON(health))

	t.Run("the agent is an independent process", func(t *testing.T) {
		pids := edge.AgentPIDs(t)
		if len(pids) == 0 {
			t.Fatalf("no pig process on the host; the node did not run an independent agent\n=== edge logs ===\n%s", edge.Logs())
		}
		// One node, one agent. A second would mean the supervisor started a
		// replacement without reaping the first, which is a leak that only
		// shows up on a node that has restarted its agent — the case a
		// single-pid check would miss.
		if len(pids) > 1 {
			t.Errorf("node is running %d agent processes (%v), want exactly 1", len(pids), pids)
		}
	})

	t.Run("the node holds no provider credential", func(t *testing.T) {
		// The property is the plan's: a node holds no cloud vendor key, so
		// a compromised node cannot spend the operator's budget. The only
		// secret the node was given is its own tunnel pair, and even that is
		// a *reference* in models.json rather than a value — the node
		// passes it in the process environment and the agent expands it.
		//
		// What this asserts is therefore two-sided: the manager's provider
		// key appears nowhere under the node's agent scope, and the node's
		// own pair appears in no file either. The second half matters
		// because a harness that only checked the first would still pass if
		// someone "fixed" a failure by writing the token into models.json.
		providerKey := "fake-test-key"
		offenders := scanDirFor(t, edge.ConfigDir, providerKey, access, secret)
		if len(offenders) > 0 {
			t.Errorf("node agent scope holds credential material: %s", strings.Join(offenders, ", "))
		}
	})

	sid := openConversation(t, env, edge, login.AccessToken, edgeID)
	frames, stopStream := env.StreamConversation(t, login.AccessToken, sid)
	defer stopStream()

	status, body, err := env.DoJSON("POST",
		fmt.Sprintf("/api/v1/node-agents/sessions/%s/messages", sid),
		map[string]any{"content": "介绍一下你自己"}, login.AccessToken)
	if err != nil {
		t.Fatalf("send turn: transport: %v", err)
	}
	if status != 202 {
		t.Fatalf("send turn: status=%d body=%s", status, testenv.MustJSON(body))
	}

	seen := collectUntilDone(t, frames, env, edge, login.AccessToken, turnTimeout)

	t.Run("the turn streams back on the console's frame contract", func(t *testing.T) {
		var text strings.Builder
		var order []string
		for _, frame := range seen {
			kind, _ := frame["type"].(string)
			order = append(order, kind)
			if kind != "assistant_delta" {
				continue
			}
			assistant, _ := frame["assistant"].(map[string]any)
			if assistant == nil {
				continue
			}
			if chunk, ok := assistant["content"].(string); ok {
				text.WriteString(chunk)
			}
		}
		if len(order) == 0 {
			t.Fatalf("no frames at all\n=== edge logs ===\n%s\n=== manager logs ===\n%s", edge.Logs(), env.ManagerLogs())
		}
		if order[0] != "assistant_start" {
			t.Errorf("first frame is %q, want assistant_start (a console that renders a delta with no start has nothing to attach it to)", order[0])
		}
		if order[len(order)-1] != "done" {
			t.Errorf("last frame is %q, want done\nframes: %v", order[len(order)-1], order)
		}
		if !containsFrame(seen, "assistant_delta") {
			t.Errorf("no assistant_delta frame; the reply did not stream\nframes: %v", order)
		}
		if got := text.String(); !strings.Contains(got, "网关") {
			t.Errorf("streamed text is %q, want the model reply the fake served", got)
		}
		for _, frame := range seen {
			if kind, _ := frame["type"].(string); kind == "error" {
				t.Errorf("the turn produced an error frame: %s", testenv.MustJSON(frame))
			}
		}
	})

	t.Run("the reply came through the manager's gateway", func(t *testing.T) {
		// The node never held a provider key, so the only way a reply can
		// exist is if the node's agent called the manager with the node's
		// own credential and the manager served it. The gateway logs that
		// fact per request, tagged with the node it authenticated, which is
		// stronger evidence than "an answer appeared": it names the edge.
		logs := env.ManagerLogs()
		if !strings.Contains(logs, "llmgw: stream served") {
			t.Fatalf("the gateway served no stream\n=== manager logs ===\n%s", logs)
		}
		// Both spellings, because the manager's handler is JSON in this
		// configuration and logfmt in the other one, and an assertion
		// written against the wrong one is a test that fails on a working
		// gateway — which is how it read when the reply was still missing.
		named := strings.Contains(logs, fmt.Sprintf("edge_id=%d", edgeID)) ||
			strings.Contains(logs, fmt.Sprintf(`"edge_id":%d`, edgeID))
		if !named {
			t.Errorf("the gateway served a stream but never named node %d; the node's identity and its model traffic are not the same fact\n=== manager logs ===\n%s", edgeID, logs)
		}
		if env.FakeLLM().CallCount() == 0 {
			t.Errorf("the model was never called; the reply did not come from a model")
		}
	})

	t.Run("a turn with no watcher is refused", func(t *testing.T) {
		// The console's contract, and the reason StreamConversation exists:
		// a turn is answered to a stream somebody is reading. This is the
		// negative of the path above, and without it a harness could pass
		// by sending turns into the void and reading the frames from
		// somewhere else.
		lonely := openConversation(t, env, edge, login.AccessToken, edgeID)
		status, body, err := env.DoJSON("POST",
			fmt.Sprintf("/api/v1/node-agents/sessions/%s/messages", lonely),
			map[string]any{"content": "没有人看我"}, login.AccessToken)
		if err != nil {
			t.Fatalf("send unwatched turn: transport: %v", err)
		}
		if status != 409 {
			t.Errorf("an unwatched turn returned %d, want 409 not_streaming (body=%s)", status, testenv.MustJSON(body))
		}
	})
}

// openConversation opens a node conversation and returns its id.
func openConversation(t *testing.T, env *testenv.Env, edge *testenv.Edge, bearer string, edgeID uint64) string {
	t.Helper()
	status, body, err := env.DoJSON("POST", "/api/v1/node-agents/sessions", map[string]any{
		"edge_id": edgeID,
	}, bearer)
	if err != nil {
		t.Fatalf("open conversation: transport: %v\n=== edge logs ===\n%s", err, edge.Logs())
	}
	if status != 200 {
		t.Fatalf("open conversation: status=%d body=%s\n=== manager logs ===\n%s",
			status, testenv.MustJSON(body), env.ManagerLogs())
	}
	sid, _ := body["session_id"].(string)
	if sid == "" {
		t.Fatalf("open conversation: no session id: %s", testenv.MustJSON(body))
	}
	return sid
}

// turnTimeout bounds one turn.
//
// Sixty seconds is generous for a loopback fake and short enough that a
// hang is a failure message rather than a wait. It is not a performance
// budget: the model here is a fixture, so a turn that has not finished in a
// minute is not a slow turn, it is a turn that went somewhere nobody is
// watching.
const turnTimeout = 60 * time.Second

// collectUntilDone reads frames until the turn ends.
//
// A failure here prints the node's own audit ledger and the control plane's
// view of its open conversations, because "no frames" has at least three
// very different causes — the agent never ran, it ran and its frames were
// dropped in transit, or the frames arrived for a conversation the control
// plane could not attribute — and they need different fixes. The frame list
// alone cannot tell them apart.
func collectUntilDone(t *testing.T, frames <-chan map[string]any, env *testenv.Env, edge *testenv.Edge, bearer string, timeout time.Duration) []map[string]any {
	t.Helper()
	var out []map[string]any
	deadline := time.After(timeout)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("the stream closed before the turn ended\nframes: %s", testenv.MustJSON(out))
			}
			out = append(out, frame)
			if kind, _ := frame["type"].(string); kind == "done" {
				return out
			}
		case <-deadline:
			t.Fatalf("the turn did not end within %s\nframes: %s\nmodel calls: %d %v\nconversations: %s\nnode ledger: %s\n=== edge logs ===\n%s\n=== manager logs ===\n%s",
				timeout, testenv.MustJSON(out),
				env.FakeLLM().CallCount(), env.FakeLLM().ModelsRequested(),
				testenv.MustJSON(env.NodeConversations(t, bearer)),
				testenv.ReadFileOrEmpty(t, filepath.Join(edge.WorkDir, "audit-ledger.jsonl")),
				edge.Logs(), env.ManagerLogs())
		}
	}
}

func containsFrame(frames []map[string]any, kind string) bool {
	for _, frame := range frames {
		if k, _ := frame["type"].(string); k == kind {
			return true
		}
	}
	return false
}

// scanDirFor returns the files under root that contain any needle.
//
// It reads bytes rather than treating every file as text: the files that
// matter most to this assertion are the ones a credential would be smuggled
// into, and a "could not parse as text, so it is fine" rule is exactly how
// a key ends up in a file nobody reads.
func scanDirFor(t *testing.T, root string, needles ...string) []string {
	t.Helper()
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, needle := range needles {
			if needle == "" {
				continue
			}
			if strings.Contains(string(body), needle) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s contains %q", rel, needle))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	return offenders
}
