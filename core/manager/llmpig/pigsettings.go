// pigsettings.go bridges the control plane's live provider settings onto the
// pigmodel.SettingsSource contract.
//
// Why a bridge rather than a direct implementation: the setting rows live in
// core/manager, and core/pig may not import it (the module graph runs
// control-plane -> core, never back). So the adapter sits here, in the one
// package that is allowed to know both shapes — see doc.go.
//
// The adapter deliberately does NOT cache. pigmodel.Registry already re-reads
// settings on every resolution — that is the mechanism that makes an admin
// key rotation take effect on the next request — and a second TTL here would
// make the effective staleness a product of two intervals nobody can reason
// about. The underlying setting.Service carries its own 60s cache, which is
// the one place that decision belongs.
package llmpig

import (
	"context"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/manager/pkg/llm"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"
)

// ProviderCatalog is the reader side of the control plane's provider
// configuration: what the SPA model picker already consumes.
//
// It is defined here (rather than taking *managerbizsetting.LLMSettingsResolver
// directly) so the adapter can be tested without a database, and so a future
// catalog source does not have to reshape this file.
type ProviderCatalog interface {
	ResolveProviders(ctx context.Context) (providers []llm.ProviderConfig, defaultProvider string, err error)
}

// NewSettingsSource adapts a ProviderCatalog onto pigmodel.SettingsSource.
//
// Every call re-reads the catalog, so an edit lands on the next request. A
// nil catalog yields a source that reports every provider unconfigured, which
// is the correct behaviour for a host with no settings loaded: the registry
// then refuses with ErrNoFallback rather than dialling a host nobody named.
func NewSettingsSource(catalog ProviderCatalog) pigmodel.SettingsSource {
	return &catalogSettings{catalog: catalog}
}

type catalogSettings struct {
	catalog ProviderCatalog
}

// llm.ProviderConfig implements pigmodel.SettingsSource.
//
// A provider the catalog omits reports ok=false, which the registry treats
// as "not configured" rather than "misconfigured". The distinction matters:
// the first is not retried, the second is.
func (s *catalogSettings) ProviderConfig(ctx context.Context, id domain.ProviderID) (pigmodel.ProviderConfig, bool) {
	if s.catalog == nil {
		return pigmodel.ProviderConfig{}, false
	}
	providers, _, err := s.catalog.ResolveProviders(ctx)
	if err != nil {
		// A transient settings read failure must not look like "this
		// provider was removed": that would make the registry report an
		// unknown provider and stop retrying. Reporting "not present"
		// here is the fail-closed choice — the caller gets a clear
		// not-configured error instead of a dial to stale coordinates.
		return pigmodel.ProviderConfig{}, false
	}
	for _, p := range providers {
		if domain.ProviderID(p.ID) != id {
			continue
		}
		return pigmodel.ProviderConfig{
			ID:           id,
			APIKey:       p.APIKey,
			BaseURL:      p.BaseURL,
			Models:       append([]string(nil), p.Models...),
			DefaultModel: p.Model,
		}, true
	}
	return pigmodel.ProviderConfig{}, false
}

// DefaultProvider implements pigmodel.SettingsSource.
//
// It returns the catalog's default provider when that provider is actually
// configured. A default that names an unconfigured provider would make every
// unpinned call fail with "default x is not configured" — an error that
// reads as a bug in OpsKeeper rather than as the missing key it is — so an
// unusable default falls through to the first configured provider, which is
// the same tie-break the SPA picker applies.
func (s *catalogSettings) DefaultProvider(ctx context.Context) (domain.ProviderID, bool) {
	if s.catalog == nil {
		return "", false
	}
	providers, def, err := s.catalog.ResolveProviders(ctx)
	if err != nil {
		return "", false
	}
	def = strings.TrimSpace(def)
	configured := func(p llm.ProviderConfig) bool { return strings.TrimSpace(p.APIKey) != "" }
	if def != "" {
		for _, p := range providers {
			if p.ID == def && configured(p) {
				return domain.ProviderID(def), true
			}
		}
	}
	for _, p := range providers {
		if configured(p) {
			return domain.ProviderID(p.ID), true
		}
	}
	return "", false
}

// Compile-time proof the adapter satisfies the contract it bridges to.
var _ pigmodel.SettingsSource = (*catalogSettings)(nil)
