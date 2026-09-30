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

// admitPackages validates each package and returns the admitted roots.
//
// Admission is a gate, not a formality. A package that fails the manifest
// rules is not written into settings at all, so the agent never starts
// with it: the alternative is a node that boots, loads code whose declared
// capabilities nobody verified, and finds out during an incident.
//
// A package that does not target the edge is refused here even though it
// validates. Manifest targets are a declaration, and a declaration the node
// does not honour is worse than none at all - it would read as "this
// plugin was reviewed for the control plane" while running on a host.
func admitPackages(roots []string) ([]pluginmanifest.Plugin, error) {
	out := make([]pluginmanifest.Plugin, 0, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("package %q: %w", root, err)
		}
		p, err := pluginmanifest.Load(abs)
		if err != nil {
			return nil, fmt.Errorf("package %q refused: %w", abs, err)
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
