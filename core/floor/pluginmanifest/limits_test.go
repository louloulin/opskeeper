package pluginmanifest

import (
	"path/filepath"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/floor/skill"
	// The executors have to be registered for skill.Get to find them.
	_ "github.com/vincent-wuhan/opskeeper/core/floor/skill/builtin"
)

// highCardinalityTools are the shipped reads whose reply size the *caller*
// chooses: max_lines, max_matches, which file, which pid.
//
// This list is the assertion that §4.28.4's blocking gap stays closed. The
// gap was not that a mechanism was missing — the spill helper and its 1 MiB
// constant had been written — it was that no tool called it and no manifest
// declared anything, so the highest-volume tools on a node shipped with no
// ceiling at all.
var highCardinalityTools = []string{
	"host_dmesg",
	"host_grep_file",
	"host_lsof",
	"host_mtr",
	"host_read_journal",
	"host_sosreport",
	"host_strace",
	"host_tail_file",
	"host_traceroute",
}

// Every one of them declares a ceiling in its own metadata AND in the
// shipped manifest, and the two agree.
//
// Three assertions rather than one, because they fail for different reasons:
// the first is "the author wrote a number down", the second is "the tool the
// node runs knows about that number", and the third is "the two files have
// not drifted". A single assertion would have passed on any one of them
// alone.
func TestEveryHighCardinalityReadDeclaresACeilingInBothPlacesItExists(t *testing.T) {
	plugins, err := LoadAll(filepath.Join(repoRoot(t), "plugins", "pig-ops"))
	if err != nil {
		t.Fatalf("load the shipped plugin catalog: %v", err)
	}
	declared := map[string]domain.ToolLimits{}
	for _, p := range plugins {
		for _, tool := range p.Manifest.Spec.Tools {
			declared[tool.Name] = tool.Limits
		}
	}
	if len(declared) == 0 {
		t.Fatal("no plugin manifests were loaded; the test would pass on nothing")
	}

	for _, name := range highCardinalityTools {
		exec, ok := skill.Get(name)
		if !ok {
			t.Errorf("%s is not a registered tool; this list and the toolset have drifted", name)
			continue
		}
		fromCode := exec.Metadata().Limits
		if fromCode.OutputBytes <= 0 {
			t.Errorf("%s has caller-controlled reply volume and declares no output ceiling in its own metadata; "+
				"the node would serve it the host default, which nobody reviewed", name)
		}
		if fromCode.TimeoutSeconds <= 0 {
			t.Errorf("%s declares no wall-clock ceiling; a tool that ignores its context would hold its slot for ever", name)
		}

		fromManifest, ok := declared[name]
		if !ok {
			t.Errorf("%s is not declared in any shipped manifest", name)
			continue
		}
		if fromManifest != fromCode {
			t.Errorf("%s declares output_bytes=%d timeout_seconds=%d and its executor declares %d/%d; "+
				"the manifest is what the host enforces, so the two are one number and must be one",
				name, fromManifest.OutputBytes, fromManifest.TimeoutSeconds,
				fromCode.OutputBytes, fromCode.TimeoutSeconds)
		}
	}
}
