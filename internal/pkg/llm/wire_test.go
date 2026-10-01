package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEndpointForDefaultsToOpenAIWhenBaseURLEmpty pins the fallback the
// removed SDK provided implicitly. If this regressed to an empty URL the
// client would POST to a relative path and every call would fail with an
// opaque "unsupported protocol scheme" instead of reaching OpenAI.
func TestEndpointForDefaultsToOpenAIWhenBaseURLEmpty(t *testing.T) {
	c := &openaiClient{}
	ep := c.endpointFor("sk-test", "")
	if ep.url != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("url = %q, want the OpenAI v1 chat endpoint", ep.url)
	}
	if ep.client == nil {
		t.Fatal("client = nil; a nil client panics on the first request")
	}
}

// TestEndpointForCachesByKeyNotByClient pins the rotation contract: a
// changed apiKey or baseURL must produce a *different* endpoint. Caching the
// bare client would let a rotated key keep using the previous gateway's
// Zhipu-aware transport (or keep POSTing at the old host).
func TestEndpointForCachesByKeyNotByClient(t *testing.T) {
	c := &openaiClient{}
	first := c.endpointFor("key-a", "https://a.example.com/v1")
	again := c.endpointFor("key-a", "https://a.example.com/v1")
	if first.url != again.url {
		t.Fatalf("same key returned different urls: %q vs %q", first.url, again.url)
	}

	otherHost := c.endpointFor("key-a", "https://b.example.com/v1")
	if otherHost.url == first.url {
		t.Fatalf("baseURL change reused the old url %q", first.url)
	}

	// Zhipu is the case where the *client* differs, not just the URL: the
	// JWT transport is installed only for a Zhipu-looking key+host pair.
	// Asserting the cached client object differs catches a change that
	// cached the URL correctly but shared one client across all keys.
	zhipu := c.endpointFor("id.secret", "https://open.bigmodel.cn/api/paas/v4")
	plain := c.endpointFor("sk-plain", "https://open.bigmodel.cn/api/paas/v4")
	if zhipu.client == plain.client {
		t.Fatal("zhipu and non-zhipu keys share one *http.Client; the JWT transport would leak onto the plain key")
	}
}

