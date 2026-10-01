// Command modulecheck enforces the OpsKeeper 2.0 module boundaries that
// the Go module system alone cannot express.
//
// The toolchain already prevents cycles between separate modules, and
// go-arch-lint covers the intra-module BC split inside the main module.
// Neither catches the rule that matters most for the PiG migration:
//
//	only core/pig may import github.com/MichaelKinsy/PiG
//
// That rule is what makes a PiG API break a one-module change instead of a
// repository-wide one. It is a property of the import graph rather than of
// any single file's location, so it needs its own check.
//
// It is checked across EVERY module, including the root module that predates
// the split. That last part is not bookkeeping: the root module is where the
// PiG import actually appeared, and a check that only walked the five new
// modules would have reported "all boundaries hold" over the one file that
// broke them.
//
// Usage:
//
//	go run ./scripts/modulecheck [repo-root]
//
// Every violation found is printed, not just the first: a reviewer fixing
// boundaries wants the whole list in one pass.
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// pigModulePrefix is the import path only the pig module may own.
const pigModulePrefix = "github.com/MichaelKinsy/PiG"

// coreModulePrefix is the contract layer every other module may reach.
const coreModulePrefix = "github.com/vincent-wuhan/opskeeper/core"

// rule is one module's boundary.
type rule struct {
	// Dir is the module root relative to the repo root.
	Dir string
	// Module is the path declared in that root's go.mod. The module's own
	// packages are always permitted.
	Module string
	// Allowed lists import prefixes the module may use beyond the standard
	// library. An empty list means "standard library only".
	Allowed []string
	// Label is used in violation messages.
	Label string
}

func rules() []rule {
	return []rule{
		{
			// core is the root of the contract graph. A contract that
			// reaches into infrastructure forces every plugin to resolve
			// that infrastructure, so core gets the standard library and
			// nothing else.
			Dir:    "core",
			Module: coreModulePrefix,
			Label:  "core (contracts)",
		},
		{
			// pig is the single PiG boundary. It may reach core and PiG.
			Dir:    "core/pig",
			Module: "github.com/vincent-wuhan/opskeeper/core/pig",
			Allowed: []string{
				coreModulePrefix + "/",
				pigModulePrefix,
			},
			Label: "pig (PiG adapter)",
		},
		{
			// edge is the node plane. It reaches core for its contracts
			// and may import the pig module — but never PiG directly.
			// The node agent is high-privilege, high-churn code; letting
			// it hold a PiG type would make a PiG upgrade rebuild every
			// node binary instead of one adapter.
			Dir:    "core/edge",
			Module: "github.com/vincent-wuhan/opskeeper/core/edge",
			Allowed: []string{
				coreModulePrefix + "/",
			},
			Label: "edge (node plane)",
		},
		{
			// harness is the evaluation plane. It reaches core for its
			// contracts and nothing else: a golden-case corpus that could
			// import an implementation would make "run the regression"
			// depend on the server's dependency graph, which is exactly
			// what an evaluation is supposed to be independent of.
			Dir:    "core/harness",
			Module: "github.com/vincent-wuhan/opskeeper/core/harness",
			Allowed: []string{
				coreModulePrefix + "/",
			},
			Label: "harness (evaluation)",
		},
		{
			// sdk is what third-party plugins compile against. Reaching an
			// internal implementation would hand every plugin OpsKeeper's
			// whole dependency graph.
			Dir:    "sdk",
			Module: "github.com/vincent-wuhan/opskeeper/sdk",
			Allowed: []string{
				coreModulePrefix + "/",
				// The manifest format is YAML, so the module that
				// defines it needs a YAML parser. That is a format
				// dependency, not an OpsKeeper implementation
				// dependency, and it stays pinned to a single
				// transitive-free library.
				"gopkg.in/yaml.v3",
			},
			Label: "sdk (plugin surface)",
		},
	}
}

