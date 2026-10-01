// This file tests the conversions in chat.go, which is the whole risk of this
// package: a field dropped in either direction does not fail a test in
// production. It produces a transcript that replays as an orphaned tool
// result, which a strict provider rejects with an HTTP 400 on a LATER turn —
// the failure appears far from its cause and in a different request.
package pigmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// opsRequest is the transcript shape operations actually produces: a system
// prompt, the user's question, an assistant turn that asked for a tool, and
// the tool's answer. Every one of those four is load-bearing, and the
// conversion is the only place any of them can be lost.
func opsRequest() ports.LLMRequest {
	return ports.LLMRequest{
		Selection: domain.ModelSelection{Provider: domain.ProviderCustom, Model: "ops-test"},
		Messages: ports.Conversation{
			{Role: "system", Content: "you are an SRE"},
			{Role: "user", Content: "why is node-01 slow?"},
			{Role: "assistant", Content: "checking", ToolCalls: []ports.ToolCall{
				{ID: "call_1", Name: "get_host_load", Arguments: json.RawMessage(`{"host":"node-01"}`)},
			}},
			{Role: "tool", ToolCallID: "call_1", ToolName: "get_host_load", Content: `{"load":9.1}`},
		},
		Tools: []ports.ToolSchema{{
			Name:        "get_host_load",
			Description: "reads load average",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host":{"type":"string"}}}`),
		}},
	}
}

// The transcript must carry all four turns with their ids and arguments
// intact. PiG's Message union carries its role on an unexported method, so the
// assertion is on the concrete type: a system turn that arrived as a user
// message would still be "some text" to a weaker check.
func TestBuildTranscriptRoundTripsTheOperationsShapes(t *testing.T) {
	t.Parallel()
	tc, err := BuildTranscript(opsRequest())
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// NormalizeContext prepends a tools-carrying system message, so the
	// expected order is: [tools header][system prompt][user][assistant][tool].
	msgs := tc.Messages()
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want 5 (a tools header plus the four requested turns)", len(msgs))
	}
	header, ok := msgs[0].(ai.SystemMessage)
	if !ok {
		t.Fatalf("message[0] = %T, want ai.SystemMessage carrying the tool declarations", msgs[0])
	}
	if len(header.ToolsAdded) != 1 || header.ToolsAdded[0].Name != "get_host_load" {
		t.Errorf("tools header = %+v, want get_host_load so the model can call it", header.ToolsAdded)
	}
	if _, ok := msgs[1].(ai.SystemMessage); !ok {
		t.Errorf("message[1] = %T, want the caller's system prompt as a system message", msgs[1])
	}
	asst, ok := msgs[3].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("message[3] = %T, want ai.AssistantMessage", msgs[3])
	}
	var call *ai.ToolCall
	for _, block := range asst.Content {
		if tc, ok := block.(ai.ToolCall); ok {
			c := tc
			call = &c
		}
	}
	if call == nil {
		t.Fatalf("assistant tool call was dropped: %#v", asst.Content)
	}
	if call.ID != "call_1" || call.Name != "get_host_load" {
		t.Errorf("tool call = %+v, want call_1/get_host_load", call)
	}
	if call.Arguments["host"] != "node-01" {
		t.Errorf("tool call arguments = %v, want host=node-01", call.Arguments)
	}
	tr, ok := msgs[4].(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("message[4] = %T, want ai.ToolResultMessage", msgs[4])
	}
	if tr.ToolCallID != "call_1" {
		t.Errorf("tool result call id = %q, want call_1 (provider correlation depends on it)", tr.ToolCallID)
	}
}

// PiG would reject an orphaned tool result too, but with an error naming no
// message index, leaving the operator to bisect the conversation.
func TestBuildTranscriptRejectsAnOrphanedToolResult(t *testing.T) {
	t.Parallel()
	_, err := BuildTranscript(ports.LLMRequest{Messages: ports.Conversation{
		{Role: "tool", ToolName: "get_host_load", Content: "{}"},
	}})
	if err == nil {
		t.Fatal("expected an error for a tool result with no call id")
	}
	if !strings.Contains(err.Error(), "get_host_load") {
		t.Errorf("err = %q, want it to name the offending tool", err)
	}
}

// A role this build cannot express is refused rather than silently mapped to
// something else. Guessing would put words in the conversation that no
// participant said.
func TestBuildTranscriptRejectsAnUnknownRole(t *testing.T) {
	t.Parallel()
	_, err := BuildTranscript(ports.LLMRequest{Messages: ports.Conversation{
		{Role: "narrator", Content: "hi"},
	}})
	if err == nil {
		t.Fatal("expected an error for an unknown role")
	}
	if !strings.Contains(err.Error(), "narrator") {
		t.Errorf("err = %q, want it to name the offending role", err)
	}
}

