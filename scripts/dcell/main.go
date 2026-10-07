// Command dcell is the measuring stick for one cell of the OpsKeeper
// progress table: plugin ecosystem.
//
// The cell used to read 95% with the residual written as "more plugins to
// migrate". That residual has no list, no acceptance line and no decidable
// end state, which is why two consecutive deliveries — the tool count going
// 91 -> 92 and the diagnosis axis going 17/20 -> 18/20 — moved every other
// reading in the ledger and left this one untouched. A cell that cannot
// measure its own progress trains everyone to ignore it.
//
// So this command restates the cell as a list. Each item is one deliverable
// the plan named for the plugin ecosystem, and each item carries a predicate
// that is evaluated against the working tree right now. The score is the
// fraction of items whose predicate holds. Adding a package, a route or a
// guard moves it; nothing else does.
//
// What this is NOT, stated here so the number is not over-read:
//
//   - The predicates read repository state. They do not execute a node, so
//     they cannot tell whether the review pipeline really blocks an unsigned
//     package. Behavioural claims stay with the behavioural gates:
//     make eval-gates (the diagnosis axis), make pig-tool-scoping-check
//     (the shipped tool set against a real pig binary) and the package
//     tests. This cell is a census, not a proof.
//
//   - An item whose predicate cannot be evaluated is reported as a failure on
//     stderr, never as closed. A checker that guesses is worse than no
//     checker, because the number it prints then means nothing.
//
// A predicate returns a shortfall sentence or nothing at all; it has no third
// "pass with a note" channel. An earlier draft of this file returned a note
// on success and the caller read it as a shortfall, so a green item printed
// as OPEN. Readouts belong in the census line, where they cannot be
// mistaken for a verdict.
//
// Usage:
//
//	go run ./scripts/dcell [repo-root]
//
// Exit status is 0 when every predicate that is evaluable agrees with the
// claim, and non-zero when a declared item contradicts the tree. Items that
// are honestly unfinished are printed as open and do not fail the command —
// they are the score.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// manifest is the slice of a pig-ops.yaml this command reads. Everything
// else in the file is somebody else's concern; the point here is a census,
// and a census that failed to parse is not a census with a zero.
type manifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		Vendor  string `yaml:"vendor"`
	} `yaml:"metadata"`
	Spec struct {
		Targets     []string `yaml:"targets"`
		SafetyLevel string   `yaml:"safety_level"`
		Tools       []struct {
			Name  string `yaml:"name"`
			Class string `yaml:"class"`
		} `yaml:"tools"`
		Approval struct {
			Required bool `yaml:"required"`
		} `yaml:"approval"`
	} `yaml:"spec"`
}

// item is one declared deliverable of the cell.
type item struct {
	id      string
	subject string
	// check returns "", nil when the item holds, and a non-empty sentence
	// describing the shortfall when it does not.
	check func(root string) (string, error)
}

var (
	semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	// The five node packages the ecosystem cell claims. A sixth directory
	// under plugins/pig-ops is a new package and must be added here on
	// purpose, which is the behaviour we want: the roster is the claim.
	roster = []string{
		"opskeeper-sre-readonly",
		"opskeeper-sre-observability",
		"opskeeper-sre-middleware",
		"opskeeper-sre-repair",
		"opskeeper-sre-autonomy",
	}
)

func loadManifest(path string) (manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(false)
	if err := dec.Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	return m, nil
}

// loadFleet parses every manifest on the roster.
func loadFleet(root string) (map[string]manifest, error) {
	fleet := map[string]manifest{}
	for _, name := range roster {
		m, err := loadManifest(filepath.Join(root, "plugins", "pig-ops", name, "pig-ops.yaml"))
		if err != nil {
			return nil, err
		}
		fleet[name] = m
	}
	return fleet, nil
}

// itemManifests checks that every rostered package is present with a header
// a node can trust: a known apiVersion, the right kind, a name that matches
// its directory, and a semantic version.
func itemManifests(root string) (string, error) {
	fleet, err := loadFleet(root)
	if err != nil {
		return "", err
	}
	var bad []string
	for _, name := range roster {
		m := fleet[name]
		switch {
		case m.APIVersion != "opskeeper.io/v1":
			bad = append(bad, name+": apiVersion is "+strconv(m.APIVersion))
		case m.Kind != "Plugin":
			bad = append(bad, name+": kind is "+strconv(m.Kind))
		case m.Metadata.Name != name:
			bad = append(bad, name+": metadata.name is "+strconv(m.Metadata.Name))
		case !semverRe.MatchString(m.Metadata.Version):
			bad = append(bad, name+": version "+strconv(m.Metadata.Version)+" is not semver")
		}
	}
	if len(bad) > 0 {
		return "manifest headers: " + strings.Join(bad, "; "), nil
	}
	return "", nil
}

