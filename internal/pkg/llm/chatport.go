// Chat port adaptation.
//
// This file is the seam between two vocabularies for the same call:
//
//   - Client, which follows OpenAI's wire shape because that is what the
//     providers speak (see the package doc and wire.go for why there is no
//     provider abstraction);
//   - ports.Completer, which is the contract the other OpsKeeper modules
//     are allowed to depend on.
//
// It exists so the evaluation harness can be a module that depends on
// core and nothing else. Without it, the judge would have to import this
// package, and core/harness would resolve the whole OpenAI/PiG/registry
// graph to score a rubric — a dependency a rubric should not have.
//
// The mapping is deliberately one-way and total. Every ports field either
// has a faithful representation here or the call fails; nothing is
// dropped on the floor and reported as success.
package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// ErrUnsupportedRequest reports a ports request this client cannot honour.
//
// It is returned rather than ignored because every field in it is a
// promise the port makes to its caller. A caller that set MaxOutputTokens
// asked for a bound; answering with an unbounded completion that reports
// success is a lie about the cost of the call, and the caller has no way to
// detect it.
var ErrUnsupportedRequest = errors.New("llm: request field not supported by this client")

// Completer adapts a Client to the port other modules depend on.
//
// A nil Client yields a completer that fails every call with ErrNoAPIKey
// rather than a nil interface: a caller that stored the result and used it
// later must get an error it can classify, not a panic.
func Completer(c Client) ports.Completer { return completer{c: c} }

type completer struct{ c Client }

// Complete issues one completion.
func (a completer) Complete(ctx context.Context, req ports.LLMRequest) (*ports.LLMResponse, error) {
	if a.c == nil {
		return nil, ErrNoAPIKey
	}
	chatReq, err := toChatReq(req)
	if err != nil {
		return nil, err
	}
	out, err := a.c.Chat(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("llm: client returned a nil response")
	}
	return fromChatResp(out), nil
}

func toChatReq(req ports.LLMRequest) (ChatReq, error) {
	// Checked before anything else so a caller that set an unsupported
	// bound learns about it without also paying for a network round trip.
	if req.MaxOutputTokens != 0 {
		return ChatReq{}, ErrMaxOutputTokensUnsupported
	}
	return ChatReq{
		// The port names a provider with domain.ProviderID, which is the
		// same closed vocabulary the router switches on. A selection the
		// caller left empty stays empty, which is what makes the router
		// apply the cluster default rather than this adapter second-guessing
		// it.
		Provider: string(req.Selection.Provider),
		Model:    req.Selection.Model,
		Messages: toMessages(req.Messages),
		Tools:    toToolSchemas(req.Tools),
	}, nil
}

func fromChatResp(out *ChatResp) *ports.LLMResponse {
	return &ports.LLMResponse{
		Content:   out.Assistant.Content,
		ToolCalls: fromToolCalls(out.Assistant.ToolCalls),
		Usage:     fromUsage(out.Usage),
		// ChatReq/ChatResp carry no finish reason: the client reports
		// whether the turn ended or a tool was requested by the shape of
		// the message, not by a stop reason field. That is a real
		// limitation of this client, and it is reported as the one stop
		// reason this transport can actually distinguish rather than
		// invented per call site.
		StopReason: stopReasonFor(out.Assistant),
	}
}

// stopReasonFor derives the normalised stop reason from the reply shape.
//
// The distinction it draws is the one callers actually branch on: a turn
// that ended versus a turn that is waiting on tools. Anything finer is not
// knowable through this client.
func stopReasonFor(m Message) string {
	if len(m.ToolCalls) > 0 {
		return ports.StopToolUse
	}
	return ports.StopEndTurn
}

func toMessages(in ports.Conversation) []Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]Message, len(in))
	for i, m := range in {
		out[i] = Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  fromToolCallPort(m.ToolCalls),
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
		}
	}
	return out
}

func fromToolCallPort(in []ports.ToolCall) []ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolCall, len(in))
	for i, c := range in {
		out[i] = ToolCall{ID: c.ID, Name: c.Name, Args: c.Arguments}
	}
	return out
}

func fromToolCalls(in []ToolCall) []ports.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ports.ToolCall, len(in))
	for i, c := range in {
		out[i] = ports.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Args}
	}
	return out
}

// toToolSchemas keeps only the three fields a model is shown. The port's
// governance fields — Class, Origin, WhenToUse — are host-side policy
// metadata; sending them to a provider would be leaking the node's
// classification of a tool to a third party for no benefit, since no
// provider interprets them.
func toToolSchemas(in []ports.ToolSchema) []ToolSchema {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolSchema, len(in))
	for i, t := range in {
		out[i] = ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
	}
	return out
}

func fromUsage(u Usage) ports.Usage {
	return ports.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

// ErrMaxOutputTokensUnsupported is the specific reason a request carrying
// MaxOutputTokens is refused. It is a named error because the fix is a
// deployment decision, not a code change: this client omits max_tokens on
// purpose (wire.go — reasoning models reject it, and a locally-rejected
// request is indistinguishable from a provider 400 at the call site that
// learns from rejections).
var ErrMaxOutputTokensUnsupported = fmt.Errorf(
	"%w: max_output_tokens; this client never sends max_tokens because reasoning models reject it", ErrUnsupportedRequest)

var _ ports.Completer = completer{}