// An argument blob that is not a JSON object fails at build time. Naming the
// tool here is the difference between a one-line fix and a bisect.
func TestBuildTranscriptRejectsMalformedToolArguments(t *testing.T) {
	t.Parallel()
	_, err := BuildTranscript(ports.LLMRequest{Messages: ports.Conversation{
		{Role: "assistant", ToolCalls: []ports.ToolCall{
			{ID: "c", Name: "query_promql", Arguments: json.RawMessage(`["not","an","object"]`)},
		}},
	}})
	if err == nil {
		t.Fatal("expected an error for arguments that are not a JSON object")
	}
	if !strings.Contains(err.Error(), "query_promql") {
		t.Errorf("err = %q, want it to name the offending tool", err)
	}
}

// The same fail-fast for tool schemas: a caller-supplied schema that is not
// JSON must fail here, not at the provider.
func TestBuildTranscriptRejectsMalformedToolSchema(t *testing.T) {
	t.Parallel()
	_, err := BuildTranscript(ports.LLMRequest{
		Messages: ports.Conversation{{Role: "user", Content: "hi"}},
		Tools:    []ports.ToolSchema{{Name: "bad", Parameters: json.RawMessage(`{not json`)}},
	})
	if err == nil {
		t.Fatal("expected an error for a malformed tool schema")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("err = %q, want it to name the offending tool", err)
	}
}

// The error names WHICH message failed. A refusal that only says "bad
// transcript" is the failure mode this whole function exists to prevent.
func TestBuildTranscriptNamesTheOffendingMessageIndex(t *testing.T) {
	t.Parallel()
	_, err := BuildTranscript(ports.LLMRequest{Messages: ports.Conversation{
		{Role: "user", Content: "fine"},
		{Role: "user", Content: "fine"},
		{Role: "narrator", Content: "not a role"},
	}})
	if err == nil {
		t.Fatal("expected an error for the third message")
	}
	if !strings.Contains(err.Error(), "message[2]") {
		t.Errorf("err = %q, want it to name message[2]", err)
	}
}

// A parameterless call reaches PiG as `{}`. PiG validates the field is an
// object, and "no arguments" is a legal object while absent/null is not.
func TestDecodeToolArgumentsTreatsAbsentAsEmptyObject(t *testing.T) {
	t.Parallel()
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage("null")} {
		got, err := decodeToolArguments(raw)
		if err != nil {
			t.Fatalf("decodeToolArguments(%q): %v", raw, err)
		}
		b, _ := json.Marshal(got)
		if string(b) != "{}" {
			t.Fatalf("decodeToolArguments(%q) = %s, want {}", raw, b)
		}
	}
}

// A parameterless tool is declared as an object with no properties rather
// than with no schema at all: a provider given an empty schema rejects the
// tool, so "no parameters" has to be spelled out.
func TestDecodeSchemaGivesAParameterlessToolAnObjectSchema(t *testing.T) {
	t.Parallel()
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null")} {
		got, err := decodeSchema(raw)
		if err != nil {
			t.Fatalf("decodeSchema(%q): %v", raw, err)
		}
		if got["type"] != "object" {
			t.Errorf("decodeSchema(%q)[type] = %v, want object", raw, got["type"])
		}
		if _, ok := got["properties"]; !ok {
			t.Errorf("decodeSchema(%q) has no properties key: %v", raw, got)
		}
	}
}

// The response mapping. Callers read Content as one string and ToolCalls as a
// slice; a multi-block assistant turn must flatten to the same shape the HTTP
// client produces, or downstream code would branch on which implementation
// ran.
func TestAssistantFromPiGFlattensTextAndKeepsToolCalls(t *testing.T) {
	t.Parallel()
	resp, err := assistantFromPiG(&ai.AssistantMessage{Content: []ai.AssistantContentBlock{
		ai.TextContent{Text: "checking "},
		ai.TextContent{Text: "now"},
		ai.ToolCall{ID: "call_7", Name: "query_promql", Arguments: ai.JsonObject{"q": "up"}},
	}})
	if err != nil {
		t.Fatalf("assistantFromPiG: %v", err)
	}
	if resp.Content != "checking now" {
		t.Errorf("content = %q, want the text blocks concatenated", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_7" || resp.ToolCalls[0].Name != "query_promql" {
		t.Errorf("tool call = %+v", resp.ToolCalls[0])
	}
	var args map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Arguments, &args); err != nil {
		t.Fatalf("tool call args are not valid JSON: %v (%q)", err, resp.ToolCalls[0].Arguments)
	}
	if args["q"] != "up" {
		t.Errorf("tool call args = %v, want q=up", args)
	}
}

