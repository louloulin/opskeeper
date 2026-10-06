package llmgw

import (
	"context"
	"fmt"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/base/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigai"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// CallBounds are the two ceilings that belong to one call rather than to a
// window: how long the provider may take, and how many output tokens it may
// produce.
//
// They live on the manager's side for the same reason the budget and the rate
// gate do. A node's agent is a loop, and nothing in PiG bounds how many turns
// it takes or how long one reply runs, so without these a single call can pin
// a provider connection, a goroutine and a node's investigation open
// indefinitely — and the ceiling that stops it has to be one the node cannot
// raise, which is the same argument that put the credentials on this side.
//
// The token ceiling is a clamp rather than a replacement, and that distinction
// is the whole design: a node asking for 4k gets its 4k if the operator allows
// 8k, and gets 8k if it asks for 100k. Replacing the value would make the
// operator's ceiling look like the node's choice and turn a per-request knob
// into a constant.

// complete runs one provider call under the cluster's per-call bounds.
//
// It exists so both request shapes get the same deadline and the same timeout
// wording. Two call sites written separately is how the streaming path ends up
// answering a hung provider with a 500 while the buffered path answers it with
// a 504 — two different failures for one problem.
func (h *Handler) complete(ctx context.Context, req pigmodel.Request) (*pigai.AssistantMessage, error) {
	ctx, cancel := h.opts.Bounds.context(ctx)
	defer cancel()
	settled, err := h.opts.Completer.Complete(ctx, req)
	if err != nil {
		return nil, h.opts.Bounds.timeoutError(ctx, err)
	}
	return settled, nil
}

// CallBounds configures the per-call ceilings. The zero value bounds nothing,
// which is the deployment that configured neither env var.
type CallBounds struct {
	// ProviderTimeout is the wall-clock ceiling for one provider call.
	// <=0 means no bound.
	ProviderTimeout time.Duration
	// MaxOutputTokens is the operator's ceiling on one reply's output
	// tokens. <=0 means the caller's own value stands.
	MaxOutputTokens int
}

// context returns the call's context and its cancel.
//
// The cancel is returned even when there is no deadline, because the caller
// holds one variable either way and a `if deadline { defer cancel() }` at
// each of the two call sites is how one of them ends up leaking the deadline.
func (b CallBounds) context(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.ProviderTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, b.ProviderTimeout)
}

// timeoutError turns an expired deadline into the sentinel a node can act on.
//
// The distinction from a generic provider failure is the point: a node whose
// call timed out should retry with less work, while a node whose provider
// returned an error should retry at all. Both arrive as an HTTP failure, and
// only one of them is the gateway's own doing.
func (b CallBounds) timeoutError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("%w: the model call exceeded the cluster's %s ceiling; "+
		"ask for a smaller answer or fewer findings", errs.ErrUpstreamTimeout, b.ProviderTimeout)
}

// tune clamps the caller's own output ceiling to the operator's.
//
// A nil incoming tune means the caller set nothing and the registry will
// apply the model configuration; the clamp still applies, because the
// operator's ceiling is about the cluster's money and not about what the node
// remembered to send.
func (b CallBounds) tune(incoming func(*pigai.StreamOptions)) func(*pigai.StreamOptions) {
	if b.MaxOutputTokens <= 0 {
		return incoming
	}
	ceiling := b.MaxOutputTokens
	if incoming == nil {
		return func(opts *pigai.StreamOptions) { opts.MaxTokens = ceiling }
	}
	return func(opts *pigai.StreamOptions) {
		incoming(opts)
		// Clamped down only. A node that asked for less than the operator
		// allows keeps asking for less: the gateway enforces the ceiling, and
		// does not become the thing that decides how big answers are.
		if opts.MaxTokens <= 0 || opts.MaxTokens > ceiling {
			opts.MaxTokens = ceiling
		}
	}
}
