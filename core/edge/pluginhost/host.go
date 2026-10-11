// Package pluginhost is the edge-side plugin host: the node half of the
// microkernel.
//
// It is the mirror of the control plane's host. The kernel
// (core/floor/kernel) is the registry and the admission gate; the manager
// host (core/manager/middleware/pluginhost) walks a directory of packages
// and activates control-plane contributions; this one walks a directory of
// packages and activates node contributions — the collectors a node runs to
// produce telemetry.
//
// The symmetry is the point. Before this, a node's collector set was a
// literal slice in cmd/opskeeper-edge/main.go: promtail, otelcol, the
// in-process scraper, the database exporters. Adding a collector meant
// editing the node binary. Now a collector is a package a node admits
// through the same gate a tool package already passes, and the message a
// node reports on refusal is the message the control plane reports, because
// it is the same gate.
//
// Two halves make up "a node hosts a package", and this is only one of
// them:
//
//   - The tool packages (plugins/pig-ops/*) ship PiG extensions and are
//     loaded by the agent process. That path already exists.
//   - The collector packages ship a collector the Supervisor runs. That is
//     what this host activates — the signal-producing half, which is what
//     makes a node's telemetry set extensible without a node rebuild.
package pluginhost

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/edge/plugins"
	"github.com/vincent-wuhan/opskeeper/core/floor/kernel"
	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"
)

// CollectorSink is where an activated collector is registered. The edge
// Supervisor satisfies it; keeping it an interface is what lets a test
// stand in without starting a subprocess.
type CollectorSink interface {
	Register(p plugins.Plugin)
}

// Activator builds the contribution for one package.
type Activator func() kernel.Contribution

// CollectorContribution contributes one collector under KindCollector,
// keyed by the collector's own signal name.
type CollectorContribution struct {
	// Plugin is the manifest name this contribution belongs to.
	Plugin string
	// Collector is the edge collector to run.
	Collector plugins.Plugin
}

func (c CollectorContribution) PluginName() string { return c.Plugin }

func (c CollectorContribution) Register(r kernel.Registrar) error {
	if c.Collector == nil {
		return fmt.Errorf("edge pluginhost: contribution for %q has no collector", c.Plugin)
	}
	return r.Register(kernel.KindCollector, c.Collector.Name(), c.Collector)
}

// Host admits and activates edge-targeted packages from a directory.
type Host struct {
	root string
	pol  kernel.Policy
	kern *kernel.Host
	sink CollectorSink
	log  *slog.Logger

	mu         sync.Mutex
	activators map[string]Activator
}

// New returns a host that reads packages from root and activates their
// collectors into sink.
func New(root string, pol kernel.Policy, sink CollectorSink, log *slog.Logger) *Host {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Host{
		root:       root,
		pol:        pol,
		kern:       kernel.NewHost(kernel.NewRegistry(), pol, log),
		sink:       sink,
		log:        log,
		activators: map[string]Activator{},
	}
}

// Registry returns the kernel registry the host activates into.
func (h *Host) Registry() *kernel.Registry { return h.kern.Registry() }

// RegisterActivator registers how the package named name contributes.
func (h *Host) RegisterActivator(name string, a Activator) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.activators[name] = a
}

// Report is what Load did.
type Report struct {
	Activated   []string
	Refused     map[string]string
	Skipped     []string
	NoActivator []string
}

// Load walks the package directory, admits every edge-targeted package,
// activates the contribution registered for each, and registers every
// resulting collector into the sink.
//
// Fail-closed in the same three ways as the control plane: a package aimed
// at the manager is skipped, one above the node's ceiling is refused, one
// whose activation fails partway is rolled back. One refusal never stops
// the others.
func (h *Host) Load(ctx context.Context) (Report, error) {
	rep := Report{Refused: map[string]string{}}

	packages, err := pluginmanifest.LoadAll(h.root)
	if err != nil {
		return rep, fmt.Errorf("edge pluginhost: reading packages from %s: %w", h.root, err)
	}

	h.mu.Lock()
	activators := make(map[string]Activator, len(h.activators))
	for k, v := range h.activators {
		activators[k] = v
	}
	h.mu.Unlock()

	for _, p := range packages {
		name := p.Name()
		if !p.RunsOn(h.pol.Target) {
			rep.Skipped = append(rep.Skipped, name)
			continue
		}
		if err := h.kern.Admit(p); err != nil {
			rep.Refused[name] = err.Error()
			h.log.Warn("edge plugin refused by admission", slog.String("plugin", name), slog.Any("err", err))
			continue
		}
		act, ok := activators[name]
		if !ok {
			rep.NoActivator = append(rep.NoActivator, name)
			continue
		}
		if err := h.kern.Activate(ctx, p, act()); err != nil {
			rep.Refused[name] = err.Error()
			continue
		}
		rep.Activated = append(rep.Activated, name)
	}

	// Register every activated collector with the sink. The order is the
	// registry's (by collector name) so the node's collector set is stable
	// across boots: a listing that reshuffled would be a diff an operator
	// has to read twice.
	if h.sink != nil {
		for _, e := range h.kern.Registry().All(kernel.KindCollector) {
			if c, ok := e.Impl.(plugins.Plugin); ok {
				h.sink.Register(c)
			}
		}
	}

	sort.Strings(rep.Activated)
	sort.Strings(rep.Skipped)
	sort.Strings(rep.NoActivator)
	return rep, nil
}

// EdgePolicy is the admission ceiling for a node host that hosts collector
// plugins: L1, no approval, no blast radius. A collector produces telemetry
// and nothing else; a package that declares more than that is refused.
func EdgePolicy() kernel.Policy {
	return kernel.Policy{
		Target:         domain.TargetEdge,
		MaxSafetyLevel: domain.SafetyL1,
		MaxBlastRadius: domain.RadiusNone,
		GrantedScopes:  domain.Scopes{domain.ScopeHostRead},
	}
}