// ── the root module's own bounded contexts ────────────────────────────
//
// The module rules above cover the 2.0 module graph. These cover the split
// that predates it and still lives inside the root module: iam, manager
// and edgeagent are three bounded contexts that may not reach each other,
// internal/pkg is the shared floor beneath them, and a service layer may
// not reach past biz into its own data layer.
//
// The same rules are written out in .go-arch-lint.yml, where they are
// documentation rather than enforcement: go-arch-lint is not installed in
// this environment and `make arch-lint` skips silently when it is missing.
// A boundary that is only written down is a boundary that has never been
// tested, so the executable copy lives here. When one changes, change
// both — and believe the one that runs.
//
// Deliberately not a full mayDependOn matrix. These four rules are the
// ones that encode architecture; a matrix would restate the Go compiler's
// "does it compile" for intra-module edges while adding a second place to
// forget to update.

// bcs are the root module's bounded contexts, as import prefixes.
var bcs = []struct{ dir, label string }{
	{"internal/iam/", "iam"},
	{"internal/manager/", "manager"},
	{"internal/edgeagent/", "edgeagent"},
}

// sharedPkg is the floor every bounded context may use. It is business
// agnostic by rule, so nothing may be added to it that knows what a
// business is.
const sharedPkg = "internal/pkg/"

// repoModule is the root module's own import path.
const repoModule = "github.com/vincent-wuhan/opskeeper"

// checkBC enforces the root module's intra-module boundaries over every Go
// file under the given directory.
func checkBC(root, dir string) ([]string, error) {
	var violations []string
	base := filepath.Join(root, dir)
	if _, err := os.Stat(base); err != nil {
		return nil, fmt.Errorf("bounded context %s: %w", dir, err)
	}
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// testdata holds fixtures rather than compiled sources.
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for _, imp := range importsOf(path) {
			if msg := checkBCImport(rel, imp); msg != "" {
				violations = append(violations, msg)
			}
		}
		return nil
	})
	return violations, err
}

// checkBCImport reports why imp is out of bounds for a file at rel, or "".
func checkBCImport(rel, imp string) string {
	if !strings.HasPrefix(imp, repoModule+"/") {
		return "" // another OpsKeeper module (core/*) or the standard library
	}
	rest := strings.TrimPrefix(imp, repoModule+"/")

	owner := bcOf(rest)
	from := bcOf(rel)

	// The shared floor must stay business agnostic. A package there that
	// imports a bounded context is the shortest path to every context
	// importing every other one, because everything depends on the floor.
	if owner == "" {
		return ""
	}
	if strings.HasPrefix(rel, sharedPkg) {
		return fmt.Sprintf("%s: internal/pkg imports %s (%s); the shared floor must not know what a business is",
			rel, imp, owner)
	}
	if from == "" {
		// api/ and any other root-module subtree outside a bounded
		// context reaches no bounded context either.
		return fmt.Sprintf("%s imports %s; only a bounded context may reach one", rel, imp)
	}
	if from != owner {
		if reason, ok := exceptions[imp]; ok {
			// The exception is for a test unless the reason says otherwise:
			// a production file matching a test-only reason is still red.
			testOnly := strings.HasPrefix(reason, "cross-plane")
			if !testOnly || strings.HasSuffix(rel, "_test.go") {
				return ""
			}
		}
		return fmt.Sprintf("%s: %s imports %s; bounded contexts may not reach each other", rel, from, owner)
	}

	// Within one context the direction is service -> biz <- data. The
	// service layer is the transaction boundary; a handler that reaches
	// data has moved that boundary into the handler. biz reaching data is
	// the same leak one layer down: the interface data implements is
	// declared in biz, so a signature naming a data type makes data part
	// of biz's public contract, and the next layer to want that type
	// imports data to get it.
	switch {
	case strings.HasPrefix(rel, from+"service/") && strings.HasPrefix(rest, from+"data/"):
		return fmt.Sprintf("%s: %s service imports %s data; the service layer must go through biz", rel, from, from)
	case strings.HasPrefix(rel, from+"biz/") && strings.HasPrefix(rest, from+"data/"):
		return fmt.Sprintf("%s: %s biz imports %s data; a use case's own types belong in model, or the interface cannot name its results",
			rel, from, from)
	}
	return ""
}

