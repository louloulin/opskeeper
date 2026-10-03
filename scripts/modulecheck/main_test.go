package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moduleFixture is one module's fixture package: where its Go file lives and
// what it contains.
type moduleFixture struct {
	// Dir is the package path under the module root.
	Dir string
	// File is the file name to write.
	File string
	// Src is the Go source.
	Src string
}

// fixture builds a fake repo root containing a package for every module the
// checker knows about, then returns the root.
//
// The list comes from rules() rather than being written out here. A rule
// added without a fixture is the one case the checker would silently stop
// testing, so the fixture is derived from the same source of truth the
// checker reads.
func fixture(t *testing.T, modules ...moduleFixture) string {
	t.Helper()
	root := t.TempDir()
	declared := make(map[string]bool, len(rules()))
	for _, r := range rules() {
		declared[r.Dir] = true
	}
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	for _, m := range modules {
		if err := os.MkdirAll(filepath.Join(root, m.Dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", m.Dir, err)
		}
		declared[m.Dir] = true
		write(m.Dir+"/"+m.File, m.Src)
	}
	// Every module the checker will walk needs a directory to exist, even
	// when a test does not care about its contents.
	for dir, ok := range declared {
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			if err := os.MkdirAll(filepath.Join(root, dir, "placeholder"), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
	}
	return root
}

// standardFixture is the three-module fixture the existing tests are written
// against: core, pig, and sdk, each with a caller-supplied source.
func standardFixture(t *testing.T, coreSrc, pigSrc, sdkSrc string) string {
	t.Helper()
	return fixture(t,
		moduleFixture{Dir: "core/domain", File: "domain.go", Src: coreSrc},
		moduleFixture{Dir: "core/pig/pigmodel", File: "registry.go", Src: pigSrc},
		moduleFixture{Dir: "sdk", File: "manifest.go", Src: sdkSrc},
	)
}

const cleanCore = `package domain

import "context"

var _ = context.Background
`

const cleanPig = `package pigmodel

import (
	"context"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/vincent-wuhan/opskeeper/core/domain"
)

var _ ai.Model
var _ domain.ModelRef
var _ = context.Background
`

const cleanSDK = `package sdk

import (
	"gopkg.in/yaml.v3"

	"github.com/vincent-wuhan/opskeeper/core/domain"
)

var _ = yaml.Marshal
var _ domain.PluginMeta
`

func TestCheckPassesOnACleanTree(t *testing.T) {
	root := standardFixture(t, cleanCore, cleanPig, cleanSDK)
	v, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("expected no violations, got %v", v)
	}
}

func TestCheckCatchesPiGImportedOutsidePig(t *testing.T) {
	// The rule that motivates the whole tool: a PiG import outside the pig
	// module is what turns a PiG API break into a repository-wide one.
	bad := cleanCore[:len(cleanCore)-len("var _ = context.Background")] +
		"\n\nimport2 := \"\"\n_ = import2\n"
	// Rewrite with a PiG import in core.
	bad = `package domain

import (
	"context"

	"github.com/MichaelKinsy/PiG/ai"
)

var _ ai.Model
var _ = context.Background
`
	root := standardFixture(t, bad, cleanPig, cleanSDK)
	v, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("a PiG import inside core must be reported")
	}
	if !strings.Contains(v[0], "github.com/MichaelKinsy/PiG/ai") {
		t.Errorf("violation should name the import, got %q", v[0])
	}
}

func TestCheckCatchesInfrastructureDependencyInCore(t *testing.T) {
	bad := `package domain

import "gorm.io/gorm"

var _ gorm.DB
`
	root := standardFixture(t, bad, cleanPig, cleanSDK)
	v, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("an infrastructure dependency inside core must be reported")
	}
	if !strings.Contains(v[0], "gorm.io/gorm") {
		t.Errorf("violation should name the import, got %q", v[0])
	}
}

func TestCheckCatchesSdkReachingPiG(t *testing.T) {
	bad := `package sdk

import "github.com/MichaelKinsy/PiG/ai"

var _ ai.Model
`
	root := standardFixture(t, cleanCore, cleanPig, bad)
	v, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("sdk must not be able to reach PiG")
	}
}

