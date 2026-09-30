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
	// CostUSD is the request's computed cost. The host, not the provider,
	// owns the price table, so this is advisory.
	CostUSD float64
}

// Total returns the number of tokens the request consumed, counting cache
// reads and writes as input.
func (u Usage) Total() int {
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

// Chat is the synchronous, non-streaming model call. It exists as a
// separate port from the streaming agent loop because the background
// investigator, the evaluation judge, and the query translator all want one
// plain completion and must not have to run a full agent turn to get it.
type Chat interface {
	// Complete performs one completion.
	Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error)
	// Available reports whether a provider is configured for ref. It is how
	// a caller distinguishes "not configured" from "configured and
	// failing": a false result must not be retried.
	Available(ctx context.Context, ref domain.ModelRef) bool
	// Resolve pins an explicit selection to a concrete model, applying the
	// cluster default when the selection is partial. It is the single
	// place the default-provider fallback is applied.
	Resolve(ctx context.Context, sel domain.ModelSelection) (domain.ModelRef, error)
}