// exceptions are the cross-context imports that exist on purpose.
//
// Each one is listed by exact import path, with the reason it is allowed,
// so the boundary stays a real boundary: a *new* cross-context import is
// red even in a package that already has an exception, because the
// exception names one path and not a directory. That is the property a
// blanket allowlist would lose.
//
// These were found by running this checker, not by reading the config —
// the rules in .go-arch-lint.yml have never been enforced here, because
// go-arch-lint is not installed and `make arch-lint` skips silently when
// it is missing. Three real edges existed underneath them.
var exceptions = map[string]string{
	// iam's HTTP handlers write to manager's audit ledger. Decision 35
	// deliberately put the audit chain in the manager's biz layer as the
	// single throat every HLD-010 row passes through, so this edge is the
	// consequence of that decision rather than an accident. It is also the
	// one edge here that a future split must resolve rather than inherit:
	// an audit port both contexts can depend on is the only version of
	// this that is not a cycle waiting to happen.
	"github.com/vincent-wuhan/opskeeper/internal/manager/biz/audit":         "iam handlers emit audit rows through the manager's single audit throat (decision 35)",
	"github.com/vincent-wuhan/opskeeper/internal/manager/model/audit":       "same edge, the audit row type the handlers construct",
	"github.com/vincent-wuhan/opskeeper/internal/manager/server/middleware": "same edge, the request id the handlers stamp rows with",

	// manager's IM bridge reads iam's model types. A model is the one
	// thing two contexts are meant to agree on; a bridge that copied the
	// type would own a second version of it.
	"github.com/vincent-wuhan/opskeeper/internal/iam/model": "manager's IM bridge reads iam's identity model types",

	// Cross-plane tests. A test that wires the node plane to the control
	// plane has to import both: the topology is the thing under test, and
	// a test that could not see both halves would be testing a mock. The
	// exception is scoped to _test.go so production code still cannot.
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/biz":                    "cross-plane topology test: the node bridge is half the subject under test",
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/cmdpolicy":              "cross-plane topology test: the node's own policy is half the subject under test",
	"github.com/vincent-wuhan/opskeeper/internal/manager/biz/nodefleet":            "cross-plane topology test: the control-plane half of the subject under test",
	"github.com/vincent-wuhan/opskeeper/internal/manager/biz/aiops/tools/basetool": "cross-plane test: the bash tool's node-side policy is what it asserts about",
}

// bcOf returns the bounded context an import path (or a file path) belongs
// to, or "" when it belongs to none.
func bcOf(path string) string {
	for _, bc := range bcs {
		if strings.HasPrefix(path, bc.dir) {
			return bc.label
		}
	}
	return ""
}

// ── the PiG leak boundary ──────────────────────────────────────────────

// The rule the rules() table above cannot express: only the pig module may
// hold a PiG type, and that has to hold for the WHOLE repository — including
// the root module, which predates the split and is not in the table.
//
// It is checked here rather than inferred from the table because a PiG
// import inside the root module is invisible to every other check in this
// file: the root module has no Allowed list to violate, and the BC rules
// only care which bounded context reaches which. The failure this prevents
// is the one the whole module graph exists for — a PiG API break turning
// into a repository-wide rebuild instead of a change to one adapter.

// pigModuleDir is the adapter module and everything under it, which is the
// one place a PiG import is the job rather than a leak.
const pigModuleDir = "core/pig"

// pigExtensionSDK is the package a module links in order to BE a PiG
// extension. PiG loads and runs those, so a module compiling against it is a
// plugin by construction rather than OpsKeeper application code.
const pigExtensionSDK = pigModulePrefix + "/extensions/sdk"

