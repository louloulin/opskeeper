// Package llm — multi-provider routing.
//
// The MultiClient dispatches Chat requests to one of N pre-built sub-
// clients keyed by ChatReq.Provider. This is the implementation of the
// per-message provider/model selector (Anthropic / 智谱 / Gemini /
// OpenAI). Sub-clients are themselves *openaiClient instances — every
// supported provider exposes an OpenAI-compatible chat completions API
// (Anthropic via its OpenAI-compatible endpoint at
// api.anthropic.com/v1, Zhipu at open.bigmodel.cn/api/paas/v4, Gemini at
// generativelanguage.googleapis.com/v1beta/openai), so we route by
// (apiKey, baseURL) and keep the SDK uniform.
//
// Backwards compat: if a caller passes ChatReq.Provider == "" the router
// falls back to the default provider, which preserves the single-
// provider behaviour (just OpenAI today).
package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/pkg/prom"
)

// ProviderConfig describes one configured upstream. Models is the
// closed-set of model slugs the operator wants to expose for this
// provider; Label is the human-readable name shown in the UI dropdown.
type ProviderConfig struct {
	ID      string   // stable id: "openai" | "anthropic" | "zhipu" | "gemini"
	Label   string   // display name
	APIKey  string   // empty → provider not configured (skipped at build)
	Model   string   // default model
	BaseURL string   // optional base URL override
	Models  []string // closed-set of allowed models for the UI selector
}

// ProviderInfo is the subset of ProviderConfig safe to leak through the
// HTTP /v1/aiops/models endpoint (no API key).
type ProviderInfo struct {
	ID     string
	Label  string
	Model  string
	Models []string
}

// ProvidersResolver supplies a fresh provider catalog at call time. The
// seam exists so admin-edited DB rows (system_settings.llm.*) flow into
// the router without a manager restart. The returned slice supersedes
// any constructor-time providers when set; an empty slice falls back to
// constructor providers. defaultProvider, when non-empty and present in
// the slice, becomes the new default.
type ProvidersResolver interface {
	ResolveProviders(ctx context.Context) (providers []ProviderConfig, defaultProvider string, err error)
}

// MultiClient is a Client that fans Chat() out to a sub-client based on
// ChatReq.Provider. Sub-clients are built up-front from ProviderConfigs;
// callers add new providers via NewMultiClient. When a ProvidersResolver
// is wired (SetProvidersResolver), the catalog is refreshed lazily on
// each Chat / Providers / Default call (TTL cache so a slow resolver
// does not block hot paths).
// SubClientFactory builds the Client that serves one provider. It is the
// seam the PiG migration needs: MultiClient already speaks only the `Client`
// interface, so swapping the HTTP implementation for the PiG one is a
// factory change rather than a router rewrite.
//
// The factory receives the provider's ProviderConfig, but a PiG-backed
// factory is expected to ignore it: in that mode the registry resolves
// coordinates from live settings on every call, which is what makes an
// admin edit take effect without a router rebuild. The static config is
// then only a list of which providers exist.
//
// Returning nil means "this provider is not usable" and the entry is
// skipped, exactly as an empty API key is today.
type SubClientFactory func(cfg ProviderConfig) Client

// defaultSubClientFactory is the HTTP path: one OpenAI-compatible client per
// provider, seeded with the config's coordinates.
func defaultSubClientFactory(cfg ProviderConfig) Client {
	return New(Config{APIKey: cfg.APIKey, Model: cfg.Model, BaseURL: cfg.BaseURL}, nil, nil)
}

type MultiClient struct {
	// Static provider set — built at construction. Used as the seed and
	// as the fallback when no resolver is wired.
	staticSubs  map[string]Client
	staticInfos []ProviderInfo
	staticDefID string
	fallback    Client

	// subFactory builds a sub-client for one provider. nil means the
	// HTTP path. It is read only through buildSub so the nil check lives
	// in one place.
	subFactory SubClientFactory

	// staticDefault is the constructor's default provider id, kept so a
	// factory swap can restore the same tie-break.
	staticDefault string

	// staticCfgs is the construction-time provider list, kept so
	// SetSubClientFactory can rebuild the static set. Without it a swap
	// that arrives after boot would leave the statically-built
	// sub-clients (and therefore the fallback path) on the old backend.
	staticCfgs []ProviderConfig

	// Dynamic provider set — repopulated from the resolver every
	// resolveTTL. When the resolver returns an empty slice, the static
	// set is used. nil resolver = static-only (legacy behaviour).
	resolver   ProvidersResolver
	resolveTTL time.Duration

	mu          sync.RWMutex
	dynSubs     map[string]Client
	dynInfos    []ProviderInfo
	dynDefID    string
	dynLoadedAt time.Time
	dynActive   bool // true after the first non-empty resolver result
}