// TestPostChatCompletionSendsOpenAIShape asserts the bytes on the wire —
// header casing, auth, and the exact JSON keys a provider expects. A
// renamed json tag is invisible to every test that only reads the response,
// so the wire body is asserted here directly.
func TestPostChatCompletionSendsOpenAIShape(t *testing.T) {
	var (
		gotAuth        string
		gotContentType string
		gotAccept      string
		gotBody        map[string]any
		gotRaw         []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotRaw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(gotRaw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	t.Cleanup(srv.Close)

	c := &openaiClient{}
	ep := c.endpointFor("sk-test", srv.URL+"/v1")
	temp := float32(0.1)
	_, err := c.postChatCompletion(context.Background(), ep, "sk-test", &wireRequest{
		Model: "gpt-4o",
		Messages: []wireMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []wireToolCall{{
				ID: "call_1", Type: "function",
				Function: wireToolCallFunction{Name: "ping", Arguments: `{"a":1}`},
			}}},
			{Role: "tool", ToolCallID: "call_1", Name: "ping", Content: "pong"},
		},
		Tools: []wireTool{{
			Type: "function",
			Function: wireToolFunction{
				Name:        "ping",
				Description: "returns pong",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		}},
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("postChatCompletion: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	// tool_calls must survive as an array of objects, and the arguments must
	// stay a JSON *string* — a provider that receives an object here
	// double-decodes and rejects the turn.
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3; body=%s", len(msgs), gotRaw)
	}
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v, want call_1", toolMsg["tool_call_id"])
	}
	if _, present := toolMsg["tool_calls"]; present {
		t.Errorf("a role=tool message carried tool_calls; providers reject that")
	}
	asst, _ := msgs[1].(map[string]any)
	calls, _ := asst["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant tool_calls len = %d, want 1", len(calls))
	}
	call, _ := calls[0].(map[string]any)
	fn, _ := call["function"].(map[string]any)
	if fn["arguments"] != `{"a":1}` {
		t.Errorf("arguments = %v, want the raw JSON string", fn["arguments"])
	}
	if _, ok := gotBody["temperature"].(float64); !ok {
		t.Errorf("temperature missing or not numeric: %v", gotBody["temperature"])
	}
}

// TestPostChatCompletionOmitsTemperaturePointer pins the reasoning-model
// contract at the wire level, independent of the Chat() heuristic: a nil
// Temperature pointer must produce a body with no temperature key at all.
// `omitempty` on a float32 could not express this — 0 is the zero value —
// which is the whole reason the field is a pointer.
func TestPostChatCompletionOmitsTemperaturePointer(t *testing.T) {
	var sawKey bool
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(raw, &probe)
		_, sawKey = probe["temperature"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	c := &openaiClient{}
	ep := c.endpointFor("sk", srv.URL+"/v1")
	if _, err := c.postChatCompletion(context.Background(), ep, "sk", &wireRequest{
		Model:    "gpt-5.5",
		Messages: []wireMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("postChatCompletion: %v", err)
	}
	if sawKey {
		t.Fatalf("temperature key present for a nil pointer; body=%s", raw)
	}
}

// TestPostChatCompletionSurfacesProviderErrorText pins that a 400's message
// reaches the caller. The reactive sampling retry matches on this text, so
// dropping the provider's message (or wrapping it in a shape that hides it)
// silently disables the retry for every gateway alias.
func TestPostChatCompletionSurfacesProviderErrorText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"this model has beta-limitations, temperature, top_p and n are fixed at 1"}}`))
	}))
	t.Cleanup(srv.Close)

	c := &openaiClient{}
	ep := c.endpointFor("sk", srv.URL+"/v1")
	_, err := c.postChatCompletion(context.Background(), ep, "sk", &wireRequest{Model: "sol-max"})
	if err == nil {
		t.Fatal("expected an error for a 400")
	}
	if !isSamplingParamError(err) {
		t.Fatalf("isSamplingParamError(%q) = false; the reactive retry can never fire", err)
	}
	var httpErr *wireHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err type = %T, want *wireHTTPError so callers can branch on status", err)
	}
	if httpErr.statusCode != http.StatusBadRequest {
		t.Errorf("statusCode = %d, want 400", httpErr.statusCode)
	}
	// Pin the *extracted* message, not just "the text appears somewhere":
	// the raw body is also echoed by Error(), so a test that only greps for
	// the phrase would pass even if message extraction were deleted. This
	// prefix is only produced by the extracted field.
	if !strings.Contains(err.Error(), "message: this model has beta-limitations") {
		t.Errorf("err = %q, want the provider message extracted into the message field", err)
	}
}

// TestPostChatCompletionCapsErrorBody pins the truncation. A gateway that
// answers 502 with a multi-megabyte HTML page must not have that page
// copied into an error string that is logged and surfaced.
func TestPostChatCompletionCapsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"` + strings.Repeat("x", 1<<20) + `"}}`))
	}))
	t.Cleanup(srv.Close)

	c := &openaiClient{}
	ep := c.endpointFor("sk", srv.URL+"/v1")
	_, err := c.postChatCompletion(context.Background(), ep, "sk", &wireRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected an error")
	}
	var httpErr *wireHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err type = %T", err)
	}
	if len(httpErr.body) > 64<<10 {
		t.Fatalf("error body = %d bytes, want <= 64KiB", len(httpErr.body))
	}
}