func TestABizPackageMayNotReachItsOwnDataLayer(t *testing.T) {
	// The intra-context rules are written against paths, and there are two
	// path forms in play: rel is a repository-relative *file* path
	// ("core/manager/iam/biz/sso/service.go") and rest is an *import* path
	// ("core/manager/iam/data/sso"). A rule that compared either of them
	// against the context's label ("iam") would never match anything, and
	// would sit in the checker looking like enforcement. That is what this
	// test is for: it asserts the rule fires, so the day somebody rewrites
	// the comparison the failure is here rather than in a boundary nobody
	// notices is unguarded.
	cases := []struct {
		name string
		rel  string
		imp  string
		red  bool
	}{
		{
			name: "biz reaching its own data layer",
			rel:  "core/manager/iam/biz/org/service.go",
			imp:  repoModule + "/core/manager/iam/data/org/store",
			red:  true,
		},
		{
			name: "service reaching its own data layer",
			rel:  "core/manager/service/alert/list.go",
			imp:  repoModule + "/core/manager/data/alert/store",
			red:  true,
		},
		{
			name: "a test may wire the layer it tests",
			rel:  "core/manager/service/alert/list_test.go",
			imp:  repoModule + "/core/manager/data/alert/store",
			red:  false,
		},
		{
			name: "a file in the debt ledger is known, not refused",
			rel:  "core/manager/biz/audit/chain.go",
			imp:  repoModule + "/core/manager/data/audit/store",
			red:  false,
		},
		{
			name: "biz reaching its own model",
			rel:  "core/manager/iam/biz/org/service.go",
			imp:  repoModule + "/core/manager/iam/model",
			red:  false,
		},
		{
			name: "service reaching its own biz",
			rel:  "core/manager/service/alert/list.go",
			imp:  repoModule + "/core/manager/biz/alert",
			red:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkBCImport(tc.rel, tc.imp)
			if tc.red && got == "" {
				t.Errorf("%s imports %s must be refused; the layer rule did not fire",
					tc.rel, tc.imp)
			}
			if !tc.red && got != "" {
				t.Errorf("%s imports %s is allowed, got %q", tc.rel, tc.imp, got)
			}
		})
	}
}

// TestTheWalkRootsCoverEveryBoundedContext pins the list the bounded-context
// rules are checked over.
//
// It exists because the walk root was hardcoded to `internal` and stopped
// covering the BCs the moment they began moving out — the checker kept
// reporting "all boundaries hold" for a tree it was no longer reading. A
// test that only exercised checkBCImport would not have noticed, because
// the rule was fine and the walk was not.
func TestTheWalkRootsCoverEveryBoundedContext(t *testing.T) {
	roots := bcWalkRoots()
	for _, bc := range bcs {
		for _, dir := range bc.dirs {
			want := strings.TrimSuffix(dir, "/")
			if !containsString(roots, want) {
				t.Errorf("bounded context %s (%s) is not walked; the rules for it never run", bc.label, dir)
			}
		}
	}
	for _, floor := range sharedPkgs {
		want := strings.TrimSuffix(floor, "/")
		if !containsString(roots, want) {
			t.Errorf("shared floor %s is not walked; its \"must not know what a business is\" rule never runs", floor)
		}
	}
}

