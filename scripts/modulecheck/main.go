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

// sdkModulePrefix is the plugin SDK a plugin module compiles against.
const sdkModulePrefix = "github.com/vincent-wuhan/opskeeper/sdk"

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
	// AnyVendor exempts third-party imports from the allowed set. It is
	// for the modules whose job is to sit on top of a lot of libraries —
	// the control plane runs on gorm, redis, pgx, kafka and otel, and
	// listing each one would be a second dependency list to maintain that
	// says nothing about OpsKeeper's own architecture. OpsKeeper imports
	// are still checked, so the rule that matters (which module may reach
	// which) keeps its teeth.
	AnyVendor bool
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
			// (contracts, the pig adapter, and the floor it shares with
			// the control plane) — but never PiG directly, and never
			// another OpsKeeper module's internals.
			//
			// The node agent is high-privilege, high-churn code; letting
			// it hold a PiG type would make a PiG upgrade rebuild every
			// node binary instead of one adapter. Now that the tree is a
			// module in its own right, that is also what the toolchain
			// refuses: the four third-party allowances below are the
			// formats and protocols the node actually speaks (process
			// and host metrics, Prometheus exposition, errgroup, YAML
			// policy files), and nothing else.
			Dir:    "core/edge",
			Module: "github.com/vincent-wuhan/opskeeper/core/edge",
			Allowed: []string{
				coreModulePrefix + "/",
				"github.com/prometheus/",
				"github.com/shirou/gopsutil/",
				"golang.org/x/sync",
				"gopkg.in/yaml.v3",
			},
			Label: "edge (node plane)",
		},
		{
			// manager is the control plane, and it is the second-to-last
			// module of the split: iam and the manager's biz/data/model/
			// server/service layers live here, together with the
			// infrastructure they run on (middleware adapters, the
			// knowledge base, dataguard, agentteams, migrations).
			//
			// Its OpsKeeper imports are core's contract layer — which
			// includes core/pig, so model access stays behind the one
			// module that absorbs a PiG API break — and sdk. Vendors are
			// exempt (AnyVendor): the control plane is expected to sit on
			// a large library set, and enumerating it here would be a
			// dependency list with no architectural statement in it.
			Dir:       "core/manager",
			Module:    "github.com/vincent-wuhan/opskeeper/core/manager",
			Allowed:   []string{coreModulePrefix + "/", sdkModulePrefix},
			AnyVendor: true,
			Label:     "manager (control plane)",
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
		{
			// floor is the infrastructure both planes need: process
			// configuration, logging, the governance manifest, the metric
			// registry, the node transport and the host skill registry.
			// It is a module rather than a package inside core because
			// core must stay stdlib-only: a plugin that had to resolve a
			// Prometheus client to read a contract type would be paying
			// for infrastructure it does not use. Its allowance is core's
			// contract subpackages, the plugin SDK, and the three
			// libraries that are the formats and protocols it speaks —
			// nothing that knows what a business is (decision 60).
			Dir:    "core/floor",
			Module: "github.com/vincent-wuhan/opskeeper/core/floor",
			Allowed: []string{
				coreModulePrefix + "/",
				sdkModulePrefix,
				"github.com/prometheus/",
				"github.com/singchia/geminio",
				"gopkg.in/yaml.v3",
			},
			Label: "floor (shared infrastructure)",
		},
	}
}

// ── the root module's own bounded contexts ────────────────────────────
//
// The module rules above cover the 2.0 module graph. These cover the split
// inside the manager module: iam and the control plane proper are two
// bounded contexts that may not reach each other, core/manager/pkg is the
// shared floor beneath them, and a service layer may not reach past biz
// into its own data layer.
//
// They are still here after the module move because the module system only
// sees modules. iam and the control plane now share one go.mod — the move
// that made `core/manager` the control plane kept iam inside it rather than
// promoting it to a fifteenth module — so nothing but this table stops an
// iam handler from importing a manager store.
//
// The third context, edgeagent, left this list when it moved to the edge
// module (decision 61): it is now `core/edge`, so the Go module system
// holds the boundary and this checker no longer needs a rule for it.
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
// boundedContext is one of the bounded contexts the root module's rules are
// written against.
type boundedContext struct {
	// label is the name used in violation messages ("iam", "manager").
	label string
	// dirs are the repository-relative directories the context owns. A
	// path belongs to the context when it starts with one of them.
	dirs []string
	// layerRoot is the directory whose service/, biz/ and data/
	// subdirectories the intra-context direction rule compares. It is not
	// always one of dirs, and the difference is real: the control plane
	// owns biz/ and service/ one level below core/manager, while
	// core/manager/pkg is the shared floor rather than part of it.
	layerRoot string
}

