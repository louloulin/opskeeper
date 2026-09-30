package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/pluginmanifest"
)

// agentSettingsFile is the per-project settings the agent reads on start.
//
// A node does not pass packages on the command line: the agent discovers
// its plugins from the project config in its working directory, and the
// working directory is the single most security-relevant thing a node
// points at. Writing the file here rather than letting a package decide
// where it goes is what makes "which code runs on this host" a question
// with one answer.
const agentSettingsFile = "settings.json"

// agentConfigDirName mirrors the agent's own rule for its project config
// directory.
//
// The two must agree or the node writes a file the agent never reads, and
// the node boots with no plugins while its settings say otherwise - the
// kind of mismatch that is only discovered during an incident. Deriving it
// from the same env var the agent uses is the only way to keep them in
// step without importing the agent's paths package, which would put PiG in
// the node's dependency graph for a string.
func agentConfigDirName() string {
	if os.Getenv("PIG_USE_PI_DIRS") == "1" {
		return ".pi"
	}
	return ".pig"
}

// agentSettings is the slice of the agent's settings file this node writes.
//
// Only packages is set, and it is written whole each time rather than
// merged. A merge would preserve keys an operator added by hand, which for
// a file that decides what code runs on a host is a liability: a stale
// entry would keep a removed plugin loaded for ever.
type agentSettings struct {
	Packages []string `json:"packages"`
}

// defaultPluginScopes is what a node grants when nobody has configured
// anything else.
//
// It is exactly what the first-party read-only package declares, and that
// is the point rather than a coincidence: a third-party package needing
// anything more is refused until an operator adds the scope by hand, so
// adding a plugin is a decision somebody makes rather than a consequence
// of installing one. Before this check existed every package got every
// scope implicitly, so this is not a narrowing of what ships — it is the
// same grant, written down.
var defaultPluginScopes = []domain.Scope{
	domain.ScopeHostRead,
	domain.ScopeTopologyRO,
	domain.ScopeAlertRO,
}

// nodePluginPolicy builds the node's review policy from the environment.
//
// Everything is env-driven for the same reason the rest of the node agent
// config is: a node is provisioned on a host OpsKeeper has never seen, and
// the only thing that reaches it is install.sh and the tunnel. The
// defaults are the ones that are safe to run without being told anything.
//
// The two that matter most are the safety ceiling and the scopes, because
// both default to the *lowest* value that lets the shipped read-only
// package work. In particular the ceiling is L1, which means the L2 repair
// package is refused until an operator says this host may change things —
// a capability that has to be enabled is a capability nobody enabled by
// accident.
func nodePluginPolicy() (pluginmanifest.Policy, error) {
	level := domain.SafetyLevel(strings.TrimSpace(os.Getenv("OPSKEEPER_EDGE_MAX_SAFETY_LEVEL")))
	if level == "" {
		level = domain.SafetyL1
	}
	if !level.Valid() {
		// Refused rather than defaulted. A typo in a capability ceiling
		// is a node whose policy is not the one the operator believes it
		// has, and a node that guessed L1 would either refuse everything
		// or - if the guess went the other way - quietly host more than
		// was asked for.
		return pluginmanifest.Policy{}, fmt.Errorf(
			"OPSKEEPER_EDGE_MAX_SAFETY_LEVEL=%q is not one of L0, L1, L2, L3", level)
	}

	radius := domain.BlastRadius(strings.TrimSpace(os.Getenv("OPSKEEPER_EDGE_MAX_BLAST_RADIUS")))
	if !radius.Valid() {
		return pluginmanifest.Policy{}, fmt.Errorf(
			"OPSKEEPER_EDGE_MAX_BLAST_RADIUS=%q is not one of pod, single-ns, namespace, cluster", radius)
	}

	scopes := defaultPluginScopes
	if raw := strings.TrimSpace(os.Getenv("OPSKEEPER_EDGE_PLUGIN_SCOPES")); raw != "" {
		scopes = nil
		for _, s := range splitList(raw) {
			scopes = append(scopes, domain.Scope(s))
		}
	}

	return pluginmanifest.PolicyFor(level, radius, scopes), nil
}

// loadTrustStore reads the node's publisher keys, if it has any.
//
// A node with no trust store has no opinion about who may publish for it,
// and that is a different state from a node whose trust store is empty by
// mistake. The first is a deployment that has not been set up; the second
// is indistinguishable from the first, which is why the absence is logged
// rather than swallowed — see admitPackages.
func loadTrustStore() *pluginmanifest.TrustStore {
	path := strings.TrimSpace(os.Getenv("OPSKEEPER_EDGE_TRUST_STORE"))
	if path == "" {
		return pluginmanifest.NewTrustStore()
	}
	store, err := pluginmanifest.TrustStoreFromConfig(path)
	if err != nil {
		// A configured but unreadable trust store is a hard error, not a
		// fall back to unsigned packages: the operator asked for
		// verification and the node cannot do it, and quietly running
		// unsigned is the one answer that is certainly wrong.
		store = pluginmanifest.NewTrustStore()
		store.SetLoadError(err)
	}
	return store
}