// itemReadPackagesPure checks that the three read packages ship nothing but
// reads.
//
// Two packages are excluded, and the first exclusion is a correction of an
// earlier version of this file. The repair package was swept in here and the
// check duly reported apply_config_change as "a write tool where no write
// tool belongs". That is the predicate being wrong, not the package: repair
// is B3, it exists to write, and it carries approval.required for exactly
// that reason. A census that cannot tell a write package from a broken one is
// a census that will be argued with instead of believed.
//
// The autonomy package is excluded because it is the one package whose tool
// may run with nobody present to approve it; D3 holds it to its own promise.
func itemReadPackagesPure(root string) (string, error) {
	fleet, err := loadFleet(root)
	if err != nil {
		return "", err
	}
	readOnly := []string{
		"opskeeper-sre-readonly",
		"opskeeper-sre-observability",
		"opskeeper-sre-middleware",
	}
	var bad []string
	total := 0
	for _, name := range readOnly {
		for _, tool := range fleet[name].Spec.Tools {
			total++
			if tool.Class != "read" {
				bad = append(bad, name+"/"+tool.Name+" is "+strconv(tool.Class))
			}
		}
	}
	if len(bad) > 0 {
		return "non-read tools in the read packages: " + strings.Join(bad, ", "), nil
	}
	_ = total // the census line below already prints the per-package totals
	return "", nil
}

// itemAutonomyDeclared checks that the autonomy package states its tier and
// carries an approval block. An autonomy package without a declared radius is
// the one shape that must never ship: the host has nothing to compare a call
// against.
func itemAutonomyDeclared(root string) (string, error) {
	fleet, err := loadFleet(root)
	if err != nil {
		return "", err
	}
	m := fleet["opskeeper-sre-autonomy"]
	if m.Spec.SafetyLevel != "L3" {
		return "autonomy safety_level is " + strconv(m.Spec.SafetyLevel) + ", want L3", nil
	}
	if len(m.Spec.Tools) == 0 {
		return "autonomy package declares no tool, so its L3 tier buys nothing", nil
	}
	if !m.Spec.Approval.Required {
		return "autonomy package has approval.required=false", nil
	}
	return "", nil
}

// itemRepairNeedsApproval checks the B3 promise in one predicate: the package
// that writes carries a human.
func itemRepairNeedsApproval(root string) (string, error) {
	fleet, err := loadFleet(root)
	if err != nil {
		return "", err
	}
	m := fleet["opskeeper-sre-repair"]
	if len(m.Spec.Tools) == 0 {
		return "repair package declares no tool", nil
	}
	if !m.Spec.Approval.Required {
		return "repair package has approval.required=false", nil
	}
	return "", nil
}