// TestTheBCWalkFindsACrossContextImportOutsideInternal is the end-to-end
// version: a manager file importing iam's data layer must be reported by the
// same entry point main() uses. core/manager is where the BCs live today, so
// the fixture is rooted there; what the test actually pins is that a walk
// rooted in a table finds the violation rather than walking past it.
func TestTheBCWalkFindsACrossContextImportOutsideInternal(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("core/manager/iam/data/org/store.go", "package org\n\ntype Store struct{}\n")
	write("core/manager/biz/alert/rule.go", `package alert

import "github.com/vincent-wuhan/opskeeper/core/manager/iam/data/org"

var _ org.Store
`)

	msgs, err := checkAllBC(root)
	if err != nil {
		t.Fatalf("checkAllBC: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("a manager package importing iam's data layer was not reported; the walk is not reaching the BCs")
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "bounded contexts may not reach each other") {
		t.Fatalf("violation reported, but not the cross-context one: %v", msgs)
	}
}

func TestANonTestFileMayNotReachATestOnlyImport(t *testing.T) {
	// The rule this pins is the one a comment could not hold: core/manager
	// carries a go.mod edge to core/edge so its cross-plane tests can drive
	// a real policy gate, and nothing in the module system distinguishes
	// "a test imports it" from "the package imports it".
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("core/manager/biz/alert/rule.go", `package alert

import "github.com/vincent-wuhan/opskeeper/core/edge/policygate"

var _ policygate.Call
`)
	write("core/manager/biz/alert/rule_test.go", `package alert

import "github.com/vincent-wuhan/opskeeper/core/edge/policygate"

var _ policygate.Call
`)

	msgs, err := checkTestOnlyImports(root)
	if err != nil {
		t.Fatalf("checkTestOnlyImports: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want exactly one violation (the non-test file), got %v", msgs)
	}
	if !strings.Contains(msgs[0], "core/manager/biz/alert/rule.go") {
		t.Fatalf("the violation should name the production file, got %q", msgs[0])
	}
}

func TestTheSharedFloorMayNotReachThePigAdapter(t *testing.T) {
	// The rule this pins is the second half of a refactor. Moving the three
	// PiG-facing files out of core/manager/pkg/llm is the fix; refusing them
	// a way back is the part that survives the next person who wants to
	// "just reuse the settings adapter".
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	// A production file, a test file, and a sibling that stays legal: all
	// three matter. Allowing tests would make the rule "the floor may reach
	// the adapter as long as it is embarrassed about it", and the sibling
	// is what proves the rule is scoped rather than a blanket ban on the
	// module.
	write("core/manager/pkg/llm/pigsettings.go", `package llm

import "github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"

var _ pigmodel.SettingsSource
`)
	write("core/manager/pkg/llm/router_test.go", `package llm

import "github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"

var _ pigmodel.ProviderConfig
`)
	write("core/manager/llmpig/pigsettings.go", `package llmpig

import "github.com/vincent-wuhan/opskeeper/core/pig/pigmodel"

var _ pigmodel.SettingsSource
`)
	write("core/manager/pkg/llm/wire.go", `package llm

import "github.com/vincent-wuhan/opskeeper/core/ports"

var _ ports.LLMRequest
`)

	msgs, err := checkFloorIsolation(root)
	if err != nil {
		t.Fatalf("checkFloorIsolation: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want exactly two violations (the production file and the test file), got %v", msgs)
	}
	for _, want := range []string{
		"core/manager/pkg/llm/pigsettings.go",
		"core/manager/pkg/llm/router_test.go",
	} {
		found := false
		for _, m := range msgs {
			if strings.Contains(m, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no violation named %s; got %v", want, msgs)
		}
	}
	for _, m := range msgs {
		if strings.Contains(m, "core/manager/llmpig/pigsettings.go") ||
			strings.Contains(m, "core/manager/pkg/llm/wire.go") {
			t.Errorf("rule fired on a legal file: %q", m)
		}
	}
}

func TestTheFloorIsolationRulesStillDescribeTheTree(t *testing.T) {
	// A rule whose directory does not exist checks nothing and reports
	// success, which is the failure mode of every hardcoded path list. The
	// root module directory is the repo itself, so at least one entry has to
	// resolve for the rule to mean anything.
	// The test binary runs in this package's directory, so the repository
	// root is two levels up. Hardcoding it is the point: the rule has to
	// hold against the tree that ships, not against a fixture.
	msgs, err := checkFloorIsolation(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("checkFloorIsolation on the real tree: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("the real tree violates its own floor isolation rule: %v", msgs)
	}
	for _, r := range floorIsolation {
		if _, err := os.Stat(filepath.Join("..", "..", filepath.Clean(filepath.FromSlash(r.Dir)))); err != nil {
			t.Errorf("floor isolation rule points at %s, which does not exist: %v", r.Dir, err)
		}
	}
}

func TestTheLayerDebtLedgerIsCurrent(t *testing.T) {
	// A debt ledger is only honest while every entry in it is still a debt.
	// An entry for a file that was deleted, or one that no longer imports
	// its own data layer, is a line that makes the boundary look worse than
	// it is — and the next reader has no way to tell it apart from a real
	// exception. So each entry is checked against the repository it claims to
	// describe.
	root := filepath.Join("..", "..")
	for rel, reason := range layerDebt {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is in the debt ledger with no reason", rel)
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		src, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s is in the debt ledger but cannot be read (%v); remove the entry", rel, err)
			continue
		}
		dir := bcDir(bcOf(rel))
		if dir == "" {
			t.Errorf("%s is in the debt ledger but belongs to no bounded context", rel)
			continue
		}
		if !strings.Contains(string(src), dir+"data/") {
			t.Errorf("%s no longer imports %sdata; the debt is paid, so remove the entry",
				rel, dir)
		}
	}
}

func TestCheckIgnoresTestdataFixtures(t *testing.T) {
	// A sample plugin under testdata is documentation, not compiled code.
	root := standardFixture(t, cleanCore, cleanPig, cleanSDK)
	td := filepath.Join(root, "sdk", "testdata", "sample")
	if err := os.MkdirAll(td, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(td, "main.go"), []byte(
		"package sample\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("testdata must be exempt, got %v", v)
	}
}

func TestCheckReportsEveryViolationNotJustTheFirst(t *testing.T) {
	bad := `package domain

import (
	"gorm.io/gorm"
	"github.com/gin-gonic/gin"
)

var _ gorm.DB
var _ gin.Engine
`
	root := standardFixture(t, bad, cleanPig, cleanSDK)
	v, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(v) < 2 {
		t.Errorf("a reviewer fixing boundaries wants the whole list, got %v", v)
	}
}

func TestCheckHandlesSingleLineAndGroupedImports(t *testing.T) {
	src := `package domain

import "context"

import (
	"net/http"
	"time"
)

var _ = context.Background
var _ = http.StatusOK
var _ = time.Now
`
	if got := importsOf(writeTemp(t, src)); len(got) != 3 {
		t.Errorf("importsOf found %v, want 3 stdlib imports", got)
	}
}

func TestIsStdlib(t *testing.T) {
	cases := map[string]bool{
		"context":                        true,
		"net/http":                       true,
		"encoding/json":                  true,
		"gopkg.in/yaml.v3":               false,
		"github.com/x/y":                 false,
		"github.com/MichaelKinsy/PiG/ai": false,
	}
	for imp, want := range cases {
		if got := isStdlib(imp); got != want {
			t.Errorf("isStdlib(%q) = %v, want %v", imp, got, want)
		}
	}
}

func writeTemp(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// --- edge module --------------------------------------------------------

const cleanEdge = `package pigsupervisor

import (
	"context"

	"github.com/vincent-wuhan/opskeeper/core/ports"
)

var _ = context.Background
var _ ports.AgentProcess
`

// The node plane may reach core's contracts but must not hold a PiG type.
// Letting it import PiG directly would rebuild every node binary on a PiG
// upgrade, which is the one thing the pig module exists to prevent.
func TestCheckCatchesEdgeImportingPiGDirectly(t *testing.T) {
	bad := `package pigsupervisor

import (
	"github.com/MichaelKinsy/PiG/coding/rpcclient"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

var _ ports.AgentProcess
var _ *rpcclient.RpcClient
`
	root := fixture(t,
		moduleFixture{Dir: "core/ports", File: "ports.go", Src: "package ports\n\nvar _ = struct{}{}\n"},
		moduleFixture{Dir: "core/edge/pigsupervisor", File: "supervisor.go", Src: bad},
	)
	violations, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly one for the direct PiG import", violations)
	}
	if !strings.Contains(violations[0], "PiG/coding/rpcclient") || !strings.Contains(violations[0], "edge (node plane)") {
		t.Errorf("violation = %q, want it to name both the import and the module", violations[0])
	}
}

// Reaching the pig module from edge is allowed: that is the whole point of
// routing the node agent through an adapter.
func TestCheckAllowsEdgeToReachThePigModule(t *testing.T) {
	root := fixture(t,
		moduleFixture{Dir: "core/ports", File: "ports.go", Src: "package ports\n\nvar _ = struct{}{}\n"},
		moduleFixture{Dir: "core/pig/pigrpc", File: "client.go", Src: "package pigrpc\n\nvar _ = struct{}{}\n"},
		moduleFixture{Dir: "core/edge/pigsupervisor", File: "supervisor.go", Src: cleanEdge},
	)
	violations, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("violations = %v, want none: edge may depend on core", violations)
	}
}

// edge must not reach the control plane or the plugin SDK. A node that
// imported the control plane could grow a second source of truth for
// identity and approval, which is exactly the split the module graph
// exists to keep.
func TestCheckCatchesEdgeReachingTheControlPlaneOrSDK(t *testing.T) {
	bad := `package pigsupervisor

import (
	"github.com/vincent-wuhan/opskeeper/sdk"
)

var _ sdk.PluginMeta
`
	root := fixture(t,
		moduleFixture{Dir: "sdk", File: "manifest.go", Src: "package sdk\n\ntype PluginMeta struct{}\n"},
		moduleFixture{Dir: "core/edge/pigsupervisor", File: "supervisor.go", Src: bad},
	)
	violations, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "opskeeper/sdk") {
		t.Errorf("violations = %v, want one naming the sdk import", violations)
	}
}

// A module added to rules() without a fixture in the tree would make check
// fail for the wrong reason, so the fixture helper guarantees the directory
// exists. This asserts that guarantee directly.
func TestFixtureCoversEveryDeclaredModule(t *testing.T) {
	root := fixture(t)
	for _, r := range rules() {
		if _, err := os.Stat(filepath.Join(root, r.Dir)); err != nil {
			t.Errorf("module %s has no fixture directory: %v", r.Dir, err)
		}
	}
}

// --- the PiG leak boundary ----------------------------------------------

// pigFixture writes a repo whose modules are described by mods: a map from
// module dir to that module's go.mod body, and a map from module dir to the
// Go source placed in it.
//
// The modules are discovered from the go.mod files rather than declared to
// the checker, so these fixtures also prove that discovery works — a checker
// that only knew about the modules in rules() would pass every one of them
// for the wrong reason.
func pigFixture(t *testing.T, mods map[string]string, src map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for dir, gomod := range mods {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "go.mod"), []byte(gomod), 0o644); err != nil {
			t.Fatalf("write %s/go.mod: %v", dir, err)
		}
	}
	for dir, body := range src {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.go"), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s/x.go: %v", dir, err)
		}
	}
	return root
}

