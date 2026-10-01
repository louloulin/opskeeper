package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// This file drives the client through PiG's real provider stack over a local
// HTTP endpoint rather than a stub.
//
// The distinction is the whole point. The thing that can break here is the
// CONVERSION — OpsKeeper's OpenAI-flavoured ChatReq into PiG's normalized
// transcript and back — and a stub would accept any shape, including exactly
// the malformed ones a real provider rejects. A stub-based test therefore
// passes while production shows "the model stopped calling tools".
//
// PiG ships a deterministic faux provider, but its Go type is unexported
// (ai.NewFauxProvider returns an unnameable struct), so it cannot be handed to
// the registry, which caches ai.Provider values. The registry is exercised
// through its real construction path instead: settings -> ai.NewOpenAIProvider,
// which also proves the base URL, auth header, and request body are right.

// openAITextSSE is one complete OpenAI-compatible stream: a text delta and a
// terminating chunk that carries usage. The usage numbers are chosen so a
// mapping bug is visible: prompt 11 + completion 4 must surface as 11/4/15,
// not as a recomputed 11/4/11.
const openAITextSSE = `data: {"id":"chatcmpl-test","choices":[{"index":0,"delta":{"content":"the answer"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}

data: [DONE]

`

// openAIToolSSE answers with a tool call instead of text. It is what proves
// the tool-call block survives PiG's parser on the way back out.
const openAIToolSSE = `data: {"id":"chatcmpl-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"get_host_load","arguments":"{\"host\":\"node-01\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}

data: [DONE]

`

// pigTestServer is a local stand-in for a provider endpoint. It records every
// request body so a test can assert what actually went on the wire — the only
// place a silent transcript regression is observable.
type pigTestServer struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies []map[string]any
}

func (s *pigTestServer) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.bodies))
	copy(out, s.bodies)
	return out
}

// newPigTestClient wires a real Client onto a registry whose only provider
// points at a local server echoing sse. Everything between Chat and the socket
// is production code: the settings source, the registry, PiG's OpenAI provider,
// and the transcript conversion.
func newPigTestClient(t *testing.T, sse string, budget BudgetChecker) (Client, *pigTestServer) {
	t.Helper()
	sink := &pigTestServer{}
	sink.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			sink.mu.Lock()
			sink.bodies = append(sink.bodies, body)
			sink.mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(sink.srv.Close)

	src := pigmodel.NewStaticSource(map[domain.ProviderID]pigmodel.ProviderConfig{
		domain.ProviderCustom: {
			ID:           domain.ProviderCustom,
			APIKey:       "sk-test",
			BaseURL:      sink.srv.URL,
			Models:       []string{"ops-test"},
			DefaultModel: "ops-test",
		},
	}, domain.ProviderCustom)

	client, err := NewPigClient(PigClientConfig{
		Registry: pigmodel.NewRegistry(src),
		Settings: src,
		Budget:   budget,
	})
	if err != nil {
		t.Fatalf("NewPigClient: %v", err)
	}
	return client, sink
}

// TestPigClientRequiresARegistry pins the fail-closed constructor. Without a
// registry there is no provider to resolve, and a client that answered anyway
// would have to invent a model name.
func TestPigClientRequiresARegistry(t *testing.T) {
	t.Parallel()
	if _, err := NewPigClient(PigClientConfig{}); err == nil {
		t.Fatal("expected an error when no registry is supplied")
	}
}

// TestSelectionForResolvesAnUnpinnedModel pins that naming a model without a
// provider is a request the registry fulfils, not an error. The SPA model
// picker sends exactly this shape when the operator never pinned a provider.
func TestSelectionForResolvesAnUnpinnedModel(t *testing.T) {
	t.Parallel()
	src := pigmodel.NewStaticSource(map[domain.ProviderID]pigmodel.ProviderConfig{
		"faux": {ID: "faux", APIKey: "k", Models: []string{"m1"}, DefaultModel: "m1"},
	}, "faux")

	sel, err := selectionFor(ChatReq{Model: "m1"}, src)
	if err != nil {
		t.Fatalf("selectionFor: %v", err)
	}
	if sel.Provider != "" || sel.Model != "m1" {
		t.Fatalf("selection = %+v, want the model with no provider so the registry scans", sel)
	}
}

// TestSelectionForUsesTheConfiguredDefault pins the empty-request path: no
// provider and no model must resolve to the cluster default rather than an
// error, because that is how every unpinned caller (the RCA worker, the judge)
// asks.
func TestSelectionForUsesTheConfiguredDefault(t *testing.T) {
	t.Parallel()
	src := pigmodel.NewStaticSource(map[domain.ProviderID]pigmodel.ProviderConfig{
		"faux": {ID: "faux", APIKey: "k", DefaultModel: "m1"},
	}, "faux")
	sel, err := selectionFor(ChatReq{}, src)
	if err != nil {
		t.Fatalf("selectionFor: %v", err)
	}
	if sel.Provider != "faux" {
		t.Fatalf("provider = %q, want the configured default", sel.Provider)
	}
}