// NewMultiClient builds a router. Providers with empty APIKey are
// skipped (they stay invisible to /v1/aiops/models so the UI doesn't
// surface unusable options). The first non-skipped entry, OR the entry
// whose ID matches defaultProvider when set, is used as the default.
//
// fallback is the legacy single-provider client used when ChatReq.
// Provider is empty AND no default is configured. Pass the env-seeded
// OpenAI client here so existing callers (alert investigator, agent
// loop without explicit provider) keep working unchanged.
func NewMultiClient(providers []ProviderConfig, defaultProvider string, fallback Client) *MultiClient {
	mc := &MultiClient{
		staticSubs:    make(map[string]Client, len(providers)),
		fallback:      fallback,
		resolveTTL:    60 * time.Second,
		staticCfgs:    append([]ProviderConfig(nil), providers...),
		staticDefault: defaultProvider,
	}
	for _, p := range providers {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		sub := mc.buildSub(p)
		if sub == nil {
			continue
		}
		mc.staticSubs[p.ID] = sub
		models := p.Models
		if len(models) == 0 && p.Model != "" {
			models = []string{p.Model}
		}
		mc.staticInfos = append(mc.staticInfos, ProviderInfo{ID: p.ID, Label: p.Label, Model: p.Model, Models: models})
	}
	// Sort infos for stable JSON output.
	sort.Slice(mc.staticInfos, func(i, j int) bool { return mc.staticInfos[i].ID < mc.staticInfos[j].ID })

	if defaultProvider != "" {
		if _, ok := mc.staticSubs[defaultProvider]; ok {
			mc.staticDefID = defaultProvider
		}
	}
	if mc.staticDefID == "" && len(mc.staticInfos) > 0 {
		// Prefer the entry sorted first (deterministic) so tests don't flake.
		mc.staticDefID = mc.staticInfos[0].ID
	}
	return mc
}

// buildSub constructs the sub-client for one provider through the wired
// factory, defaulting to the HTTP path. The nil check lives here so a
// factory that declines a provider (returns nil) is skipped by every caller
// instead of storing a nil Client that panics on first use.
func (m *MultiClient) buildSub(cfg ProviderConfig) Client {
	// The factory is read under the lock: SetSubClientFactory can run at any
	// time from the assembly layer, and the dynamic resolve path calls this
	// without holding the lock. An unsynchronised read here would be a data
	// race on a field that decides which implementation serves traffic.
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buildSubLocked(cfg)
}

// buildSubLocked is buildSub for callers that already hold m.mu. It returns
// nil when the factory declines the provider, so every caller must check.
func (m *MultiClient) buildSubLocked(cfg ProviderConfig) Client {
	factory := m.subFactory
	if factory == nil {
		factory = defaultSubClientFactory
	}
	return factory(cfg)
}

