// pigregistry.go assembles the PiG-backed Client from a settings source.
//
// It exists so cmd/opskeeper has exactly one line of PiG wiring and no
// knowledge of pigmodel's types. The registry is a long-lived object — it
// caches one provider per id and each provider owns an HTTP transport — so
// it is built once and shared by every routed sub-client. Sharing is not an
// optimisation detail: a registry built per request would open a new
// transport per call and never observe the connection reuse the prompt-cache
// session id depends on.
package llm

import (
	"context"
	"log/slog"

	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// PigRegistry wraps a pigmodel.Registry for the assembly layer.
type PigRegistry struct {
	registry *pigmodel.Registry
	log      *slog.Logger
}

// NewPigRegistry builds a registry over src. A nil src yields a registry
// that reports every provider unconfigured, which is the fail-closed
// behaviour: the first Chat fails with a clear "no provider configured"
// rather than dialling a host nobody named.
func NewPigRegistry(src pigmodel.SettingsSource, log *slog.Logger) *PigRegistry {
	if log == nil {
		log = slog.Default()
	}
	return &PigRegistry{registry: pigmodel.NewRegistry(src), log: log}
}

// Registry exposes the underlying pigmodel registry.
//
// The chat kernel resolves one model per turn through the same interface the
// registry already implements, and it must resolve through the SAME cache: a
// second registry would open a second HTTP transport per provider and the
// prompt-cache session id would stop being reused across the two paths.
func (r *PigRegistry) Registry() *pigmodel.Registry {
	if r == nil {
		return nil
	}
	return r.registry
}

// Client returns a Client backed by the shared registry.
//
// It returns a fresh Client value per call; the registry (and therefore the
// provider cache and its transports) is shared. The returned Client is
// non-nil by construction — the registry is never nil here — so the router's
// factory can treat a nil return as "decline this provider" without
// ambiguity.
func (r *PigRegistry) Client() Client {
	c, err := NewPigClient(PigClientConfig{Registry: r.registry})
	if err != nil {
		// NewPigClient only fails on a nil registry, which cannot happen
		// here. A panic would take down the manager at boot over an
		// impossible state; returning a failing Client keeps the process
		// alive so the operator can see the error at the call site
		// instead of a stack trace.
		r.log.Error("llm: pig client construction failed; provider calls will fail", slog.Any("err", err))
		return failingClient{err: err}
	}
	return c
}

// Close releases every cached provider's transport. The host calls it on
// shutdown so a rolling restart does not leak connections.
func (r *PigRegistry) Close() error {
	if r == nil || r.registry == nil {
		return nil
	}
	return r.registry.Close()
}

// failingClient is the Client returned when construction failed. It reports
// the original error on every call rather than panicking or silently
// succeeding with a zero response.
type failingClient struct{ err error }

func (c failingClient) Chat(_ context.Context, _ ChatReq) (*ChatResp, error) {
	return nil, c.err
}
