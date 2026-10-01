// Package ports declares the interfaces OpsKeeper modules depend on. Each
// port is implemented outside core: the provider and agent-loop ports are
// backed by PiG in the pig module, the audit and approval ports by the
// control plane.
//
// Why ports rather than the concrete PiG types: a plugin compiled against
// core must not have to re-resolve PiG's module graph, and must not break
// when PiG moves. Everything PiG-shaped stops here.
package ports

import (
	"context"

	"github.com/vincent-wuhan/opskeeper/core/domain"
)

// Usage is a token accounting record for one provider request.
//
// Red line: telemetry labels MUST NOT contain user, tenant, or session
// identifiers. Only the model, the operation kind, and the result may be
// used as label values.
type Usage struct {
	InputTokens  int
	OutputTokens int
	// CacheReadTokens and CacheWriteTokens are reported separately from
	// InputTokens because providers bill them at different rates.
	CacheReadTokens  int
	CacheWriteTokens int
	// ReportedTotal is the total the provider itself reported, when it
	// reported one. Zero means "the provider was silent".
	//
	// It exists because the sum above is not always the number that was
	// billed. Reasoning models bill reasoning tokens, and several
	// providers fold those into the total without naming them in either
	// input or output, so Input+Output under-reports. Recomputing the
	// provider's own number would quietly change the bill, which is the
	// one number in this struct a caller must not second-guess.
	ReportedTotal int
	// CostUSD is the request's computed cost. The host, not the provider,
	// owns the price table, so this is advisory.
	CostUSD float64
}

// Total returns the number of tokens the request consumed, counting cache
// reads and writes as input.
//
// A provider-reported total wins over the sum. The sum is the fallback for
// the providers that report no total at all, and it is a lower bound rather
// than an estimate when the provider was silent about reasoning tokens.
func (u Usage) Total() int {
	if u.ReportedTotal != 0 {
		return u.ReportedTotal
	}
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// LLMMessage is one entry in a conversation transcript.
//
// Role is the literal wire role: "system", "user", "assistant", "tool".
// ToolCallID is set on tool results so a provider that requires the pairing
// can be satisfied without re-deriving it.
type LLMMessage struct {
	Role       string
	Content    string
	ToolCallID string
	// ToolName is set on assistant tool-call entries and on the matching
	// tool result.
	ToolName string
	// ToolCalls is set on the assistant entry that requested tools. It is
	// the field that makes a transcript replayable: a follow-up turn has
	// to carry the assistant's tool requests back to the provider, or the
	// provider sees an orphan tool result and either rejects the request
	// or, worse, silently attributes it to the wrong call. An adapter
	// that cannot represent this cannot be lossless, which is why it
	// lives here rather than being reconstructed from LLMResponse.
	ToolCalls []ToolCall
}

// Conversation is an ordered transcript handed to a model in one call.
type Conversation []LLMMessage

// LLMRequest is one model call. Every field is resolved by the caller
// before the port is invoked, so an implementation never has to consult
// settings storage itself.
type LLMRequest struct {
	Selection domain.ModelSelection
	Messages  Conversation
	Tools     []ToolSchema
	// MaxOutputTokens bounds the reply. Zero leaves the provider default.
	MaxOutputTokens int
	// Stream requests incremental delivery through the EventSink on the
	// request's context. A non-streaming implementation may ignore it and
	// emit exactly one terminal event.
	Stream bool
	// SessionID enables provider-side prompt caching when the provider
	// supports it. It is an opaque cache key and must not be a user
	// identifier.
	SessionID string
}

// LLMResponse is the completed reply.
type LLMResponse struct {
	Content string
	// ToolCalls are the model's requests to invoke tools.
	ToolCalls []ToolCall
	Usage     Usage
	// StopReason is provider-specific: "end_turn", "tool_use",
	// "max_tokens". Callers branch on it, so implementations must
	// normalise it to these three values.
	StopReason string
}

// Stop reasons, normalised across providers.
const (
	StopEndTurn  = "end_turn"
	StopToolUse  = "tool_use"
	StopMaxToken = "max_tokens"
)

// Completer is one plain completion: the smallest useful way to talk to a
// model.
//
// It is separate from Chat because the two answer different questions.
// Completer is "say this and give me the text". Chat adds "is anything
// configured" and "which model would this actually use" — provider
// discovery, which a caller that was handed a client does not have any use
// for. Folding the discovery methods into the completion port is why the
// port had no implementation: everything that wanted a completion was also
// being asked to implement configuration lookup, and nothing that knows
// the configuration wants to be handed a judging rubric.
//
// The evaluation judge is the motivating caller: it wants one completion
// per score, with a fallback of its own, and it must not grow a dependency
// on how providers are discovered in order to be testable.
type Completer interface {
	// Complete performs one completion.
	Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

// Chat is the full synchronous model port: a completer plus the discovery
// a caller needs before it can decide whether to call at all.
type Chat interface {
	Completer
	// Available reports whether a provider is configured for ref. It is how
	// a caller distinguishes "not configured" from "configured and
	// failing": a false result must not be retried.
	Available(ctx context.Context, ref domain.ModelRef) bool
	// Resolve pins an explicit selection to a concrete model, applying the
	// cluster default when the selection is partial. It is the single
	// place the default-provider fallback is applied.
	Resolve(ctx context.Context, sel domain.ModelSelection) (domain.ModelRef, error)
}
