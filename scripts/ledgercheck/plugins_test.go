package ledgercheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file checks the one number in the progress table that the repository
// can answer on its own: how many tools the shipped plugin packages declare.
//
// It exists for the same reason as the weighted total, and from the same
// audit. That audit found the plugin row quoting "18 + 12 + 53 + 5" while the
// five shipped manifests declared 18 / 12 / 54 / 5 / 1 — one tool short, one
// package missing, and the arithmetic of the row wrong in a way that no prose
// review would catch because every individual number in it looked plausible.
//
// A count is the cheapest kind of fact to verify and the easiest to let drift:
// nothing fails when a tool is added, because every test in the repository is
// asking whether the new tool is *offered*, not whether the table still
// describes the fleet.

const (
	pluginManifestDir = "../../plugins/pig-ops"
	// planDRowPrefix is how the plugin row begins in the progress table.
	planDRowPrefix = "| D 插件生态 |"
	// progressHeading scopes the search; see progressRow for why.
	progressHeading = "## 六、当前实现进度"
)

// manifestNameRE reads metadata.name out of a pig-ops.yaml. The file has a
// long header comment and a nested spec, so the name is matched by shape
// rather than by line number.
var manifestNameRE = regexp.MustCompile(`(?m)^\s*name:\s*(\S+)\s*$`)

// toolEntryRE matches one entry of a spec.tools list. Only the flow-map form
// counts, and that is deliberate: `spec.autonomy.actions` lists its entries as
// block maps (`- name: ...`), and counting those is the exact mistake
// decision 104.3 recorded — the scoping gate read a signed action as a tool
// and then complained the node did not have it. A block-map tool declaration
// would need this taught a new shape, and until then it shows up as a
// mismatch against the table rather than as a silent zero.
var toolEntryRE = regexp.MustCompile(`^\s*-\s*\{.*\bname:`)

// toolsKeyRE captures the progress table's own per-package counts.
var toolsKeyRE = regexp.MustCompile(`(opskeeper-sre-[a-z-]+) (\d+)`)

// toolsTotalRE captures the total the table claims for those counts.
var toolsTotalRE = regexp.MustCompile(`= (\d+) 个工具`)

// shippedPackage is one manifest and the number of tools it declares.
type shippedPackage struct {
	name  string
	tools int
}

func readShippedPackages(t *testing.T) []shippedPackage {
	t.Helper()
	paths, err := filepath.Glob(filepath.FromSlash(pluginManifestDir + "/*/pig-ops.yaml"))
	if err != nil {
		t.Fatalf("glob manifests: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no manifests under %s; this check would pass on an empty fleet", pluginManifestDir)
	}
	out := make([]shippedPackage, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(raw)
		name := manifestNameRE.FindStringSubmatch(body)
		if name == nil {
			t.Errorf("%s declares no metadata.name; the table cannot be checked against it", path)
			continue
		}
		out = append(out, shippedPackage{name: name[1], tools: countDeclaredTools(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// countDeclaredTools counts the entries of spec.tools in one manifest.
func countDeclaredTools(body string) int {
	lines := strings.Split(body, "\n")
	inTools := false
	indent := 0
	n := 0
	for _, line := range lines {
		if !inTools {
			if strings.TrimSpace(line) == "tools:" {
				inTools = true
				indent = len(line) - len(strings.TrimLeft(line, " "))
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cur := len(line) - len(strings.TrimLeft(line, " "))
		if cur <= indent && !strings.HasPrefix(trimmed, "-") {
			break
		}
		if toolEntryRE.MatchString(line) {
			n++
		}
	}
	return n
}

// planDRow returns the plugin row of the progress table.
func planDRow(t *testing.T, ledger string) string {
	t.Helper()
	return progressRow(t, ledger, planDRowPrefix)
}

// TestTheProgressTableCountsTheToolsTheManifestsDeclare is the check that
// would have caught "18 + 12 + 53 + 5".
func TestTheProgressTableCountsTheToolsTheManifestsDeclare(t *testing.T) {
	packages := readShippedPackages(t)
	row := planDRow(t, readLedger(t))

	claimed := map[string]int{}
	for _, m := range toolsKeyRE.FindAllStringSubmatch(row, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("the table quotes an unreadable count for %s: %v", m[1], err)
		}
		claimed[m[1]] = n
	}
	if len(claimed) == 0 {
		t.Fatalf("the plugin row quotes no per-package tool counts, so nothing can be checked; expected the form "+
			"`opskeeper-sre-<name> <n> + ... = <total> 个工具`: %s", firstLine(row))
	}

	for _, pkg := range packages {
		got, ok := claimed[pkg.name]
		if !ok {
			t.Errorf("package %s is shipped (%d tools) but the progress table does not mention it; "+
				"a package nobody counts is a package nobody is reviewing", pkg.name, pkg.tools)
			continue
		}
		if got != pkg.tools {
			t.Errorf("the progress table says %s declares %d tools; its manifest declares %d",
				pkg.name, got, pkg.tools)
		}
	}
	for name := range claimed {
		found := false
		for _, pkg := range packages {
			if pkg.name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the progress table counts %s, which is not a shipped manifest under %s", name, pluginManifestDir)
		}
	}

	var total int
	for _, pkg := range packages {
		total += pkg.tools
	}
	stated := toolsTotalRE.FindStringSubmatch(row)
	if stated == nil {
		t.Fatalf("the plugin row states no total (`= <n> 个工具`); the fleet has %d", total)
	}
	got, err := strconv.Atoi(stated[1])
	if err != nil {
		t.Fatalf("unreadable total %q: %v", stated[1], err)
	}
	if got != total {
		t.Errorf("the progress table totals the plugin tools at %d; the %d shipped manifests declare %d (%s)",
			got, len(packages), total, describePackages(packages))
	}
}

func describePackages(packages []shippedPackage) string {
	parts := make([]string, 0, len(packages))
	for _, p := range packages {
		parts = append(parts, fmt.Sprintf("%s=%d", p.name, p.tools))
	}
	return strings.Join(parts, " ")
}
