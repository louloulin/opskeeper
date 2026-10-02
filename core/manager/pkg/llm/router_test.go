package llm

import (
	"context"
	"errors"
	"testing"
)

// stubClient records the last ChatReq and returns a canned response.
type stubClient struct {
	id   string
	last ChatReq
}

func (s *stubClient) Chat(_ context.Context, req ChatReq) (*ChatResp, error) {
	s.last = req
	c := "from-" + s.id
	return &ChatResp{Assistant: Message{Role: "assistant", Content: c}}, nil
}

func TestMultiClient_RoutesByProvider(t *testing.T) {
	openai := &stubClient{id: "openai"}
	zhipu := &stubClient{id: "zhipu"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{"openai": openai, "zhipu": zhipu},
		staticInfos: []ProviderInfo{{ID: "openai"}, {ID: "zhipu"}},
		staticDefID: "openai",
	}

	resp, err := mc.Chat(context.Background(), ChatReq{Provider: "zhipu", Model: "glm-4-plus"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-zhipu" {
		t.Errorf("routed to wrong sub: %q", resp.Assistant.Content)
	}
	if zhipu.last.Model != "glm-4-plus" {
		t.Errorf("model passthrough: %q", zhipu.last.Model)
	}
}

func TestMultiClient_DefaultsWhenProviderEmpty(t *testing.T) {
	openai := &stubClient{id: "openai"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{"openai": openai},
		staticInfos: []ProviderInfo{{ID: "openai"}},
		staticDefID: "openai",
	}
	resp, err := mc.Chat(context.Background(), ChatReq{})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-openai" {
		t.Errorf("expected default provider; got %q", resp.Assistant.Content)
	}
}

func TestMultiClient_FallbackWhenNoDefault(t *testing.T) {
	fb := &stubClient{id: "fallback"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{},
		staticInfos: nil,
		fallback:    fb,
	}
	resp, err := mc.Chat(context.Background(), ChatReq{})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-fallback" {
		t.Errorf("expected fallback; got %q", resp.Assistant.Content)
	}
}

func TestMultiClient_UnknownProviderErrors(t *testing.T) {
	openai := &stubClient{id: "openai"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{"openai": openai},
		staticInfos: []ProviderInfo{{ID: "openai"}},
		staticDefID: "openai",
	}
	_, err := mc.Chat(context.Background(), ChatReq{Provider: "anthropic"})
	if err == nil || !errors.Is(err, err) {
		t.Fatalf("expected provider-not-configured error; got %v", err)
	}
}

func TestNewMultiClient_SkipsEmptyAPIKey(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", Label: "OpenAI", APIKey: "sk-test", Model: "gpt-4o"},
		{ID: "anthropic", Label: "Anthropic", APIKey: "", Model: "claude-3-5-sonnet"},
	}, "", nil)

	infos := mc.Providers()
	if len(infos) != 1 || infos[0].ID != "openai" {
		t.Fatalf("expected openai only; got %+v", infos)
	}
	if !mc.HasProvider("openai") || mc.HasProvider("anthropic") {
		t.Errorf("HasProvider mismatch")
	}
	defID, defModel := mc.Default()
	if defID != "openai" || defModel != "gpt-4o" {
		t.Errorf("default = %q/%q", defID, defModel)
	}
}

func TestNewMultiClient_ExplicitDefault(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", APIKey: "k1", Model: "gpt-4o"},
		{ID: "zhipu", APIKey: "k2", Model: "glm-4-plus"},
	}, "zhipu", nil)
	id, _ := mc.Default()
	if id != "zhipu" {
		t.Errorf("default = %q, want zhipu", id)
	}
}

// TestMultiClient_UsesTheSubClientFactory pins the PiG migration seam: every
// sub-client must be built through the wired factory, both for the
// construction-time providers and for the ones refreshed from the resolver.
// If either path kept calling New() directly, a deployment that switched
// backends would keep serving HTTP clients for one of its two code paths —
// a split brain that only shows up as inconsistent provider behaviour.
func TestMultiClient_UsesTheSubClientFactory(t *testing.T) {
	var built []string
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", Label: "OpenAI", APIKey: "sk-1", Model: "gpt-4o"},
		{ID: "zhipu", Label: "GLM", APIKey: "sk-2", Model: "glm-4"},
	}, "", nil)
	mc.SetSubClientFactory(func(cfg ProviderConfig) Client {
		built = append(built, cfg.ID)
		return &stubClient{id: "factory-" + cfg.ID}
	})
	// The dynamic path rebuilds the catalog; wire a resolver that yields a
	// provider so both paths run.
	mc.SetProvidersResolver(&stubProvidersResolver{cfg: []ProviderConfig{
		{ID: "anthropic", APIKey: "sk-3", Model: "claude"},
	}})

	resp, err := mc.Chat(context.Background(), ChatReq{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-factory-anthropic" {
		t.Fatalf("routed to %q, want the factory-built sub-client", resp.Assistant.Content)
	}
	// The dynamic refresh must also have gone through the factory.
	for _, id := range built {
		if id == "anthropic" {
			return
		}
	}
	t.Fatalf("factory saw %v, want it to have built the dynamically-resolved anthropic", built)
}