// The case that motivated the check: the root module reached for PiG
// directly. It is invisible to rules() — the root module has no Allowed list
// to violate — and invisible to the BC rules, which only ask which bounded
// context reaches which. Left unchecked it is a PiG upgrade rebuilding every
// binary in the repository.
func TestPiGBoundaryCatchesADirectPiGImportInTheRootModule(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".":        "module github.com/vincent-wuhan/opskeeper\n",
			"core/pig": "module github.com/vincent-wuhan/opskeeper/core/pig\n",
		},
		map[string]string{
			"core/manager/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("violations = %v, want exactly one for the root module's PiG import", v)
	}
	if !strings.Contains(v[0], "core/manager/pkg/llm/x.go") || !strings.Contains(v[0], "PiG/ai") {
		t.Errorf("violation = %q, want it to name both the file and the import", v[0])
	}
}

// The adapter module is where a PiG type belongs, so its own imports are the
// job rather than the leak.
func TestPiGBoundaryAllowsThePigModuleItself(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".":        "module github.com/vincent-wuhan/opskeeper\n",
			"core/pig": "module github.com/vincent-wuhan/opskeeper/core/pig\n",
		},
		map[string]string{
			"core/pig/pigmodel": "package pigmodel\n\nimport (\n\t\"github.com/MichaelKinsy/PiG/ai\"\n\t\"github.com/vincent-wuhan/opskeeper/core/ports\"\n)\n\nvar _ ai.Model\nvar _ ports.Completer\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("violations = %v, want none: the pig module is the boundary", v)
	}
}

