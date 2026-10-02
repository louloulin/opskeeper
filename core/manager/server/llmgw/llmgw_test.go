package llmgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/go-chi/chi/v5"

	"github.com/vincent-wuhan/opskeeper/core/floor/tunnel"
	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// stubAuth is the tunnel's authenticator, reduced to a map.
type stubAuth struct {
	edges map[string]uint64
	seen  []string
}

func (s *stubAuth) Authenticate(_ context.Context, accessKey, secretKey string) (tunnel.Session, error) {
	s.seen = append(s.seen, accessKey)
	id, ok := s.edges[accessKey+":"+secretKey]
	if !ok {
		return tunnel.Session{}, errs.ErrUnauthorized
	}
	return tunnel.Session{EdgeID: id}, nil
}

// stubCompleter records what it was asked and returns a fixed reply.
type stubCompleter struct {
	got   pigmodel.Request
	reply *ai.AssistantMessage
	err   error
}

func (s *stubCompleter) Complete(_ context.Context, req pigmodel.Request) (*ai.AssistantMessage, error) {
	s.got = req
	return s.reply, s.err
}

func assistantWithToolCall() *ai.AssistantMessage {
	msg := ai.AssistantMessage{}
	msg.Content = append(msg.Content, ai.TextContent{Text: "let me look"})
	msg.Content = append(msg.Content, ai.ToolCall{
		ID:   "call_abc123",
		Name: "host_dmesg",
		Arguments: ai.JsonObject{
			"lines": json.Number("40"),
		},
	})
	return &msg
}

func newTestHandler(t *testing.T, auth EdgeAuthenticator, completer Completer) *Handler {
	t.Helper()
	handler, err := NewHandler(Options{
		Auth:         auth,
		Completer:    completer,
		DefaultModel: "opskeeper-default",
	})
	if err != nil {
		t.Fatalf("build the handler: %v", err)
	}
	return handler
}