// TestMultiClient_FactoryCanDeclineAProvider pins that a factory returning nil
// removes the provider from the catalog rather than storing a nil Client. A
// stored nil would panic on the next Chat instead of failing the request.
func TestMultiClient_FactoryCanDeclineAProvider(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", APIKey: "sk-1", Model: "gpt-4o"},
	}, "", nil)
	mc.SetSubClientFactory(func(ProviderConfig) Client { return nil })

	if mc.HasProvider("openai") {
		t.Fatal("a declined provider must not be routable")
	}
	// The failure must be an error, not a panic — a stored nil Client
	// would dereference on the first routed call. Recovering is the only
	// way to assert "did not panic" from a test.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("a declined provider caused a panic instead of an error: %v", r)
			}
		}()
		if _, err := mc.Chat(context.Background(), ChatReq{Provider: "openai"}); err == nil {
			t.Fatal("expected an error for a declined provider")
		}
	}()
}

// TestMultiClient_SetSubClientFactoryInvalidatesTheCache pins that switching
// factories takes effect immediately. Without invalidation a router that had
// already resolved its catalog would keep serving clients built by the old
// factory until the TTL expired — the classic "the switch worked on some
// requests" bug.
func TestMultiClient_SetSubClientFactoryInvalidatesTheCache(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", APIKey: "sk-1", Model: "gpt-4o"},
	}, "", nil)
	mc.SetProvidersResolver(&stubProvidersResolver{cfg: []ProviderConfig{
		{ID: "openai", APIKey: "sk-1", Model: "gpt-4o"},
	}})

	mc.SetSubClientFactory(func(cfg ProviderConfig) Client { return &stubClient{id: "first"} })
	// Warm the dynamic cache. Providers() drives the same activeSubs path
	// Chat does, without making a real network call.
	if got := mc.Providers(); len(got) != 1 {
		t.Fatalf("warm catalog = %+v, want one provider", got)
	}

	mc.SetSubClientFactory(func(cfg ProviderConfig) Client { return &stubClient{id: "second"} })
	resp, err := mc.Chat(context.Background(), ChatReq{Provider: "openai"})
	if err != nil {
		t.Fatalf("chat after swap: %v", err)
	}
	if resp.Assistant.Content != "from-second" {
		t.Fatalf("content = %q, want the swapped factory to serve immediately without waiting for the TTL",
			resp.Assistant.Content)
	}
}

// stubProvidersResolver is a fixed-catalog ProvidersResolver.
type stubProvidersResolver struct {
	cfg []ProviderConfig
	def string
}

func (r *stubProvidersResolver) ResolveProviders(context.Context) ([]ProviderConfig, string, error) {
	return r.cfg, r.def, nil
}

// TestMultiClient_FactorySwapPreservesTheConfiguredDefault pins that a swap
// rebuilds the default the same way the constructor resolved it. A rebuild
// that left the old default id in place would route the fallback path at a
// provider the new factory had declined.
func TestMultiClient_FactorySwapPreservesTheConfiguredDefault(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "anthropic", APIKey: "sk-1", Model: "claude"},
		{ID: "openai", APIKey: "sk-2", Model: "gpt-5"},
	}, "openai", nil)

	if id, _ := mc.Default(); id != "openai" {
		t.Fatalf("default before swap = %q, want the configured openai", id)
	}
	mc.SetSubClientFactory(func(cfg ProviderConfig) Client { return &stubClient{id: cfg.ID} })
	if id, _ := mc.Default(); id != "openai" {
		t.Fatalf("default after swap = %q, want the configured openai preserved", id)
	}
}

// TestMultiClient_FactorySwapMovesTheDefaultWhenDeclined pins the other half:
// when the swap declines the configured default, the router must fall back to
// the first surviving provider rather than keep pointing at a dead one.
func TestMultiClient_FactorySwapMovesTheDefaultWhenDeclined(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "anthropic", APIKey: "sk-1", Model: "claude"},
		{ID: "openai", APIKey: "sk-2", Model: "gpt-5"},
	}, "openai", nil)

	mc.SetSubClientFactory(func(cfg ProviderConfig) Client {
		if cfg.ID == "openai" {
			return nil // declined: no credential for it in the new backend
		}
		return &stubClient{id: cfg.ID}
	})
	if id, _ := mc.Default(); id != "anthropic" {
		t.Fatalf("default after a declining swap = %q, want the surviving anthropic", id)
	}
}