// A shipped plugin is a PiG extension that PiG loads and runs, so it must
// compile against PiG. This is the exemption that keeps the rule from being
// merely annoying, and it is granted for a reason a module cannot fake by
// being named conveniently.
func TestPiGBoundaryAllowsAPluginExtensionModule(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".":                                  "module github.com/vincent-wuhan/opskeeper\n",
			"plugins/pig-ops/pkg/extensions/ext": "module github.com/vincent-wuhan/opskeeper/plugins/pig-ops/pkg/extensions/ext\n\nrequire github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0\n",
		},
		map[string]string{
			"plugins/pig-ops/pkg/extensions/ext": "package ext\n\nimport sdk \"github.com/MichaelKinsy/PiG/extensions/sdk\"\n\nvar _ sdk.Extension\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("violations = %v, want none: a plugin extension may link the PiG SDK", v)
	}
}

// The exemption is for the extension SDK specifically, not for anything a
// module that once linked it might also reach. Without this, the SDK
// allowance would be a backdoor: add one import to a plugin module and every
// application package behind it gains PiG.
func TestPiGBoundaryDoesNotLetTheExtensionSDKAloneBeABackdoor(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".":                                  "module github.com/vincent-wuhan/opskeeper\n",
			"plugins/pig-ops/pkg/extensions/ext": "module github.com/vincent-wuhan/opskeeper/plugins/pig-ops/pkg/extensions/ext\n\nrequire github.com/MichaelKinsy/PiG/extensions/sdk v0.3.0\n",
		},
		map[string]string{
			"plugins/pig-ops/pkg/extensions/ext": "package ext\n\nimport (\n\tsdk \"github.com/MichaelKinsy/PiG/extensions/sdk\"\n\t\"github.com/MichaelKinsy/PiG/ai\"\n)\n\nvar _ sdk.Extension\nvar _ ai.Model\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 || !strings.Contains(v[0], "PiG/ai") {
		t.Errorf("violations = %v, want one naming the ai import the SDK allowance does not cover", v)
	}
}