// checkPiGBoundary reports every PiG import held by a module that has not
// earned the right to one.
//
// The exemption for the extension SDK is what keeps this checkable rather
// than merely annoying. OpsKeeper ships Pi extensions as their own modules —
// the ones under plugins/pig-ops — and those must compile against PiG,
// because PiG is what loads and runs them. Distinguishing them by that fact
// rather than by a path prefix means the exemption cannot be widened by
// creating a directory with a convenient name: a new OpsKeeper module that
// wants PiG types has to link the extension SDK, which is a visible,
// deliberate act that says "this is a plugin", while application code still
// cannot reach PiG at all.
func checkPiGBoundary(root string) ([]string, error) {
	roots, err := moduleRootsIn(root)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, mod := range roots {
		if mod == pigModuleDir || strings.HasPrefix(mod, pigModuleDir+"/") {
			continue
		}
		msgs, err := checkPiGInModule(root, mod, roots)
		if err != nil {
			return nil, err
		}
		violations = append(violations, msgs...)
	}
	sort.Strings(violations)
	return violations, nil
}

// checkPiGInModule reports the offending imports in one module, skipping any
// nested module: a nested module is walked under its own root, and holding
// the parent responsible for its imports is how a rule ends up blaming the
// wrong file.
func checkPiGInModule(root, mod string, allRoots []string) ([]string, error) {
	var violations []string
	dir := filepath.Join(root, filepath.FromSlash(mod))
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// The walk root is exempt from the name-based skips below.
			// Its own name is "." whenever the caller passed "." — which is
			// exactly how this tool is invoked by `make module-check` — and
			// a guard that cannot tell the root from a hidden directory
			// would skip the entire repository before reading a single
			// file. The check below would then report "no violations" for
			// a tree it never looked at, which is the most expensive
			// possible failure for a boundary checker.
			if path == dir {
				return nil
			}
			base := info.Name()
			if base == "testdata" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			if rel, relErr := filepath.Rel(root, path); relErr == nil {
				rel = filepath.ToSlash(filepath.Clean(rel))
				if rel != mod && containsString(allRoots, rel) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for _, imp := range importsOf(path) {
			if !strings.HasPrefix(imp, pigModulePrefix) {
				continue
			}
			if imp == pigExtensionSDK {
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s: imports %q; only %s and PiG extensions (which link %s) may reach PiG",
				rel, imp, pigModuleDir, pigExtensionSDK))
		}
		return nil
	})
	return violations, err
}

// moduleRootsIn returns every Go module in the repo, as slash-cleaned paths
// relative to root. They are discovered from the go.mod files themselves
// rather than from a table, so a module added later is covered without this
// file being edited — the failure mode of every hardcoded list.
func moduleRootsIn(root string) ([]string, error) {
	var roots []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// As above: the walk root is "." when invoked the way the
			// Makefile invokes it, and skipping it would silently check
			// nothing.
			if path == root {
				return nil
			}
			base := info.Name()
			// Dependency trees and build output are not source, and
			// walking them would find other people's modules.
			if base == "testdata" || base == "node_modules" || base == "vendor" ||
				base == "dist" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "go.mod" {
			return nil
		}
		dir := filepath.Dir(path)
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil {
			rel = dir
		}
		roots = append(roots, filepath.ToSlash(filepath.Clean(rel)))
		return nil
	})
	sort.Strings(roots)
	return roots, err
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	violations, err := check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulecheck: "+err.Error())
		os.Exit(2)
	}
	bcViolations, err := checkBC(root, "internal")
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulecheck: "+err.Error())
		os.Exit(2)
	}
	violations = append(violations, bcViolations...)

	// The root module is not in rules() — it predates the split — so the
	// PiG boundary needs its own repo-wide pass to cover it.
	pigViolations, err := checkPiGBoundary(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulecheck: "+err.Error())
		os.Exit(2)
	}
	violations = append(violations, pigViolations...)

	if len(violations) == 0 {
		fmt.Println("modulecheck: all module boundaries hold")
		return
	}
	sort.Strings(violations)
	fmt.Fprintf(os.Stderr, "modulecheck: %d boundary violation(s):\n", len(violations))
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, "  "+v)
	}
	os.Exit(1)
}

