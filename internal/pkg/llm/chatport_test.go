package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// capturing records the request the client was handed, so a test can assert
// on what would have gone to the provider rather than on the adapter's
// intermediate values.
type capturing struct {
	req  ChatReq
	resp *ChatResp
	err  error
}

func (c *capturing) Chat(_ context.Context, req ChatReq) (*ChatResp, error) {
	c.req = req
	return c.resp, c.err
}

func TestCompleterSpeaksThePortsVocabulary(t *testing.T) {
	inner := &capturing{resp: &ChatResp{
		Assistant: Message{
			Role:    "assistant",
			Content: "the pool is exhausted",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "get_host_load", Args: json.RawMessage(`{"host":"api-01"}`)},
			},
		},
		Usage: Usage{PromptTokens: 120, CompletionTokens: 34, TotalTokens: 154},
	}}

	resp, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{
		Selection: domain.ModelSelection{Provider: domain.ProviderAnthropic, Model: "claude-opus-5"},
		Messages: ports.Conversation{
			{Role: "system", Content: "you are a judge"},
			{Role: "user", Content: "score this"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Content != "the pool is exhausted" {
		t.Errorf("content = %q, want the assistant text", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "get_host_load" {
		t.Errorf("tool calls = %+v, want the one the assistant asked for", resp.ToolCalls)
	}
	if string(resp.ToolCalls[0].Arguments) != `{"host":"api-01"}` {
		t.Errorf("arguments = %s, want the raw provider JSON", resp.ToolCalls[0].Arguments)
	}
	// A turn that ends by asking for tools is not a turn that ended. The
	// difference is what tells a caller whether to run the tools or to read
	// the text, so it cannot be flattened to one value.
	if resp.StopReason != ports.StopToolUse {
		t.Errorf("stop reason = %q, want %q", resp.StopReason, ports.StopToolUse)
	}
	if resp.Usage.InputTokens != 120 || resp.Usage.OutputTokens != 34 {
		t.Errorf("usage = %+v, want the provider's counts", resp.Usage)
	}

	// The provider names, not the port names.
	if inner.req.Provider != string(domain.ProviderAnthropic) || inner.req.Model != "claude-opus-5" {
		t.Errorf("client got %s/%s, want the requested anthropic/claude-opus-5",
			inner.req.Provider, inner.req.Model)
	}
	if len(inner.req.Messages) != 2 || inner.req.Messages[0].Role != "system" {
		t.Errorf("client got messages %+v, want the transcript in order", inner.req.Messages)
	}
}

func TestATranscriptCarriesTheAssistantToolCallsBackToTheProvider(t *testing.T) {
	// A follow-up turn replays the assistant's tool requests alongside the
	// tool results. Dropping them leaves the provider with an orphan
	// result, so the round trip has to preserve them.
	inner := &capturing{resp: &ChatResp{Assistant: Message{Role: "assistant", Content: "done"}}}

	_, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{
		Messages: ports.Conversation{
			{Role: "user", Content: "why is pg slow"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ports.ToolCall{
					{ID: "call_1", Name: "query_promql", Arguments: json.RawMessage(`{"expr":"up"}`)},
				},
			},
			{Role: "tool", ToolCallID: "call_1", ToolName: "query_promql", Content: `{"value":1}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(inner.req.Messages) != 3 {
		t.Fatalf("client got %d messages, want 3", len(inner.req.Messages))
	}
	assistant := inner.req.Messages[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("the assistant turn lost its tool calls: %+v", assistant)
	}
	if inner.req.Messages[2].ToolCallID != "call_1" {
		t.Errorf("the tool result lost its pairing id: %+v", inner.req.Messages[2])
	}
}

func TestGovernanceMetadataIsNotSentToTheProvider(t *testing.T) {
	// Class, Origin and WhenToUse describe the node's own policy posture.
	// No provider interprets them, and shipping them tells a third party
	// how this deployment grades its own tools.
	inner := &capturing{resp: &ChatResp{Assistant: Message{Role: "assistant"}}}

	_, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{
		Tools: []ports.ToolSchema{{
			Name:        "restart_service",
			Description: "restart a service",
			WhenToUse:   "when the service is wedged",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Class:       domain.ClassDestructive,
			Origin:      ports.OriginPlugin,
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(inner.req.Tools) != 1 {
		t.Fatalf("client got %d tools, want 1", len(inner.req.Tools))
	}
	sent, err := json.Marshal(inner.req.Tools[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"destructive", "plugin", "wedged"} {
		if strings.Contains(string(sent), leak) {
			t.Errorf("the tool schema leaked %q to the provider: %s", leak, sent)
		}
	}
	if inner.req.Tools[0].Name != "restart_service" {
		t.Errorf("the tool name was dropped: %+v", inner.req.Tools[0])
	}
}

func TestABoundThisClientCannotHonourIsRefusedNotIgnored(t *testing.T) {
	// The client never sends max_tokens: reasoning models reject it, and a
	// request rejected locally is indistinguishable from a provider 400 at
	// the call site that learns from rejections. So the port's bound cannot
	// be delivered — and the honest answer is to refuse the call, because a
	// silent unbounded completion reports success for a request that asked
	// to be capped.
	inner := &capturing{resp: &ChatResp{Assistant: Message{Role: "assistant"}}}

	_, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{
		Messages:        ports.Conversation{{Role: "user", Content: "hi"}},
		MaxOutputTokens: 256,
	})
	if !errors.Is(err, ErrUnsupportedRequest) {
		t.Fatalf("error = %v, want ErrUnsupportedRequest", err)
	}
	if !errors.Is(err, ErrMaxOutputTokensUnsupported) {
		t.Errorf("error = %v, want the specific max-tokens reason", err)
	}
	if inner.req.Messages != nil {
		t.Error("the request reached the client despite an unsupported field")
	}
}

func TestANilClientIsAnErrorRatherThanAPanic(t *testing.T) {
	_, err := Completer(nil).Complete(context.Background(), ports.LLMRequest{
		Messages: ports.Conversation{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("error = %v, want a classifiable ErrNoAPIKey", err)
	}
}

func TestANilResponseFromTheClientIsNotPassedOnAsSuccess(t *testing.T) {
	inner := &capturing{resp: nil}
	resp, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{
		Messages: ports.Conversation{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("a nil response came back as %+v, want an error", resp)
	}
}

func TestAnEmptyTranscriptIsNotSentAsAnEmptyNonNilSlice(t *testing.T) {
	// A provider distinguishes "no messages" from "one empty message", and
	// only the first is what a caller with nothing to say means.
	inner := &capturing{resp: &ChatResp{Assistant: Message{Role: "assistant"}}}
	if _, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if inner.req.Messages != nil {
		t.Errorf("messages = %+v, want nil", inner.req.Messages)
	}
}

func TestATurnWithoutToolCallsReportsEndOfTurn(t *testing.T) {
	inner := &capturing{resp: &ChatResp{Assistant: Message{Role: "assistant", Content: "all clear"}}}
	resp, err := Completer(inner).Complete(context.Background(), ports.LLMRequest{
		Messages: ports.Conversation{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != ports.StopEndTurn {
		t.Errorf("stop reason = %q, want %q", resp.StopReason, ports.StopEndTurn)
	}
}