// A nested module's imports belong to the nested module. Blaming the parent
// would point a reviewer at the wrong file, and — worse — would make the
// pig module look dirty for the extensions that legitimately live under it.
func TestPiGBoundaryAttributesANestedModulesImportsToThatModule(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".":                                  "module github.com/vincent-wuhan/opskeeper\n",
			"core/pig":                           "module github.com/vincent-wuhan/opskeeper/core/pig\n",
			"core/pig/extensions/opskeeper-gate": "module github.com/vincent-wuhan/opskeeper/core/pig/extensions/opskeeper-gate\n",
			"core/manager/pkg/llm":               "module github.com/vincent-wuhan/opskeeper/core/manager/pkg/llm\n",
		},
		map[string]string{
			// A nested module inside the root that is NOT under core/pig and
			// does reach for PiG: the violation belongs to it, by its own
			// path, not to the root module.
			"core/manager/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("violations = %v, want exactly one", v)
	}
	if !strings.HasPrefix(v[0], "core/manager/pkg/llm/x.go") {
		t.Errorf("violation = %q, want the nested module's own path", v[0])
	}
	// The exempt module is named in the message text, so only the path can
	// say who is to blame — and it must be the nested module.
	if got := v[0][:strings.Index(v[0], ":")]; got != "core/manager/pkg/llm/x.go" {
		t.Errorf("violation blames %q, want the nested module rather than the pig module", got)
	}
}

// A module with no PiG import is not reported, and a fixture with no go.mod
// at all is not an error: the checker is run against real trees and partial
// ones, and finding no modules must not read as finding a violation.
func TestPiGBoundaryIsQuietOnATreeWithNoPiGImports(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".": "module github.com/vincent-wuhan/opskeeper\n",
		},
		map[string]string{
			"core/manager/pkg/llm": "package llm\n\nimport (\n\t\"context\"\n\t\"github.com/vincent-wuhan/opskeeper/core/ports\"\n)\n\nvar _ = context.Background\nvar _ ports.Completer\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("violations = %v, want none", v)
	}

	empty := t.TempDir()
	if v, err := checkPiGBoundary(empty); err != nil || len(v) != 0 {
		t.Errorf("an empty tree gave (%v, %v), want no violations and no error", v, err)
	}
}

// The invocation `make module-check` actually uses passes "." as the root, and
// a walk rooted at "." reports the root directory's own name as ".". A guard
// that skips dot-directories without exempting the walk root therefore skips
// the entire repository before reading a file — and a boundary checker that
// checked nothing reports "all boundaries hold", which is the most expensive
// failure this tool could have.
//
// The fixture above cannot catch that: t.TempDir returns an absolute path
// whose final element is not ".", so every fixture-based test walks a real
// directory. Only an invocation from inside the tree reproduces it.
func TestPiGBoundaryActuallyWalksWhenTheRootIsDot(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".": "module github.com/vincent-wuhan/opskeeper\n",
		},
		map[string]string{
			"core/manager/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
		},
	)
	t.Chdir(root)

	roots, err := moduleRootsIn(".")
	if err != nil {
		t.Fatalf("moduleRootsIn: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal(`moduleRootsIn(".") found no modules; the walk root was skipped, ` +
			"so this check would pass on a tree it never read")
	}
	v, err := checkPiGBoundary(".")
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 {
		t.Errorf(`checkPiGBoundary(".") = %v, want the one violation it exists to find`, v)
	}
}