// TestPostChatCompletionOmitsAuthWhenKeyEmpty pins the keyless-local-server
// path: Ollama / LM Studio are routinely pointed at with a placeholder or
// no key, and "Bearer " with nothing after it is a malformed header some
// gateways reject outright.
func TestPostChatCompletionOmitsAuthWhenKeyEmpty(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Header["Authorization"]; ok {
			auth = v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	c := &openaiClient{}
	ep := c.endpointFor("", srv.URL+"/v1")
	if _, err := c.postChatCompletion(context.Background(), ep, "", &wireRequest{Model: "m"}); err != nil {
		t.Fatalf("postChatCompletion: %v", err)
	}
	if len(auth) != 0 {
		t.Fatalf("Authorization sent for an empty key: %v", auth)
	}
}

// TestWireErrorMessageFormatIsPinned guards the exact prefix. The previous
// SDK produced "error, status code: N, status: S, message: M, body: B";
// error strings reach logs and are asserted in operator runbooks, so a
// silent reformat is an operational regression even though nothing fails.
func TestWireErrorMessageFormatIsPinned(t *testing.T) {
	e := &wireHTTPError{
		statusCode: 429,
		status:     "429 Too Many Requests",
		message:    "rate limited",
		body:       []byte(`{"error":{"message":"rate limited"}}`),
	}
	got := e.Error()
	for _, want := range []string{"error, status code: 429", "429 Too Many Requests", "rate limited"} {
		if !strings.Contains(got, want) {
			t.Errorf("error text %q missing %q", got, want)
		}
	}
}

// TestToWireReqRejectsEmptyRole pins fail-closed validation: a role-less
// message would be accepted by the encoder and rejected by the provider with
// a 400 that names no index, leaving the operator to bisect the message
// list. Naming the index here is the difference between a one-line fix and
// an afternoon.
func TestToWireReqRejectsEmptyRole(t *testing.T) {
	c := &openaiClient{}
	_, err := c.toWireReq(ChatReq{
		Messages: []Message{{Role: "user", Content: "fine"}, {Content: "no role"}},
	}, "gpt-4o")
	if err == nil {
		t.Fatal("expected an error for a role-less message")
	}
	if !strings.Contains(err.Error(), "message[1]") {
		t.Errorf("err = %q, want it to name message[1]", err)
	}
}

// TestToWireReqRejectsMalformedToolSchema pins that a bad schema fails at
// build time. json.RawMessage is emitted verbatim by encoding/json without
// validation, so an unchecked malformed schema would reach the provider and
// come back as a 400 naming no tool.
func TestToWireReqRejectsMalformedToolSchema(t *testing.T) {
	c := &openaiClient{}
	_, err := c.toWireReq(ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []ToolSchema{
			{Name: "good", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "bad", Parameters: json.RawMessage(`{not json`)},
		},
	}, "gpt-4o")
	if err == nil {
		t.Fatal("expected an error for a malformed tool schema")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("err = %q, want it to name the offending tool", err)
	}
}

// TestDecodeWireMessageCoercesEmptyArguments pins that a provider omitting
// `arguments` yields "{}" rather than a nil RawMessage. A nil blob is not
// valid JSON, so the tool executor would fail while decoding instead of
// executing the tool the model asked for.
func TestDecodeWireMessageCoercesEmptyArguments(t *testing.T) {
	got, err := decodeWireMessage(wireMessage{
		Role: "assistant",
		ToolCalls: []wireToolCall{{
			ID: "call_1", Type: "function",
			Function: wireToolCallFunction{Name: "ping"},
		}},
	})
	if err != nil {
		t.Fatalf("decodeWireMessage: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(got.ToolCalls))
	}
	if string(got.ToolCalls[0].Args) != "{}" {
		t.Fatalf("args = %q, want {}", got.ToolCalls[0].Args)
	}
	if !json.Valid(got.ToolCalls[0].Args) {
		t.Fatalf("args %q is not valid JSON", got.ToolCalls[0].Args)
	}
}

// TestChatWithoutBaseURLUsesOpenAIHost pins end-to-end that a client built
// without a BaseURL still POSTs to api.openai.com rather than a relative
// path. It cannot reach the network in a unit test, so it asserts the
// resolution step only — the property that would otherwise fail silently.
func TestChatWithoutBaseURLUsesOpenAIHost(t *testing.T) {
	c := &openaiClient{cfg: Config{APIKey: "sk-x", Model: "gpt-4o", Timeout: time.Second}}
	ep := c.endpointFor("sk-x", "")
	if !strings.HasPrefix(ep.url, "https://api.openai.com/") {
		t.Fatalf("url = %q, want an absolute api.openai.com URL", ep.url)
	}
}