var bcs = []boundedContext{
	{
		label:     "iam",
		dirs:      []string{"core/manager/iam/"},
		layerRoot: "core/manager/iam/",
	},
	{
		// The control plane's context is the five layer directories, not
		// the whole manager tree. That distinction was implicit before
		// the move — the context was `internal/manager/` and the
		// infrastructure it runs on (`internal/pkg`, `internal/dataguard`,
		// `internal/control`, `internal/middleware`, ...) was its sibling,
		// so no context prefix matched them. Under core/manager they are
		// its children, and a context prefix of "core/manager/" would
		// swallow every one of them: the floor would read as business
		// code and iam's use of dataguard would read as a cross-context
		// import.
		label: "manager",
		dirs: []string{
			"core/manager/biz/",
			"core/manager/data/",
			"core/manager/model/",
			"core/manager/server/",
			"core/manager/service/",
		},
		// The layer rule compares "service/", "biz/" and "data/" under
		// this root, which is the manager module itself and not the
		// context's five directories — the layers sit one level below.
		layerRoot: "core/manager/",
	},
}

// sharedPkgs are the directories that make up the shared floor every
// bounded context may use: the manager's own pkg tree and the 2.0 floor
// module at core/floor. Both are business agnostic by rule, so nothing may
// be added to either that knows what a business is.
//
// `internal/pkg` used to be one of these. It is gone: its subpackages moved
// to core/manager/pkg with the rest of the control plane (decision 62),
// which is also when the walk was rewritten to derive its roots from this
// table instead of hardcoding `internal` — walking a path that no longer
// holds the bounded contexts is how a checker keeps reporting "all
// boundaries hold" while looking at nothing.
var sharedPkgs = []string{"core/manager/pkg/", "core/floor/"}

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

// bcWalkRoots returns every directory the bounded-context rules are checked
// over: each bounded context's own directory, plus every shared-floor
// directory. Deriving the list from the tables is the point — a hardcoded
// `internal` silently stopped covering the BCs once floor and the manager's
// packages became their own modules.
func bcWalkRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		p = strings.TrimSuffix(p, "/")
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		roots = append(roots, p)
	}
	for _, bc := range bcs {
		for _, dir := range bc.dirs {
			add(dir)
		}
	}
	for _, p := range sharedPkgs {
		add(p)
	}
	sort.Strings(roots)
	return roots
}

// checkAllBC runs checkBC over every walk root that exists.
//
// A root that does not exist is skipped, and the skip is deliberate: after
// the last move `internal` is gone, and demanding it would make the checker
// fail on a tree it has nothing to say about. What must not happen is a
// root disappearing from the list while still holding code — that is why
// the list is derived from the same tables the rules come from, and why
// TestTheWalkRootsCoverEveryBoundedContext pins it.
func checkAllBC(root string) ([]string, error) {
	var violations []string
	for _, dir := range bcWalkRoots() {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("bounded context root %s: %w", dir, err)
		}
		msgs, err := checkBC(root, dir)
		if err != nil {
			return nil, err
		}
		violations = append(violations, msgs...)
	}
	return violations, nil
}

