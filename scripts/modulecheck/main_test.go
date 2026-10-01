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
			"internal/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("violations = %v, want exactly one for the root module's PiG import", v)
	}
	if !strings.Contains(v[0], "internal/pkg/llm/x.go") || !strings.Contains(v[0], "PiG/ai") {
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
			"internal/pkg/llm":                   "module github.com/vincent-wuhan/opskeeper/internal/pkg/llm\n",
		},
		map[string]string{
			// A nested module inside the root that is NOT under core/pig and
			// does reach for PiG: the violation belongs to it, by its own
			// path, not to the root module.
			"internal/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
		},
	)
	v, err := checkPiGBoundary(root)
	if err != nil {
		t.Fatalf("checkPiGBoundary: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("violations = %v, want exactly one", v)
	}
	if !strings.HasPrefix(v[0], "internal/pkg/llm/x.go") {
		t.Errorf("violation = %q, want the nested module's own path", v[0])
	}
	// The exempt module is named in the message text, so only the path can
	// say who is to blame — and it must be the nested module.
	if got := v[0][:strings.Index(v[0], ":")]; got != "internal/pkg/llm/x.go" {
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
			"internal/pkg/llm": "package llm\n\nimport (\n\t\"context\"\n\t\"github.com/vincent-wuhan/opskeeper/core/ports\"\n)\n\nvar _ = context.Background\nvar _ ports.Completer\n",
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
			"internal/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/ai\"\n\nvar _ ai.Model\n",
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
			"internal/pkg/llm": "package llm\n\nimport \"github.com/MichaelKinsy/PiG/coding/rpcclient\"\n\nvar _ *rpcclient.RpcClient\n",
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
