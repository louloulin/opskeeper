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