// SetSubClientFactory replaces how sub-clients are built. The PiG migration
// calls this once at assembly time; passing nil restores the HTTP path.
//
// The dynamic cache is invalidated so the next resolve rebuilds every
// sub-client through the new factory. Without that, a router that had
// already resolved its catalog would keep serving clients built by the old
// factory until the TTL expired — a correctness bug that would look like
// "the switch did not take effect on some requests".
func (m *MultiClient) SetSubClientFactory(f SubClientFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subFactory = f
	// Rebuild the static set too: the constructor may already have built
	// sub-clients through the previous factory, and leaving them in place
	// would mean the fallback path (ChatReq.Provider == "" with no
	// configured default) kept talking to the old backend.
	subs := make(map[string]Client, len(m.staticCfgs))
	infos := make([]ProviderInfo, 0, len(m.staticCfgs))
	for _, p := range m.staticCfgs {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		sub := m.buildSubLocked(p)
		if sub == nil {
			continue
		}
		subs[p.ID] = sub
		models := p.Models
		if len(models) == 0 && p.Model != "" {
			models = []string{p.Model}
		}
		infos = append(infos, ProviderInfo{ID: p.ID, Label: p.Label, Model: p.Model, Models: models})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	m.staticSubs = subs
	m.staticInfos = infos
	// Re-resolve the default the same way the constructor does: the named
	// default wins when it survived the rebuild, otherwise the first
	// provider by sorted id. Leaving the old id in place would point the
	// fallback at a provider the new factory declined.
	m.staticDefID = ""
	if def := strings.TrimSpace(m.staticDefault); def != "" {
		if _, ok := subs[def]; ok {
			m.staticDefID = def
		}
	}
	if m.staticDefID == "" && len(infos) > 0 {
		m.staticDefID = infos[0].ID
	}
	m.dynSubs = nil
	m.dynInfos = nil
	m.dynDefID = ""
	m.dynLoadedAt = time.Time{}
	m.dynActive = false
}

// SetProvidersResolver wires a dynamic catalog source. Pass nil to clear.
// The resolver is queried lazily (TTL = 60s) on each Chat / Providers /
// Default call; an empty result falls back to the static set seeded at
// construction. This lets cmd/main.go layer DB-backed provider configs
// over env-seeded defaults without forcing a restart.
func (m *MultiClient) SetProvidersResolver(r ProvidersResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolver = r
	// Invalidate dynamic cache so the next call rebuilds.
	m.dynSubs = nil
	m.dynInfos = nil
	m.dynDefID = ""
	m.dynLoadedAt = time.Time{}
	m.dynActive = false
}

// SetResolveTTL overrides the dynamic-resolve cache TTL. Mainly used by
// tests; production code should leave the default.
func (m *MultiClient) SetResolveTTL(d time.Duration) {
	m.mu.Lock()
	m.resolveTTL = d
	m.mu.Unlock()
}

// activeSubs / activeInfos / activeDefID return the in-effect catalog,
// refreshing from the resolver when its TTL has elapsed. When the
// resolver yields nothing usable, the static set seeded at construction
// is returned.
func (m *MultiClient) activeSubs(ctx context.Context) (map[string]Client, []ProviderInfo, string) {
	m.mu.RLock()
	resolver := m.resolver
	ttl := m.resolveTTL
	loadedAt := m.dynLoadedAt
	dynActive := m.dynActive
	subs := m.dynSubs
	infos := m.dynInfos
	defID := m.dynDefID
	m.mu.RUnlock()

	if resolver == nil {
		return m.staticSubs, m.staticInfos, m.staticDefID
	}
	if dynActive && time.Since(loadedAt) < ttl {
		return subs, infos, defID
	}

	cfgs, def, err := resolver.ResolveProviders(ctx)
	if err != nil || len(cfgs) == 0 {
		// Soft-fail: fall back to static set, refresh the timestamp so a
		// flaky resolver doesn't hammer the DB.
		m.mu.Lock()
		m.dynLoadedAt = time.Now()
		m.dynActive = false
		m.dynSubs = nil
		m.dynInfos = nil
		m.dynDefID = ""
		m.mu.Unlock()
		return m.staticSubs, m.staticInfos, m.staticDefID
	}

	newSubs := make(map[string]Client, len(cfgs))
	newInfos := make([]ProviderInfo, 0, len(cfgs))
	for _, p := range cfgs {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		sub := m.buildSub(p)
		if sub == nil {
			continue
		}
		newSubs[p.ID] = sub
		models := p.Models
		if len(models) == 0 && p.Model != "" {
			models = []string{p.Model}
		}
		newInfos = append(newInfos, ProviderInfo{ID: p.ID, Label: p.Label, Model: p.Model, Models: models})
	}
	sort.Slice(newInfos, func(i, j int) bool { return newInfos[i].ID < newInfos[j].ID })

	resolvedDef := def
	if resolvedDef != "" {
		if _, ok := newSubs[resolvedDef]; !ok {
			resolvedDef = ""
		}
	}
	if resolvedDef == "" && len(newInfos) > 0 {
		resolvedDef = newInfos[0].ID
	}

	m.mu.Lock()
	m.dynSubs = newSubs
	m.dynInfos = newInfos
	m.dynDefID = resolvedDef
	m.dynLoadedAt = time.Now()
	m.dynActive = len(newSubs) > 0
	m.mu.Unlock()

	if len(newSubs) == 0 {
		return m.staticSubs, m.staticInfos, m.staticDefID
	}
	return newSubs, newInfos, resolvedDef
}

// Invalidate forces the next Chat / Providers / Default call to refresh
// from the resolver. Called by the LLM settings handler after a PUT so
// admin edits apply immediately rather than on the next 60s tick.
func (m *MultiClient) Invalidate() {
	m.mu.Lock()
	m.dynLoadedAt = time.Time{}
	m.dynActive = false
	m.dynSubs = nil
	m.dynInfos = nil
	m.dynDefID = ""
	m.mu.Unlock()
}

// Providers returns the currently-configured provider catalog.
// Read-only; safe to share.
func (m *MultiClient) Providers() []ProviderInfo {
	_, infos, _ := m.activeSubs(context.Background())
	out := make([]ProviderInfo, len(infos))
	copy(out, infos)
	return out
}

// Default returns the default (provider, model) pair. Empty strings
// when nothing is configured (caller should hide the model selector).
func (m *MultiClient) Default() (string, string) {
	_, infos, defID := m.activeSubs(context.Background())
	if defID == "" {
		return "", ""
	}
	for _, p := range infos {
		if p.ID == defID {
			return p.ID, p.Model
		}
	}
	return defID, ""
}

// HasProvider reports whether id is wired.
func (m *MultiClient) HasProvider(id string) bool {
	if id == "" {
		return false
	}
	subs, _, _ := m.activeSubs(context.Background())
	_, ok := subs[id]
	return ok
}

// Chat routes the request to the sub-client matching req.Provider; an
// empty provider falls back to the default sub-client, then to the
// constructor-supplied fallback. Returns an error if neither is
// configured.
//
// Self-obs: records prom.ObserveLLMCall on every path (success, error,
// timeout, missing-provider) so the ADR-026 dashboards reflect even
// configuration errors (the LLM resolver provider/model mismatch bug
// 2026-05-16 was invisible because the legacy metrics inside the sub
// client tagged everything as "model=..." with no provider label).
func (m *MultiClient) Chat(ctx context.Context, req ChatReq) (*ChatResp, error) {
	subs, _, defID := m.activeSubs(ctx)
	id := strings.TrimSpace(req.Provider)
	if id == "" {
		id = defID
	}

	start := time.Now()
	var (
		resp *ChatResp
		err  error
	)

	switch {
	case id == "":
		if m.fallback == nil {
			err = errors.New("llm: no providers configured")
		} else {
			resp, err = m.fallback.Chat(ctx, req)
		}
	default:
		sub, ok := subs[id]
		if !ok {
			err = fmt.Errorf("llm: provider %q not configured", id)
		} else {
			resp, err = sub.Chat(ctx, req)
		}
	}

	providerLabel := id
	if providerLabel == "" {
		providerLabel = "fallback"
	}
	modelLabel := strings.TrimSpace(req.Model)
	if modelLabel == "" {
		modelLabel = "(default)"
	}
	status := llmStatusFor(err)
	var inp, out int
	if resp != nil {
		inp = resp.Usage.PromptTokens
		out = resp.Usage.CompletionTokens
	}
	prom.ObserveLLMCall(providerLabel, modelLabel, status, time.Since(start).Seconds(), inp, out)
	return resp, err
}

// llmStatusFor maps an error into one of the bounded status labels the
// ADR-026 dashboards group by. timeout / rate_limited stay separate so
// operators can tell "provider slow" from "provider broken".
func llmStatusFor(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "429") {
		return "rate_limited"
	}
	return "error"
}

// ProviderInfoToWire is a small DTO helper used by the HTTP layer to
// shape the /v1/aiops/models response. Lives here so the wire shape is
// co-located with the router definition.
type ProviderInfoToWire struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Models []string `json:"models"`
	Model  string   `json:"model,omitempty"`
}

// AsWire renders the router's provider catalog into the JSON DTO the
// SPA expects.
func (m *MultiClient) AsWire() []ProviderInfoToWire {
	infos := m.Providers()
	out := make([]ProviderInfoToWire, 0, len(infos))
	for _, p := range infos {
		out = append(out, ProviderInfoToWire{
			ID:     p.ID,
			Label:  p.Label,
			Models: p.Models,
			Model:  p.Model,
		})
	}
	return out
}