// TestSelectionForRefusesWithNothingToResolveWith pins the fail-closed case: a
// request naming nothing, with no settings source to consult, must be refused.
// Guessing OpenAI here would silently send a tenant's traffic to a host the
// operator never configured.
func TestSelectionForRefusesWithNothingToResolveWith(t *testing.T) {
	t.Parallel()
	if _, err := selectionFor(ChatReq{}, nil); err == nil {
		t.Fatal("expected an error with no provider, no model, and no settings")
	}
	// But a named model IS resolvable without settings.
	if _, err := selectionFor(ChatReq{Model: "m1"}, nil); err != nil {
		t.Fatalf("a named model must be resolvable without a settings source: %v", err)
	}
}

// TestBuildTranscriptRoundTripsTheOperationsShapes pins the conversion the
// whole package depends on: system/user/assistant-with-tools/tool-result must
// survive into PiG's transcript with ids and arguments intact. A silent drop
// here does not fail a test in production — it shows up as a model that stops
// calling tools.
func TestPigClientChatEndToEnd(t *testing.T) {
	t.Parallel()
	client, sink := newPigTestClient(t, openAITextSSE, nil)

	resp, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{
			{Role: "system", Content: "you are an SRE"},
			{Role: "user", Content: "why slow?"},
		},
		Tools: []ToolSchema{{
			Name:        "get_host_load",
			Description: "reads load",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Assistant.Content != "the answer" {
		t.Fatalf("content = %q, want %q", resp.Assistant.Content, "the answer")
	}
	if resp.Assistant.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", resp.Assistant.Role)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v, want the provider's 15 tokens", resp.Usage)
	}

	// The wire body is the only place a silent transcript regression is
	// visible: a dropped system message or an unnamed tool is answered by a
	// model that never calls a tool, with no error to point at.
	bodies := sink.requests()
	if len(bodies) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(bodies))
	}
	if got := bodies[0]["model"]; got != "ops-test" {
		t.Errorf("model = %v, want the resolved default ops-test", got)
	}
	msgs, _ := bodies[0]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "you are an SRE" {
		t.Errorf("messages[0] = %v, want the system turn intact", first)
	}
	tools, _ := bodies[0]["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "get_host_load" {
		t.Errorf("tool = %v, want get_host_load declared with its schema", tool)
	}
}

// TestPigClientKeepsToolCallsEndToEnd pins the return leg: a tool call the
// provider emits must arrive as a ToolCall, not be flattened away. This is
// what the agent loop branches on, so losing it silently ends multi-step
// diagnosis after the first round.
func TestPigClientKeepsToolCallsEndToEnd(t *testing.T) {
	t.Parallel()
	client, _ := newPigTestClient(t, openAIToolSSE, nil)

	resp, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "check node-01"}},
		Tools: []ToolSchema{{
			Name:        "get_host_load",
			Description: "reads load",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Assistant.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want 1", resp.Assistant.ToolCalls)
	}
	call := resp.Assistant.ToolCalls[0]
	if call.ID != "call_9" || call.Name != "get_host_load" {
		t.Fatalf("tool call = %+v, want call_9/get_host_load", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("tool call args are not JSON: %v (%q)", err, call.Args)
	}
	if args["host"] != "node-01" {
		t.Errorf("args = %v, want host=node-01 reassembled from the streamed fragments", args)
	}
}

// TestPigClientBudgetGateRunsBeforeTheProvider pins the billing contract. A
// budget refusal must not consume a provider call — the same rule the HTTP
// client enforces — or a capped tenant would still be charged. The assertion
// is on the server's request count, not on a mock's, because only the server
// knows whether bytes were really sent.
func TestPigClientBudgetGateRunsBeforeTheProvider(t *testing.T) {
	t.Parallel()
	budget := &countingBudget{checkErr: ErrBudgetExceeded}
	client, sink := newPigTestClient(t, openAITextSSE, budget)

	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
		UserID:   7,
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if budget.checkCalls != 1 {
		t.Errorf("budget.Check calls = %d, want 1", budget.checkCalls)
	}
	if budget.recordCalls != 0 {
		t.Errorf("budget.Record calls = %d, want 0 on a refused turn", budget.recordCalls)
	}
	if got := len(sink.requests()); got != 0 {
		t.Errorf("provider saw %d requests, want 0: a refused turn must not bill", got)
	}
}

// TestPigClientRecordsUsageAfterSuccess pins the other side of the ledger: the
// usage the provider reported reaches Record, so the budget reflects what was
// actually spent.
func TestPigClientRecordsUsageAfterSuccess(t *testing.T) {
	t.Parallel()
	budget := &countingBudget{}
	client, _ := newPigTestClient(t, openAITextSSE, budget)

	if _, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
		UserID:   7,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if budget.recordCalls != 1 {
		t.Fatalf("budget.Record calls = %d, want 1 after a successful turn", budget.recordCalls)
	}
	if budget.lastUsage.TotalTokens != 15 {
		t.Errorf("recorded usage = %+v, want the provider's 15 tokens", budget.lastUsage)
	}
}

// TestPigClientSatisfiesTheClientInterface pins the plan's instruction
// literally: the interface survives, only the implementation changes. A
// compile-time assertion is the weakest useful form of that, and it is the one
// that keeps a future refactor from quietly widening the interface.
func TestPigClientSatisfiesTheClientInterface(t *testing.T) {
	t.Parallel()
	var _ Client = (*pigClient)(nil)
}
