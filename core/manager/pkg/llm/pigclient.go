// pigclient.go is the PiG-backed implementation of the llm.Client interface.
//
// The plan's instruction is precise: keep the `llm.Client` interface, replace
// its implementation. That matters because `Client` is the seam fifty-odd
// call sites already speak — the ReAct graph, the detected/investigated worker
// loops, the RCA judge, chat_to_query, alertdraft — and all of them change
// behaviour the moment the model call underneath them changes. Swapping the
// implementation under the frozen interface is what lets the eino removal land
// as one commit instead of fifty.
//
// When to use this instead of the HTTP client in wire.go:
//
//   - This path adds PiG's provider compatibility table (which endpoints want
//     max_tokens vs max_completion_tokens, which need a non-standard auth
//     path), its retry/backoff, and its SSE streaming machinery.
//   - It also CONVERTS the request into PiG's transcript shape and back.
//     That conversion is the whole risk: fields the operations tools depend on
//     (tool_call ids, tool-result correlation, per-request credentials) ride
//     through it, and a silent drop shows up as a model that stops calling
//     tools rather than as an error.
//
// Because a wrong conversion fails quietly, this file carries its own test
// suite, and the package's behavioural tests (client_test.go) decide which
// implementation the default constructor returns.
package llm

import (
	"context"
	"fmt"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// PigClientConfig wires a Client onto OpsKeeper's model registry.
//
// Registry and Settings are separate on purpose. The registry answers "which
// provider serves this model and what are its coordinates"; the settings
// source answers "what is the API key right now". Splitting them keeps a key
// rotation observable without rebuilding a provider, which is the same
// lifetime contract the HTTP path honours through its own resolver cache.
type PigClientConfig struct {
	Registry *pigmodel.Registry
	Settings pigmodel.SettingsSource
	// Budget gates requests before the network call; nil means no limit.
	Budget BudgetChecker
}

// NewPigClient returns a Client that reaches providers through PiG.
//
// It fails closed on a missing registry: without one there is no provider to
// resolve, and a client that answered anyway would have to invent a model.
func NewPigClient(cfg PigClientConfig) (Client, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("llm: pig client requires a model registry")
	}
	return &pigClient{cfg: cfg}, nil
}

type pigClient struct {
	cfg PigClientConfig
}

// Chat implements Client.
//
// The gate order matches the HTTP client exactly — budget before the network
// call, usage recorded only on success — because the two must be
// interchangeable from the caller's point of view: a deployment that switched
// implementations must not also get a different billing trail.
func (c *pigClient) Chat(ctx context.Context, req ChatReq) (*ChatResp, error) {
	selection, err := selectionFor(req, c.cfg.Settings)
	if err != nil {
		return nil, err
	}

	if c.cfg.Budget != nil {
		if err := c.cfg.Budget.Check(ctx, req.UserID, estimatePromptTokens(req.Messages)); err != nil {
			return nil, err
		}
	}

	// Everything PiG-shaped happens behind this one call. The request goes
	// in as ports vocabulary and the reply comes back the same way, so no
	// PiG type is named anywhere in this file — which is what makes a PiG
	// upgrade a change to core/pig rather than a rebuild of every node and
	// manager binary.
	resp, err := c.cfg.Registry.Complete(ctx, toPortRequest(req, selection))
	if err != nil {
		return nil, err
	}
	assistant, usage := fromPortResponse(resp)

	if c.cfg.Budget != nil {
		// A recording failure must not fail the user's request: the answer
		// was produced and paid for, and discarding it would bill for a
		// response nobody receives.
		_ = c.cfg.Budget.Record(ctx, req.UserID, usage)
	}

	return &ChatResp{Assistant: assistant, Usage: usage}, nil
}

// selectionFor turns a ChatReq into a PiG model selection.
//
// A model named without a provider is a REQUEST to the registry, not an
// error: the registry scans the configured providers for one that offers the
// slug, which is what makes the SPA's model picker work when the operator
// never pinned a provider.
func selectionFor(req ChatReq, settings pigmodel.SettingsSource) (domain.ModelSelection, error) {
	sel := domain.ModelSelection{
		Provider: domain.ProviderID(req.Provider),
		Model:    req.Model,
	}
	if settings == nil {
		if sel.Provider == "" && sel.Model == "" {
			return sel, fmt.Errorf("llm: no provider or model named and no settings source to fall back on")
		}
		// A provider with no model still resolves: the registry uses that
		// provider's own default, which is what the operator configured.
		return sel, nil
	}
	if sel.Provider == "" && sel.Model == "" {
		def, ok := settings.DefaultProvider(context.Background())
		if !ok {
			return sel, fmt.Errorf("llm: no provider configured")
		}
		sel.Provider = def
	}
	return sel, nil
}

// toPortRequest maps the wire request onto the port the pig module speaks.
//
// The mapping is where this package stops knowing about PiG. Everything
// downstream — transcript construction, provider dispatch, the response
// conversion — happens inside core/pig, which is the only module allowed to
// hold a PiG type. A caller that wanted to build the transcript itself would
// have to import PiG to name the result, which is the coupling this mapping
// exists to remove.
func toPortRequest(req ChatReq, sel domain.ModelSelection) ports.LLMRequest {
	return ports.LLMRequest{
		Selection: sel,
		Messages:  toPortMessages(req.Messages),
		Tools:     toPortTools(req.Tools),
	}
}

func toPortMessages(in []Message) ports.Conversation {
	if len(in) == 0 {
		return nil
	}
	out := make(ports.Conversation, len(in))
	for i, m := range in {
		out[i] = ports.LLMMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			ToolCalls:  toPortToolCalls(m.ToolCalls),
		}
	}
	return out
}

func toPortToolCalls(in []ToolCall) []ports.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ports.ToolCall, len(in))
	for i, c := range in {
		out[i] = ports.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Args}
	}
	return out
}

// toPortTools keeps only the three fields a model is shown. The port's
// governance fields — Class, Origin, WhenToUse — are host-side policy
// metadata, and sending them to a provider would leak the node's
// classification of a tool to a third party for no benefit.
func toPortTools(in []ToolSchema) []ports.ToolSchema {
	if len(in) == 0 {
		return nil
	}
	out := make([]ports.ToolSchema, len(in))
	for i, t := range in {
		out[i] = ports.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
	}
	return out
}

// fromPortResponse maps the port reply back onto the shape this package's
// callers already consume.
//
// The total is taken from the port's own accessor rather than summed here:
// the port reports a provider-stated total when there is one, and that number
// is the one the budget ledger bills against.
func fromPortResponse(resp *ports.LLMResponse) (Message, Usage) {
	msg := Message{Role: "assistant", Content: resp.Content, ToolCalls: fromPortToolCalls(resp.ToolCalls)}
	usage := Usage{
		PromptTokens:     resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheWriteTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.Total(),
	}
	return msg, usage
}

func fromPortToolCalls(in []ports.ToolCall) []ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolCall, len(in))
	for i, c := range in {
		out[i] = ToolCall{ID: c.ID, Name: c.Name, Args: c.Arguments}
	}
	return out
}