func post(t *testing.T, handler *Handler, credential, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	handler.Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// A gateway with no credential check is a key dispenser with a URL, and the
// mistake is easy because the handler is otherwise complete. This is the one
// construction that must not succeed.
func TestTheGatewayRefusesToBeBuiltWithoutACredentialCheck(t *testing.T) {
	if _, err := NewHandler(Options{Completer: &stubCompleter{}}); err == nil {
		t.Error("a gateway with no authenticator was built; it would serve model calls to anyone " +
			"who found the URL, spending the operator's credentials")
	}
	if _, err := NewHandler(Options{Auth: &stubAuth{}}); err == nil {
		t.Error("a gateway with no completer was built")
	}
}

// The node's credential is its existing tunnel pair, verified by the same
// function the tunnel dial uses. Every failure has to collapse to one answer:
// a gateway that distinguishes "no such node" from "wrong secret" is an
// oracle for enumerating the fleet.
func TestEveryCredentialFailureIsOneUnanswerableRefusal(t *testing.T) {
	auth := &stubAuth{edges: map[string]uint64{"ak-1:sk-good": 42}}
	handler := newTestHandler(t, auth, &stubCompleter{reply: &ai.AssistantMessage{}})
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`

	cases := []struct {
		name       string
		credential string
	}{
		{"no header at all", ""},
		{"not a bearer token", "Basic YWtrLTE6c2stZ29vZA=="},
		{"missing the secret half", "ak-1"},
		{"empty secret", "ak-1:"},
		{"unknown node", "ak-unknown:sk-whatever"},
		{"wrong secret", "ak-1:sk-wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, handler, tc.credential, body)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401; the body is %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "authentication_error") {
				t.Errorf("the refusal is not in the shape a provider client parses: %s", rec.Body.String())
			}
		})
	}
}

// The tool-call id is the whole reason this translation is hand-written. An
// id that does not survive the round trip is not an error — it is a node whose
// model stops calling tools and starts answering in prose, which reads as a
// model problem and is a gateway problem.
func TestToolCallIdentitySurvivesTheRoundTripInBothDirections(t *testing.T) {
	completer := &stubCompleter{reply: assistantWithToolCall()}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)

	// In: an assistant turn that made a call, and the result answering it.
	body := `{"model":"m","messages":[
      {"role":"user","content":"why is the disk full"},
      {"role":"assistant","content":"","tool_calls":[
        {"id":"call_abc123","type":"function","function":{"name":"host_dmesg","arguments":"{\"lines\":40}"}}]},
      {"role":"tool","tool_call_id":"call_abc123","name":"host_dmesg","content":"no space left on device"}
    ]}`
	rec := post(t, handler, "ak:sk", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	// The transcript the model saw must still carry the id.
	if len(completer.got.Messages) != 3 {
		t.Fatalf("the transcript has %d messages, want 3", len(completer.got.Messages))
	}
	assistant, ok := completer.got.Messages[1].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("messages[1] is %T, want an assistant turn", completer.got.Messages[1])
	}
	calls := pigmodel.ReplyToolCalls(&assistant)
	if len(calls) != 1 || calls[0].ID != "call_abc123" {
		t.Fatalf("the transcript's tool call lost its id: %+v", calls)
	}
	if got := calls[0].Arguments["lines"]; got != json.Number("40") {
		t.Errorf("the tool call's arguments were re-encoded as %T(%v); a number that arrives as "+
			"a string makes a strict provider reject the whole turn", got, got)
	}
	result, ok := completer.got.Messages[2].(ai.ToolResultMessage)
	if !ok || result.ToolCallID != "call_abc123" {
		t.Fatalf("the tool result lost its call id: %+v", completer.got.Messages[2])
	}

	// Out: the reply's tool call must carry the same id onto the wire, and
	// the client branches on finish_reason to decide to execute it.
	var response chatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("the response is not a chat completion: %v", err)
	}
	if response.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason is %q; a client that reads \"stop\" here stops the loop and never "+
			"executes the call the model asked for", response.Choices[0].FinishReason)
	}
	out := response.Choices[0].Message.ToolCalls
	if len(out) != 1 || out[0].ID != "call_abc123" {
		t.Fatalf("the reply's tool call lost its id on the way out: %+v", out)
	}
	if out[0].Function.Name != "host_dmesg" {
		t.Errorf("the reply's tool call is named %q, want host_dmesg", out[0].Function.Name)
	}
	if !strings.Contains(out[0].Function.Arguments, "40") {
		t.Errorf("the reply's arguments are %q, which does not carry the value", out[0].Function.Arguments)
	}
}

// A tool result whose call was never made is refused here, where the message
// can name the index, rather than passed upstream to become a 400 that names
// nothing. This is the check that keeps an orphan out of a transcript.
func TestAToolResultWithNoMatchingCallIsRefusedAtTheEdge(t *testing.T) {
	completer := &stubCompleter{reply: &ai.AssistantMessage{}}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)

	body := `{"model":"m","messages":[
      {"role":"user","content":"hi"},
      {"role":"tool","tool_call_id":"call_never_made","name":"host_dmesg","content":"output"}
    ]}`
	rec := post(t, handler, "ak:sk", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "call_never_made") {
		t.Errorf("the refusal does not name the orphan: %s", rec.Body.String())
	}
	if completer.got.Messages != nil {
		t.Error("the request reached the model despite failing validation")
	}
}

// A node names a model and never a provider. If it could name a provider it
// could spend a credential the operator never put in this cluster, which is
// the one thing the gateway exists to prevent.
func TestANodeCannotChooseWhichProviderPays(t *testing.T) {
	completer := &stubCompleter{reply: &ai.AssistantMessage{}}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)

	body := `{"model":"some-model","messages":[{"role":"user","content":"hi"}]}`
	if rec := post(t, handler, "ak:sk", body); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if completer.got.Selection.Provider != "" {
		t.Errorf("the request carried provider %q; a node must not be able to choose which "+
			"provider account the manager spends", completer.got.Selection.Provider)
	}
	if completer.got.Selection.Model != "some-model" {
		t.Errorf("the requested model was not carried through: %q", completer.got.Selection.Model)
	}
	if completer.got.SessionID != "" {
		t.Errorf("the request carried a provider-visible cache key %q supplied by the node; "+
			"providers key their cache on it and it is echoed on the wire", completer.got.SessionID)
	}
}

// Tool declarations have to survive with their schemas intact: a tool the
// model is offered with an empty parameter object is a tool it will call
// wrongly, and the wrong call reaches a host.
func TestToolDeclarationsKeepTheirSchemas(t *testing.T) {
	completer := &stubCompleter{reply: &ai.AssistantMessage{}}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[
      {"type":"function","function":{"name":"host_dmesg","description":"read the ring buffer",
        "parameters":{"type":"object","properties":{"lines":{"type":"integer"}},"required":["lines"]}}},
      {"type":"function","function":{"name":"ping","description":"no arguments"}}
    ]}`
	if rec := post(t, handler, "ak:sk", body); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(completer.got.Tools) != 2 {
		t.Fatalf("the model was offered %d tools, want 2", len(completer.got.Tools))
	}
	if completer.got.Tools[0].Name != "host_dmesg" {
		t.Errorf("tool[0] is %q", completer.got.Tools[0].Name)
	}
	props, ok := completer.got.Tools[0].Parameters["properties"].(map[string]any)
	if !ok || props["lines"] == nil {
		t.Errorf("tool[0] lost its parameter schema: %+v", completer.got.Tools[0].Parameters)
	}
	// A tool with no schema is told it takes no arguments, rather than being
	// offered nil, which says nothing at all.
	if completer.got.Tools[1].Parameters["type"] != "object" {
		t.Errorf("a no-argument tool was offered %+v; it must say it takes an object", completer.got.Tools[1].Parameters)
	}
}

// A streaming reply has to be a well-formed frame sequence ending in [DONE],
// and the tool calls have to arrive whole in the final frame — a client that
// is accumulating deltas has nowhere else to get an id from.
func TestAStreamingReplyIsAWellFormedFrameSequence(t *testing.T) {
	completer := &stubCompleter{reply: assistantWithToolCall()}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"why is the disk full"}]}`
	rec := post(t, handler, "ak:sk", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type is %q, want text/event-stream", ct)
	}

	raw := rec.Body.String()
	if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
		t.Errorf("the stream does not end with [DONE]; a client that waits for it hangs until "+
			"its own timeout:\n%q", raw)
	}

	var frames []chatChunk
	var sawRole, sawText bool
	for _, line := range strings.Split(raw, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("a frame is not a chat.completion.chunk: %v\n%s", err, payload)
		}
		frames = append(frames, chunk)
		for _, choice := range chunk.Choices {
			if choice.Delta == nil {
				continue
			}
			if choice.Delta.Content == "" && choice.Delta.Role == roleAssistant {
				sawRole = true
			}
			if choice.Delta.Content != "" {
				sawText = true
			}
			if choice.Delta.ToolCalls != nil {
				t.Errorf("a delta carried tool calls: %+v; tool calls are not streamed, they "+
					"arrive whole in the final frame", choice.Delta)
			}
		}
	}
	if !sawRole {
		t.Error("the stream has no role frame; a client that accumulates a message from deltas " +
			"has nowhere to start")
	}
	if !sawText {
		t.Error("the stream has no content frame; the model's text never reached the caller")
	}
	last := frames[len(frames)-1]
	if last.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("the final frame's finish_reason is %q, want tool_calls", last.Choices[0].FinishReason)
	}
	final := last.Choices[0].Message.ToolCalls
	if len(final) != 1 || final[0].ID != "call_abc123" {
		t.Errorf("the final frame's tool calls are %+v; a streaming client gets the id from "+
			"nowhere else", final)
	}
}

// A provider that reports no usage gets usage:null, not zeros. A client that
// sees zeros has been told a number; one that sees null knows it was told
// nothing, and only one of those is safe to bill against.
func TestAbsentProviderUsageIsReportedAsAbsent(t *testing.T) {
	auth := &stubAuth{edges: map[string]uint64{"ak:sk": 7}}
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`

	rec := post(t, newTestHandler(t, auth, &stubCompleter{reply: &ai.AssistantMessage{}}), "ak:sk", body)
	if !strings.Contains(rec.Body.String(), `"usage"`) && !strings.Contains(rec.Body.String(), `"choices"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	var response chatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Usage != nil {
		t.Errorf("usage is %+v; a provider that reported nothing must not be reported as zero", response.Usage)
	}
}

// An upstream failure inside a stream cannot become a 500 — the status line
// is already written. It is reported as an OpenAI error object in the stream,
// because an empty choices array would read as "the model said nothing",
// which is the one reading a caller cannot tell from a real empty reply.
func TestAnUpstreamFailureInsideAStreamIsAnErrorObject(t *testing.T) {
	completer := &stubCompleter{err: errors.New("provider is out of credit")}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := post(t, handler, "ak:sk", body)
	raw := rec.Body.String()
	if !strings.Contains(raw, `"error"`) || !strings.Contains(raw, "out of credit") {
		t.Errorf("the stream did not carry the failure as an error object:\n%s", raw)
	}
	if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
		t.Error("a failed stream must still be terminated by [DONE] so the client stops waiting")
	}
}

// A malformed request is refused before the model is touched, with an error
// in the shape a provider client parses.
func TestMalformedRequestsAreRefusedBeforeTheModelIsCalled(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"not json", `{`, "not chat completions"},
		{"no messages", `{"model":"m","messages":[]}`, "messages is empty"},
		{"unknown role", `{"model":"m","messages":[{"role":"wizard","content":"x"}]}`, "wizard"},
		{"tool result with no id", `{"model":"m","messages":[{"role":"tool","content":"x"}]}`, "tool_call_id"},
		{"a tool with no name", `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{}}]}`, "no name"},
		{"arguments that are not an object", `{"model":"m","messages":[
			{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"t","arguments":"[1,2]"}}]}]}`, "not a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completer := &stubCompleter{reply: &ai.AssistantMessage{}}
			handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)
			rec := post(t, handler, "ak:sk", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("the refusal does not mention %q: %s", tc.want, rec.Body.String())
			}
			if completer.got.Messages != nil {
				t.Error("the request reached the model despite failing validation")
			}
		})
	}
}

// An empty string of arguments is a call with no arguments, which models emit
// for a no-argument tool, and it decodes to an empty object rather than nil.
func TestEmptyToolArgumentsDecodeToAnEmptyObject(t *testing.T) {
	completer := &stubCompleter{reply: &ai.AssistantMessage{}}
	handler := newTestHandler(t, &stubAuth{edges: map[string]uint64{"ak:sk": 7}}, completer)
	body := `{"model":"m","messages":[
      {"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":""}}]}]}`
	if rec := post(t, handler, "ak:sk", body); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assistant := completer.got.Messages[0].(ai.AssistantMessage)
	calls := pigmodel.ReplyToolCalls(&assistant)
	if len(calls) != 1 {
		t.Fatalf("tool calls: %+v", calls)
	}
	if calls[0].Arguments == nil {
		t.Error("empty arguments decoded to nil; a provider validating \"arguments is required\" " +
			"is right to refuse nil, and \"this tool takes no arguments\" is a fact it needs told")
	}
}