// checkBCImport reports why imp is out of bounds for a file at rel, or "".
func checkBCImport(rel, imp string) string {
	if !strings.HasPrefix(imp, repoModule+"/") {
		return "" // another OpsKeeper module (core/*) or the standard library
	}
	rest := strings.TrimPrefix(imp, repoModule+"/")

	from := bcOf(rel)

	// A target that lives in the shared floor is not a bounded context, and
	// neither is a floor package. Everything depends on the floor, so the
	// floor is deliberately outside the graph these rules describe.
	//
	// Before the move this needed no code: `internal/pkg` matched no context
	// prefix, so bcOf returned "" on its own. core/manager/pkg does match a
	// context whose directories are derived from the tree it sits in, which
	// is why the exemption is now written out. Without it every use of
	// core/manager/pkg from within a context would read as a cross-context
	// import, and pkg/auth's use of pkg/tenantctx would read as the floor
	// knowing what a business is.
	owner := bcOf(rest)
	if inSharedFloor(rest) {
		owner = ""
	}

	// The shared floor must stay business agnostic. A package there that
	// imports a bounded context is the shortest path to every context
	// importing every other one, because everything depends on the floor.
	//
	// This direction is the one the floor rule still has to catch, and it
	// is checked before the owner test below because a floor file has no
	// context of its own to compare against.
	if inSharedFloor(rel) {
		if owner == "" {
			return ""
		}
		return fmt.Sprintf("%s: the shared floor imports %s (%s); the shared floor must not know what a business is",
			rel, imp, owner)
	}
	if owner == "" {
		return ""
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
	//
	// The comparison is against the context's *directory*
	// ("core/manager/iam/"), not its label ("iam"). rel is a file path and
	// rest is an import path, so a rule written against the label matches
	// neither — which is how this rule spent its first life comparing two
	// strings that could not be equal. TestABizPackageMayNotReachItsOwnDataLayer
	// is the regression test.
	dir := bcDir(from)
	if dir == "" {
		return ""
	}
	// A test is allowed to wire the concrete layer it tests: a biz test
	// that installs a real store is an integration test, and the same
	// scoping is already used for the cross-context exceptions below.
	// Production files get no such licence.
	if strings.HasSuffix(rel, "_test.go") {
		return ""
	}
	if reason, ok := layerDebt[rel]; ok {
		_ = reason
		return ""
	}
	switch {
	case strings.HasPrefix(rel, dir+"service/") && strings.HasPrefix(rest, dir+"data/"):
		return fmt.Sprintf("%s: %s service imports %s data; the service layer must go through biz", rel, from, from)
	case strings.HasPrefix(rel, dir+"biz/") && strings.HasPrefix(rest, dir+"data/"):
		return fmt.Sprintf("%s: %s biz imports %s data; a use case's own types belong in model, or the interface cannot name its results",
			rel, from, from)
	}
	return ""
}

// layerDebt is the intra-context debt that the service -> biz <- data rule
// finds today, listed file by file so the boundary can be enforced from now
// on without a refactor of the audit chain landing in the same change.
//
// These are debts, not decisions. Nothing in this repository ever argued
// that a biz package should hold a concrete store; the rule that forbids it
// simply never ran. It compared a repository-relative file path and an
// import path against a context *label* ("iam"), which neither of them
// starts with, so every file below was invisible to it. The comparison is
// fixed and the ledger is what remains: an entry disappears the moment the
// store type moves behind an interface the biz layer declares, and
// TestTheLayerDebtLedgerIsCurrent refuses an entry that has gone stale or a
// file that no longer exists.
var layerDebt = map[string]string{
	"core/manager/iam/biz/sso/service.go":                    "holds *ssostore.OrgSSOConfigStore directly; the store belongs behind a biz-declared interface",
	"core/manager/biz/aiops/proposal/expiry.go":              "holds *store.MutatingProposalRepo and builds store.ProposalAuditEntry values; both are repository types",
	"core/manager/biz/audit/chain.go":                        "seals rows with store.Head / store.Seal, the persistence layer's own types",
	"core/manager/biz/audit/usecase.go":                      "same edge: the audit use case names the store it persists through",
	"core/manager/biz/edge/changeevent/usecase.go":           "names edgestore.ChangeEventRepoIface, an interface that is declared in data rather than in biz",
	"core/manager/biz/loop/contractloader/contractloader.go": "is an adapter over *loopstore.ContractRepoDB; its whole job is the edge it is on, but the type should be an interface it declares",
}

// bcDir returns the directory whose service/, biz/ and data/ subdirectories
// the layer rule compares, from a context label ("iam" -> "core/manager/iam/",
// "manager" -> "core/manager/"), or "" for a label nothing declares.
//
// The three forms in play are not interchangeable: rel is a repository file
// path, rest is an import path, and the label is a name in this file. Every
// path the layer rules compare is a repository path, so they are written
// against directories rather than names.
func bcDir(label string) string {
	for _, bc := range bcs {
		if bc.label == label {
			return bc.layerRoot
		}
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
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/audit":         "iam handlers emit audit rows through the manager's single audit throat (decision 35)",
	"github.com/vincent-wuhan/opskeeper/core/manager/model/audit":       "same edge, the audit row type the handlers construct",
	"github.com/vincent-wuhan/opskeeper/core/manager/server/middleware": "same edge, the request id the handlers stamp rows with",

	// manager's IM bridge reads iam's model types. A model is the one
	// thing two contexts are meant to agree on; a bridge that copied the
	// type would own a second version of it.
	"github.com/vincent-wuhan/opskeeper/core/manager/iam/model": "manager's IM bridge reads iam's identity model types",

	// Cross-plane tests no longer need an exception to import the node
	// module: `core/edge` is not a bounded context inside the root module,
	// so importing it from a manager test is an ordinary cross-module
	// import and `checkBCImport` lets it through. That is the shape the
	// move bought — the boundary that used to be enforced by an
	// allow-list is now enforced by the module graph.
}

// inSharedFloor reports whether a repository-relative file path lives in
// one of the shared floor directories. It takes a *file* path, not an
// import path, because the rule it serves is about the files inside the
// floor and not about who may import them.
func inSharedFloor(rel string) bool {
	for _, p := range sharedPkgs {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// bcOf returns the bounded context an import path (or a file path) belongs
// to, or "" when it belongs to none.
func bcOf(path string) string {
	for _, bc := range bcs {
		for _, dir := range bc.dirs {
			if strings.HasPrefix(path, dir) {
				return bc.label
			}
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

// ── cross-module imports that are allowed from tests only ───────────────

// testOnlyImports records the cross-module edges that exist because a test
// has to wire two planes together, and that would be an architecture
// violation in production code.
//
// The rule exists because "not a real dependency" is exactly the kind of
// claim that stops being true quietly. `core/manager` is documented as not
// depending on `core/edge` — the control plane tells the node plane what to
// do over the tunnel, it never links it — but three test files under
// core/manager import the node plane's policy gate and command policy to
// drive a real gate across a loopback tunnel. That is the boundary being
// exercised rather than crossed, and Go has no test-only require, so the
// go.mod edge is simply there and nothing would notice the day a
// non-test file joins it.
//
// This is that notice. It is a rule rather than a comment because the
// comment was already written and did not stop anything.
var testOnlyImports = []struct {
	// Dir is the importing module root, repository-relative.
	Dir string
	// Prefix is the import prefix that module may only reach from
	// _test.go files.
	Prefix string
	// Why is printed with a violation so the reader gets the intent, not
	// just the file name.
	Why string
}{
	{
		Dir:    "core/manager",
		Prefix: "github.com/vincent-wuhan/opskeeper/core/edge/",
		Why: "the control plane must not link the node plane; the manager's " +
			"cross-plane end-to-end tests drive a real policy gate over a " +
			"loopback tunnel, which is why the edge appears in manager's " +
			"go.mod at all, and it may only ever appear in a _test.go file",
	},
}

// checkTestOnlyImports reports every non-test file that reaches an import
// prefix its module is only allowed to reach from tests.
func checkTestOnlyImports(root string) ([]string, error) {
	var violations []string
	for _, r := range testOnlyImports {
		base := filepath.Join(root, filepath.FromSlash(r.Dir))
		if _, err := os.Stat(base); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("test-only import rule %s: %w", r.Dir, err)
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				base := info.Name()
				if base == "testdata" || strings.HasPrefix(base, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for _, imp := range importsOf(path) {
				if !strings.HasPrefix(imp, r.Prefix) {
					continue
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				violations = append(violations, fmt.Sprintf(
					"%s: a non-test file imports %q, which %s may reach only from _test.go files; %s",
					filepath.ToSlash(rel), imp, r.Dir, r.Why))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(violations)
	return violations, nil
}

// floorIsolation are directory subtrees that may not reach an import prefix
// at all — in any file, test or not.
//
// This exists because of a specific refactor that would otherwise have been
// one good decision away from silently undoing itself. core/manager/pkg is
// the shared floor: the LLM port, the provider table, the router, the budget
// hook, every context imports it. Three files in its llm subpackage reached
// the PiG adapter, which meant a PiG upgrade edited the floor and, through
// it, every bounded context. Those files now live in core/manager/llmpig,
// which imports the floor and is named in exactly one arch-lint component.
//
// The move is the fix; this is the part that keeps it. A refactor that only
// relocates a problem leaves the next person free to walk it back with a
// three-line import, and the "just reuse the settings adapter" suggestion is
// exactly the kind that sounds like a simplification.
var floorIsolation = []struct {
	// Dir is the directory subtree, repository-relative, with a trailing
	// slash. Every .go file beneath it is checked.
	Dir string
	// Prefix is the import prefix that subtree may not use.
	Prefix string
	// Why is printed with a violation.
	Why string
}{
	{
		Dir:    "core/manager/pkg/",
		Prefix: coreModulePrefix + "/pig",
		Why: "the shared floor is the control plane's business-agnostic base " +
			"and every bounded context imports it, so a type it names is a " +
			"type every context sees; reaching the PiG adapter from here " +
			"turns a PiG upgrade into an edit to the floor and out to all of " +
			"them, which is the one thing core/pig exists to prevent " +
			"(decisions 57 and 62). The package that binds the two is " +
			"core/manager/llmpig: it imports the floor, never the reverse",
	},
	{
		Dir:    "core/floor/",
		Prefix: coreModulePrefix + "/pig",
		Why: "core/floor is the infrastructure both planes share, and the " +
			"node plane reaches it through the same import path the " +
			"control plane does; an adapter type here would put PiG in front " +
			"of the node plane as well (decision 60)",
	},
}

// checkFloorIsolation reports every file under an isolated subtree that
// reaches a forbidden prefix.
func checkFloorIsolation(root string) ([]string, error) {
	var violations []string
	for _, r := range floorIsolation {
		base := filepath.Join(root, filepath.FromSlash(r.Dir))
		if _, err := os.Stat(base); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("floor isolation rule %s: %w", r.Dir, err)
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				// A nested module is walked under its own root and is
				// not the floor's to judge.
				if path != base {
					if rel, relErr := filepath.Rel(root, path); relErr == nil {
						if moduleRoots()[filepath.ToSlash(filepath.Clean(rel))] {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			for _, imp := range importsOf(path) {
				if !strings.HasPrefix(imp, r.Prefix) {
					continue
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				violations = append(violations, fmt.Sprintf(
					"%s: %s may not import %q; %s",
					filepath.ToSlash(rel), r.Dir, imp, r.Why))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(violations)
	return violations, nil
}

// moduleRoots returns the set of declared module roots, slash-cleaned and
// repository-relative. It is the same table check() walks; a subtree walk
// needs it to know where the floor stops and a module of its own begins.
func moduleRoots() map[string]bool {
	roots := make(map[string]bool)
	for _, r := range rules() {
		roots[filepath.ToSlash(filepath.Clean(r.Dir))] = true
	}
	return roots
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
	// The bounded-context rules run over the BC directories and the shared
	// floor directories. The roots are derived from those tables rather
	// than hardcoded: the previous version always walked `internal`, which
	// stopped covering anything the day the BCs moved elsewhere, and a
	// checker that walks a path with no BCs in it reports "all boundaries
	// hold" for a tree it never looked at.
	bcViolations, err := checkAllBC(root)
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

	// The cross-module edges that are allowed only from tests get the same
	// repo-wide treatment: they are about a module boundary, so the module
	// rule table's prefix check would let them through.
	testOnlyViolations, err := checkTestOnlyImports(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulecheck: "+err.Error())
		os.Exit(2)
	}
	violations = append(violations, testOnlyViolations...)

	// The shared floor's isolation is a statement about a directory, not
	// about a module, so it gets the same repo-wide treatment.
	floorViolations, err := checkFloorIsolation(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulecheck: "+err.Error())
		os.Exit(2)
	}
	violations = append(violations, floorViolations...)

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
		if r.AnyVendor && !strings.HasPrefix(imp, repoModule+"/") {
			return ""
		}
		return fmt.Sprintf("imports %q; %s may depend only on the standard library", imp, r.Label)
	}
	for _, a := range r.Allowed {
		if imp == strings.TrimSuffix(a, "/") || strings.HasPrefix(imp, a) {
			return ""
		}
	}
	// A third-party import is not what an allowance of this kind is about:
	// the rule states which OpsKeeper module may reach which, and every
	// OpsKeeper import above has already been checked against the list.
	if r.AnyVendor && !strings.HasPrefix(imp, repoModule+"/") {
		return ""
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