// check walks every module and returns the violations it finds.
func check(root string) ([]string, error) {
	var violations []string

	// Every module root, keyed by its slash-cleaned path relative to the
	// repo root. The walk uses this to stop at nested module boundaries.
	moduleRoots := make(map[string]bool)
	for _, r := range rules() {
		moduleRoots[filepath.ToSlash(filepath.Clean(r.Dir))] = true
	}

	for _, r := range rules() {
		dir := filepath.Join(root, r.Dir)
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("module directory %s: %w", r.Dir, err)
		}
		// Walk the whole tree rather than importing the packages: a
		// violation in a file the build currently skips must still be
		// reported before it becomes reachable.
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				base := info.Name()
				// testdata and dot-directories hold fixtures, not
				// compiled sources; a sample plugin under testdata is
				// allowed to mention any import.
				if base == "testdata" || strings.HasPrefix(base, ".") {
					return filepath.SkipDir
				}
				// A nested module has its own rule. Descending into it
				// would report its imports against this module's
				// allowance, so the parent would flag the very files
				// that are supposed to hold the foreign dependency.
				// The key is repo-relative while path may be
				// absolute, so the comparison is made on the relative
				// form.
				if path != dir {
					if rel, err := filepath.Rel(root, path); err == nil {
						if moduleRoots[filepath.ToSlash(filepath.Clean(rel))] {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			for _, imp := range importsOf(path) {
				if msg := r.check(imp); msg != "" {
					violations = append(violations,
						fmt.Sprintf("%s: %s (%s)", rel, msg, r.Label))
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return violations, nil
}

// check reports a violation message for imp, or "" when it is acceptable.
func (r rule) check(imp string) string {
	if isStdlib(imp) {
		return ""
	}
	if imp == r.Module || strings.HasPrefix(imp, r.Module+"/") {
		return ""
	}
	if len(r.Allowed) == 0 {
		return fmt.Sprintf("imports %q; %s may depend only on the standard library", imp, r.Label)
	}
	for _, a := range r.Allowed {
		if imp == strings.TrimSuffix(a, "/") || strings.HasPrefix(imp, a) {
			return ""
		}
	}
	return fmt.Sprintf("imports %q, outside %s's allowed set", imp, r.Label)
}

// isStdlib reports whether imp names a standard-library package. A first
// path element without a dot is the standard library; "example.com/x" and
// "github.com/x/y" are not.
func isStdlib(imp string) bool {
	first := imp
	if i := strings.Index(imp, "/"); i >= 0 {
		first = imp[:i]
	}
	return !strings.Contains(first, ".")
}

// importsOf extracts import paths from a Go source file.
//
// It parses rather than scanning lexically, and the reason is not style. A
// lexical scan cannot tell code from data: this file's own tests embed
// `import "github.com/MichaelKinsy/PiG/ai"` inside string literals to build
// fixtures, and a scanner reports every one of them as a real violation. That
// is not a cosmetic problem — a boundary checker that fires on its own test
// file gets disabled, and a disabled boundary checker is the failure this
// whole tool exists to prevent.
//
// go/parser is in the standard library, so parsing costs no dependency and
// still runs anywhere Go runs.
//
// A file that does not parse yields no imports rather than an error. The
// caller is deciding whether a *boundary* was crossed, and a file the
// toolchain cannot read has not been shown to cross one; refusing to report
// it is the conservative direction, because the compiler is what will refuse
// to build it.
func importsOf(path string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		if spec.Path == nil {
			continue
		}
		if unquoted, err := strconv.Unquote(spec.Path.Value); err == nil {
			out = append(out, unquoted)
		}
	}
	return out
}