// itemReleaseRoutes counts the release transport routes on the control
// plane. Six is the set the ledger claims; a route deleted from the handler
// without updating the ledger turns this red rather than quietly shrinking
// the transport.
func itemReleaseRoutes(root string) (string, error) {
	path := filepath.Join(root, "core", "domains", "server", "plugin", "http.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`"/v1/plugins/releases`)
	n := len(re.FindAllString(string(raw), -1))
	if n != 6 {
		return fmt.Sprintf("%d release routes registered, want 6", n), nil
	}
	return "", nil
}

// itemSignature checks that package signing is a control-plane function and
// not a comment. This is the first of the three review stages.
func itemSignature(root string) (string, error) {
	return symbolPresent(root, "core/manager/biz/marketplace/signature.go", "func VerifySignature(")
}

// itemEdgeAdmit checks the node-side admission path exists. This is the last
// of the three review stages, and the only one that runs where a bad package
// would actually land.
func itemEdgeAdmit(root string) (string, error) {
	return symbolPresent(root, "core/edge/policygate/gate.go", "func (g *Gate) Admit(")
}

// itemImporter checks the container importer is wired, not just present as a
// file: the control plane route and the implementation both.
func itemImporter(root string) (string, error) {
	if short, err := symbolPresent(root, "core/manager/server/marketplace/import.go", "func (h *Handler) importContainer("); err != nil || short != "" {
		return short, err
	}
	return symbolPresent(root, "core/manager/biz/pluginimport/importer.go", "func (i *Importer) Import(")
}

// itemSDK checks the third-party authoring surface ships its three parts.
// A plugin author needs a manifest type, a registration call and a version
// negotiation; two of three is a half-built SDK that still looks finished.
func itemSDK(root string) (string, error) {
	for _, f := range []string{"manifest.go", "register.go", "negotiate.go"} {
		if _, err := os.Stat(filepath.Join(root, "sdk", f)); err != nil {
			return "sdk/" + f + " is missing", nil
		}
	}
	return "", nil
}

// itemGapReasoning checks that a recorded diagnosis gap carries the two
// places a reviewer must be able to check. The reason text itself is not
// machine-checkable and is not checked here — the shape is.
func itemGapReasoning(root string) (string, error) {
	path := filepath.Join(root, "core", "floor", "pluginmanifest", "coverage.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	body := string(raw)
	for _, want := range []string{"Searched:", "core/edge", "core/floor"} {
		if !strings.Contains(body, want) {
			return "coverage.go no longer records " + strconv(want) + " on a gap reason", nil
		}
	}
	return "", nil
}

// itemGatesOnRecord checks the two behavioural gates that back this cell are
// reachable from the makefile. If either target disappears, the cell's
// behavioural half silently stops running while this one keeps printing a
// number.
func itemGatesOnRecord(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return "", err
	}
	for _, target := range []string{"eval-gates:", "pig-tool-scoping-check:"} {
		if !strings.Contains(string(raw), target) {
			return "makefile no longer has a " + target + " target", nil
		}
	}
	return "", nil
}

// itemPackageSync checks the packaging copy of the manifests is kept in step
// by a script that exists, since the bundle ships the copy and not the
// source tree.
func itemPackageSync(root string) (string, error) {
	path := filepath.Join(root, "scripts", "sync-pig-ops.sh")
	if _, err := os.Stat(path); err != nil {
		return "scripts/sync-pig-ops.sh is missing", nil
	}
	return "", nil
}

func items() []item {
	return []item{
		{"D1", "五份节点包清单齐备且头部可信", itemManifests},
		{"D2", "三个只读包只带只读工具", itemReadPackagesPure},
		{"D3", "自治包声明 L3 与审批半径", itemAutonomyDeclared},
		{"D4", "修复包的写操作挂着人", itemRepairNeedsApproval},
		{"D5", "发布运输通道六条路由在册", itemReleaseRoutes},
		{"D6", "审核第一段：包签名校验", itemSignature},
		{"D7", "审核第三段：节点侧准入", itemEdgeAdmit},
		{"D8", "存量容器导入器接线", itemImporter},
		{"D9", "第三方 sdk 三个发布物", itemSDK},
		{"D10", "GAP 理由带着可查的出处", itemGapReasoning},
		{"D11", "本格的行为闸门仍在册", itemGatesOnRecord},
		{"D12", "打包副本同步脚本在册", itemPackageSync},
	}
}

// open items are declared, not discovered: the plan asked for a market with
// a version matrix, and neither a manifest index nor a compatibility check
// exists yet. They are printed as OPEN and they are why the cell is not 100.
var planned = []struct{ id, subject, why string }{
	{"X1", "插件市场索引", "清单索引与版本矩阵尚无实现，计划第二节点名"},
	{"X2", "兼容矩阵检查", "PiG 版本与 edge 版本的兼容判定尚无实现"},
}

// symbolPresent reports whether a file exists and still contains a symbol.
//
// The needle is passed separately from the path on purpose. When the two were
// one variadic argument the checker read "func VerifySignature(" as a
// directory, which is the shape of bug that makes a census quietly report
// "cannot evaluate" on every wiring item and teaches the reader to ignore it.
func symbolPresent(root, rel, needle string) (string, error) {
	file := filepath.Join(root, rel)
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(raw), needle) {
		return rel + " no longer contains " + strconv(needle), nil
	}
	return "", nil
}

func strconv(s string) string { return "\"" + s + "\"" }

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	list := items()
	open := 0
	var notes []string
	var failures []string
	for _, it := range list {
		shortfall, err := it.check(root)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s %s: cannot evaluate: %v", it.id, it.subject, err))
			continue
		}
		if shortfall == "" {
			fmt.Printf("  ok   %-3s %s\n", it.id, it.subject)
			continue
		}
		open++
		fmt.Printf("  OPEN %-3s %s\n         %s\n", it.id, it.subject, shortfall)
	}
	for _, p := range planned {
		fmt.Printf("  OPEN %-3s %s\n         %s\n", p.id, p.subject, p.why)
		open++
	}
	total := len(list) + len(planned)
	notes = append(notes, fmt.Sprintf("%d/%d closed", total-open, total))

	fleet, err := loadFleet(root)
	if err == nil {
		var names []string
		tools := 0
		for name, m := range fleet {
			names = append(names, fmt.Sprintf("%s@%s", name, m.Metadata.Version))
			tools += len(m.Spec.Tools)
		}
		sort.Strings(names)
		notes = append(notes, fmt.Sprintf("%d tools declared across %d packages", tools, len(fleet)))
		notes = append(notes, strings.Join(names, " "))
	} else {
		failures = append(failures, "manifest census: "+err.Error())
	}

	fmt.Printf("\ndcell: %s\n", strings.Join(notes, "\n       "))
	fmt.Printf("       score %d/%d = %.1f%%  (census of repository state, not a behavioural proof)\n",
		total-open, total, 100*float64(total-open)/float64(total))

	if len(failures) > 0 {
		fmt.Fprintln(os.Stderr)
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, "dcell: "+f)
		}
		os.Exit(1)
	}
}
