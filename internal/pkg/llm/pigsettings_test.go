package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// stubCatalog is a ProviderCatalog that returns a canned slice, or an error.
type stubCatalog struct {
	providers []ProviderConfig
	def       string
	err       error
	calls     int
}

func (c *stubCatalog) ResolveProviders(context.Context) ([]ProviderConfig, string, error) {
	c.calls++
	if c.err != nil {
		return nil, "", c.err
	}
	return c.providers, c.def, nil
}

// TestSettingsSourceRejectsUnconfiguredProvider pins the fail-closed
// behaviour: a provider the catalog omits is reported absent, not as an
// empty-but-present config. The registry treats those two differently — an
// absent provider is not retried, an empty one is — so conflating them would
// hide a settings bug behind a retry storm.
func TestSettingsSourceRejectsUnconfiguredProvider(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(&stubCatalog{providers: []ProviderConfig{
		{ID: "openai", APIKey: "sk-1", Model: "gpt-5"},
	}})

	if _, ok := src.ProviderConfig(context.Background(), domain.ProviderOpenAI); !ok {
		t.Fatal("expected openai to be present")
	}
	if _, ok := src.ProviderConfig(context.Background(), domain.ProviderAnthropic); ok {
		t.Fatal("expected anthropic to be reported absent")
	}
}

// TestSettingsSourceMapsEveryField pins the field-by-field mapping. A dropped
// base URL would send the key to the vendor's default host; a dropped model
// list would make every slug resolve as "not offered". Both are silent
// failures — the first is a security bug, the second a usability one.
func TestSettingsSourceMapsEveryField(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(&stubCatalog{providers: []ProviderConfig{
		{
			ID: "custom", APIKey: "k", BaseURL: "https://vllm.internal/v1",
			Model: "qwen3-72b", Models: []string{"qwen3-72b", "qwen3-32b"},
		},
	}})

	got, ok := src.ProviderConfig(context.Background(), domain.ProviderCustom)
	if !ok {
		t.Fatal("expected custom to be present")
	}
	if got.ID != domain.ProviderCustom || got.APIKey != "k" {
		t.Errorf("id/key = %q/%q", got.ID, got.APIKey)
	}
	if got.BaseURL != "https://vllm.internal/v1" {
		t.Errorf("base URL = %q, want the configured endpoint, not a vendor default", got.BaseURL)
	}
	if got.DefaultModel != "qwen3-72b" || len(got.Models) != 2 {
		t.Errorf("model list = %q / %v", got.DefaultModel, got.Models)
	}
}

// TestSettingsSourceDefaultProviderSkipsAnUnconfiguredDefault pins the
// fall-through. A default that names an unconfigured provider would make
// every unpinned call fail with "default x is not configured" — an error that
// reads as an OpsKeeper bug rather than as the missing key it is.
func TestSettingsSourceDefaultProviderSkipsAnUnconfiguredDefault(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(&stubCatalog{
		providers: []ProviderConfig{
			{ID: "openai", APIKey: "sk-1", Model: "gpt-5"},
			{ID: "anthropic", APIKey: "", Model: "claude"},
		},
		def: "anthropic",
	})

	id, ok := src.DefaultProvider(context.Background())
	if !ok {
		t.Fatal("expected a default")
	}
	if id != domain.ProviderOpenAI {
		t.Fatalf("default = %q, want the first configured provider (openai)", id)
	}
}

// TestSettingsSourceDefaultProviderHonoursAConfiguredDefault pins the happy
// path: when the named default is usable, it is the one returned.
func TestSettingsSourceDefaultProviderHonoursAConfiguredDefault(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(&stubCatalog{
		providers: []ProviderConfig{
			{ID: "openai", APIKey: "sk-1", Model: "gpt-5"},
			{ID: "anthropic", APIKey: "sk-2", Model: "claude"},
		},
		def: "anthropic",
	})

	id, ok := src.DefaultProvider(context.Background())
	if !ok || id != domain.ProviderAnthropic {
		t.Fatalf("default = %q (ok=%v), want the configured anthropic", id, ok)
	}
}

// TestSettingsSourceReportsNothingWithNoProviders pins the empty-cluster
// case: no configured provider means no default, so the registry refuses
// rather than inventing one.
func TestSettingsSourceReportsNothingWithNoProviders(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(&stubCatalog{})
	if _, ok := src.DefaultProvider(context.Background()); ok {
		t.Fatal("expected no default when nothing is configured")
	}
}

// TestSettingsSourceFailsClosedOnAReadError pins the transient-error path. A
// settings read failure must not look like "this provider was removed":
// reporting absent is the fail-closed choice, because the caller then gets a
// clear not-configured error instead of a dial to stale coordinates.
func TestSettingsSourceFailsClosedOnAReadError(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(&stubCatalog{err: errors.New("db is down")})

	if _, ok := src.ProviderConfig(context.Background(), domain.ProviderOpenAI); ok {
		t.Fatal("expected the provider to be reported absent on a read error")
	}
	if _, ok := src.DefaultProvider(context.Background()); ok {
		t.Fatal("expected no default on a read error")
	}
}

// TestSettingsSourceIsNilSafe pins that a host with no catalog wired does not
// panic. The registry may be built before settings are available, and a nil
// dereference at boot is the least useful possible failure.
func TestSettingsSourceIsNilSafe(t *testing.T) {
	t.Parallel()
	src := NewSettingsSource(nil)
	if _, ok := src.ProviderConfig(context.Background(), domain.ProviderOpenAI); ok {
		t.Fatal("expected absent with no catalog")
	}
	if _, ok := src.DefaultProvider(context.Background()); ok {
		t.Fatal("expected no default with no catalog")
	}
}

// TestSettingsSourceSatisfiesTheRegistryContract pins that the adapter is
// what pigmodel accepts, so a future change to either side is a compile
// error rather than a runtime surprise.
func TestSettingsSourceSatisfiesTheRegistryContract(t *testing.T) {
	t.Parallel()
	var _ pigmodel.SettingsSource = NewSettingsSource(nil)
}

// TestSettingsSourceDoesNotCache pins the freshness contract the registry
// depends on: pigmodel re-reads settings on every resolution so an admin key
// rotation takes effect on the next request. An adapter that cached would
// reintroduce exactly the staleness pigmodel's design removes.
func TestSettingsSourceDoesNotCache(t *testing.T) {
	t.Parallel()
	catalog := &stubCatalog{providers: []ProviderConfig{{ID: "openai", APIKey: "sk-1"}}}
	src := NewSettingsSource(catalog)

	_, _ = src.ProviderConfig(context.Background(), domain.ProviderOpenAI)
	_, _ = src.ProviderConfig(context.Background(), domain.ProviderOpenAI)
	if catalog.calls != 2 {
		t.Fatalf("catalog calls = %d, want 2: the adapter must not cache", catalog.calls)
	}
}