// The same trap one level down: the root module's own directory is walked
// from the repo root, so its walk root is the repo root — also ".".
func TestPiGBoundaryFindsAViolationInTheRootModuleWhenInvokedAsDot(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".":        "module github.com/vincent-wuhan/opskeeper\n",
			"core/pig": "module github.com/vincent-wuhan/opskeeper/core/pig\n",
		},
		map[string]string{
			"core/manager/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/coding/rpcclient\"\n\nvar _ *rpcclient.RpcClient\n",
		},
	)
	t.Chdir(root)
	v, err := checkPiGBoundary(".")
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 || !strings.Contains(v[0], "rpcclient") {
		t.Errorf("violations = %v, want the root module's PiG import found", v)
	}
}

// A hidden directory is still skipped — the root exemption must not turn into
// a blanket "walk everything", or .git and any vendored tree become input.
func TestPiGBoundaryStillSkipsHiddenDirectories(t *testing.T) {
	root := pigFixture(t,
		map[string]string{
			".": "module github.com/vincent-wuhan/opskeeper\n",
		},
		map[string]string{
			".hidden": "package hidden\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 0 {
		t.Errorf("violations = %v, want none: a hidden directory is not source", v)
	}
}

// The repository-root sentinel rule. Eight suites used to ask for go.work,
// which is gitignored; in a clean clone the question was false at every
// level, and the callers failed or skipped green. The rule has to fire on a
// new file that reintroduces the probe, in a test or not, and it has to stay
// quiet on a file that names go.work in prose or as a non-probe string.
func TestTheRootSentinelRuleFiresOnAGoWorkProbe(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("core/floor/thing/root_test.go", `package thing

import ("os"; "path/filepath")

var _ = os.Stat(filepath.Join("..", "..", "go.work"))
`)

	msgs, err := checkRootSentinel(root)
	if err != nil {
		t.Fatalf("checkRootSentinel: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want exactly one violation, got %v", msgs)
	}
	if !strings.Contains(msgs[0], "core/floor/thing/root_test.go") {
		t.Errorf("the finding does not name the offending file: %v", msgs)
	}
	if !strings.Contains(msgs[0], "reporoot.Find") {
		t.Errorf("the finding does not say what to do instead: %v", msgs)
	}
}

// The rule is not a blanket ban on the string. A file may mention go.work in
// a comment or a message; only a probe is a defect. Otherwise every file that
// explains the migration would be red.
func TestTheRootSentinelRuleIgnoresProse(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("core/floor/thing/doc.go", `package thing

// This package does not look for the go.work sentinel any more; see
// core/floor/reporoot. A string that merely mentions the file, with no
// quotes around it, is documentation rather than a probe.
`)
	msgs, err := checkRootSentinel(root)
	if err != nil {
		t.Fatalf("checkRootSentinel: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("prose about go.work was reported as a probe: %v", msgs)
	}
}

// A violation in the walk root itself must be found. The walk root's base
// name is "." when the Makefile invokes the checker, which starts with a dot;
// a hidden-directory rule that did not exempt the root would skip the whole
// tree and report success over a repository it never read.
func TestTheRootSentinelRuleFindsAViolationAtTheWalkRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe_test.go"),
		[]byte("package p\n\nvar _ = \"go.work\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Chdir(root)
	msgs, err := checkRootSentinel(".")
	if err != nil {
		t.Fatalf("checkRootSentinel: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("violations = %v, want the probe at the walk root found", msgs)
	}
}

// Every exemption carries a reason, and every exempt file exists. An
// exemption for a file that is gone is a hole nobody will ever notice.
func TestTheRootSentinelExemptionsAreLiveAndJustified(t *testing.T) {
	for rel, why := range rootSentinelExempt {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is exempt with no reason recorded", rel)
		}
		if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s is exempt from the sentinel rule but does not exist: %v", rel, err)
		}
	}
}

// The real repository must be clean under this new rule. It is the same
// statement the mutation tests above make, made against the tree that ships.
func TestTheRepositoryHasNoGoWorkProbes(t *testing.T) {
	msgs, err := checkRootSentinel(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("checkRootSentinel on the real tree: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("the real tree still probes for go.work: %v", msgs)
	}
}