// admitPackages reviews each package and returns the admitted ones.
//
// Admission is a gate, not a formality. A package that fails is not written
// into settings at all, so the agent never starts with it: the alternative
// is a node that boots, loads code whose declared capabilities nobody
// verified, and finds out during an incident.
//
// The review runs in a fixed order — signature, then manifest, then this
// node's policy — and the order is the security property. A package's own
// description of itself is not consulted until the bytes it is written in
// have been authenticated, so a package that lies about its capabilities
// is refused by the signature step rather than by a later check that would
// have believed it.
//
// A package that does not target the edge is refused here even though it
// validates. Manifest targets are a declaration, and a declaration the node
// does not honour is worse than none at all - it would read as "this
// plugin was reviewed for the control plane" while running on a host.
func admitPackages(roots []string, trust *pluginmanifest.TrustStore, pol pluginmanifest.Policy) ([]pluginmanifest.Plugin, error) {
	if trust == nil {
		trust = pluginmanifest.NewTrustStore()
	}
	// A node that has never been told who it trusts runs unsigned packages,
	// and says so, out loud, on every boot.
	//
	// This is a transitional default and it is the wrong default, which is
	// why it is a default at all: OpsKeeper does not ship a release private
	// key — shipping one would make every node trust whoever holds the
	// repository — so "signatures required" cannot be true on a node that
	// has not been given a key. The switch is the trust store's own
	// presence: configure one and every package must verify, and there is
	// no configuration that turns that back off. A node without a key is a
	// node that has not finished being provisioned, and it says so here
	// rather than being quietly permissive.
	pol.AllowUnsigned = len(trust.KeyIDs()) == 0
	if pol.AllowUnsigned {
		if loadErr := trust.LoadError(); loadErr != nil {
			return nil, fmt.Errorf("trust store: %w", loadErr)
		}
	}

	out := make([]pluginmanifest.Plugin, 0, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("package %q: %w", root, err)
		}
		d := pluginmanifest.Review(abs, trust, pol)
		if !d.Allowed {
			return nil, fmt.Errorf("%s", d)
		}
		p, err := pluginmanifest.Load(abs)
		if err != nil {
			// Unreachable: Review loaded it a moment ago. Kept anyway
			// because a node that skips this on the strength of a
			// preceding call is a node that depends on nothing changing in
			// between, and the cost is one re-read of a YAML file.
			return nil, fmt.Errorf("package %q was admitted and then failed to load: %w", abs, err)
		}
		if !p.RunsOn("edge") {
			return nil, fmt.Errorf("package %q does not target the edge; it declares targets %v",
				p.Name(), p.Targets())
		}
		out = append(out, p)
	}
	// Sorted by path so the settings file is byte-stable across restarts. A
	// file that rewrites itself on every boot is a diff nobody can review.
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out, nil
}

// packageRoots projects admitted packages back to the paths the agent's
// settings file carries.
func packageRoots(plugins []pluginmanifest.Plugin) []string {
	out := make([]string, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.Root)
	}
	return out
}

// writeAgentSettings points the agent at the admitted packages.
//
// It replaces the file rather than editing it, and it is written to a
// temporary name and renamed, so a node that is killed halfway through
// this leaves either the old package set or the new one - never a
// truncated file that would start an agent with no plugins at all and no
// explanation.
func writeAgentSettings(dir string, packages []string) (string, error) {
	if dir == "" {
		return "", errors.New("agent: no working directory to write settings into")
	}
	configDir := filepath.Join(dir, agentConfigDirName())
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return "", fmt.Errorf("agent: create %s: %w", configDir, err)
	}

	settings := agentSettings{Packages: make([]string, 0, len(packages))}
	for _, abs := range packages {
		settings.Packages = append(settings.Packages, fileURL(abs))
	}
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("agent: encode settings: %w", err)
	}
	body = append(body, '\n')

	final := filepath.Join(configDir, agentSettingsFile)
	tmp, err := os.CreateTemp(configDir, agentSettingsFile+".*")
	if err != nil {
		return "", fmt.Errorf("agent: stage settings: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agent: write settings: %w", err)
	}
	// The agent reads this as the user it runs as; 0640 keeps it readable
	// to that user and to root, and off the rest of the host.
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agent: chmod settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("agent: close settings: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("agent: install settings: %w", err)
	}
	return final, nil
}

// fileURL renders an absolute path the way the agent's package loader
// expects it.
func fileURL(abs string) string {
	return "file://" + (&url.URL{Path: filepath.ToSlash(abs)}).String()
}

// splitList reads a comma-separated environment list.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