// Thinking and image blocks are dropped rather than guessed at. Synthesising
// a mapping would put text into the transcript the model never said, which
// the tool executor would then read as a tool call or a message.
func TestAssistantFromPiGDropsNonTextBlocks(t *testing.T) {
	t.Parallel()
	resp, err := assistantFromPiG(&ai.AssistantMessage{Content: []ai.AssistantContentBlock{
		ai.ThinkingContent{Thinking: "secret reasoning"},
		ai.TextContent{Text: "visible"},
	}})
	if err != nil {
		t.Fatalf("assistantFromPiG: %v", err)
	}
	if resp.Content != "visible" {
		t.Fatalf("content = %q, want only the text block", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("tool calls = %d, want 0", len(resp.ToolCalls))
	}
}

// The stop reason is what callers branch on to decide whether to run tools or
// end the turn. A reply that carried a tool call but reported end_turn would
// leave the operator watching a turn that silently did nothing.
func TestAssistantFromPiGReportsTheStopReasonTheShapeImplies(t *testing.T) {
	t.Parallel()
	withTool, err := assistantFromPiG(&ai.AssistantMessage{Content: []ai.AssistantContentBlock{
		ai.TextContent{Text: "one moment"},
		ai.ToolCall{ID: "c", Name: "get_host_load", Arguments: ai.JsonObject{}},
	}})
	if err != nil {
		t.Fatalf("assistantFromPiG: %v", err)
	}
	if withTool.StopReason != ports.StopToolUse {
		t.Errorf("stop reason = %q, want %q when the model asked for a tool",
			withTool.StopReason, ports.StopToolUse)
	}

	textOnly, err := assistantFromPiG(&ai.AssistantMessage{Content: []ai.AssistantContentBlock{
		ai.TextContent{Text: "all clear"},
	}})
	if err != nil {
		t.Fatalf("assistantFromPiG: %v", err)
	}
	if textOnly.StopReason != ports.StopEndTurn {
		t.Errorf("stop reason = %q, want %q for a finished turn",
			textOnly.StopReason, ports.StopEndTurn)
	}
}

// A parameterless call comes back from PiG as a nil JsonObject, which
// marshals to the four bytes "null". The host stores this blob and replays it
// verbatim on the next turn, and a stored null argument set is a request PiG
// refuses — so the conversion has to normalise it here, where the reason is
// known, rather than three turns later where it is not.
func TestAParameterlessToolCallDoesNotBecomeStoredNull(t *testing.T) {
	t.Parallel()
	resp, err := assistantFromPiG(&ai.AssistantMessage{Content: []ai.AssistantContentBlock{
		ai.ToolCall{ID: "c", Name: "get_host_load"},
	}})
	if err != nil {
		t.Fatalf("assistantFromPiG: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if got := string(resp.ToolCalls[0].Arguments); got != "{}" {
		t.Errorf("arguments = %q, want {} rather than null", got)
	}
}

// A real argument set must survive the round trip byte-for-byte in meaning:
// the host replays this blob, so a re-encoded object that changed shape would
// change what the tool receives.
func TestToolCallArgumentsSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	resp, err := assistantFromPiG(&ai.AssistantMessage{Content: []ai.AssistantContentBlock{
		ai.ToolCall{ID: "c", Name: "query_promql", Arguments: ai.JsonObject{
			"q":    "up",
			"step": float64(30),
		}},
	}})
	if err != nil {
		t.Fatalf("assistantFromPiG: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Arguments, &got); err != nil {
		t.Fatalf("arguments are not valid JSON: %v", err)
	}
	if got["q"] != "up" {
		t.Errorf("q = %v, want up", got["q"])
	}
	if got["step"] != float64(30) {
		t.Errorf("step = %v (%T), want the number 30", got["step"], got["step"])
	}
}

// The accounting. PiG reports input, output, and cache separately and carries
// a provider total that need not equal their sum. The budget ledger bills
// against the total, so recomputing it would drift from the invoice wherever
// cache or reasoning tokens are counted separately.
func TestUsageFromPiGCarriesTheProviderTotalSeparately(t *testing.T) {
	t.Parallel()
	got := usageFromPiG(&ai.AssistantMessage{Usage: ai.Usage{
		Input:       11,
		Output:      4,
		CacheRead:   100,
		TotalTokens: 115,
	}})
	if got.InputTokens != 11 || got.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want input 11 / output 4", got)
	}
	if got.CacheReadTokens != 100 {
		t.Errorf("cache read = %d, want 100 kept separate rather than folded into input", got.CacheReadTokens)
	}
	if got.ReportedTotal != 115 {
		t.Errorf("reported total = %d, want the provider's 115", got.ReportedTotal)
	}
	// The number the budget actually bills is the provider's.
	if got.Total() != 115 {
		t.Errorf("Total() = %d, want 115 rather than the sum 15", got.Total())
	}
}

// The other half: a provider that streams no total still produces a usable
// budget number rather than a zero that would read as "free".
func TestUsageFromPiGFallsBackToTheSumWhenNoTotalIsReported(t *testing.T) {
	t.Parallel()
	got := usageFromPiG(&ai.AssistantMessage{Usage: ai.Usage{Input: 11, Output: 4}})
	if got.ReportedTotal != 0 {
		t.Errorf("reported total = %d, want 0 so the sum is used", got.ReportedTotal)
	}
	if got.Total() != 15 {
		t.Errorf("Total() = %d, want 15 when the provider reports no total", got.Total())
	}
}

// A message that streamed no usage at all is an honest zero, not a
// fabrication. A caller that budgets on this must be able to tell "the
// provider told us nothing" from "the request was free" — and the honest
// answer is that the port reports what it was given.
func TestUsageFromPiGIsZeroWhenTheProviderReportedNothing(t *testing.T) {
	t.Parallel()
	if got := usageFromPiG(&ai.AssistantMessage{}); got.Total() != 0 {
		t.Errorf("usage = %+v, want an honest zero", got)
	}
}
