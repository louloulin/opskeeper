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
// Usage:
//
//	go run ./scripts/modulecheck [repo-root]
//
// Every violation found is printed, not just the first: a reviewer fixing
// boundaries wants the whole list in one pass.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// importsOf extracts import paths from a Go source file. This is a lexical
// scan rather than go/parser so the checker has no dependencies of its own
// and runs anywhere Go runs.
func importsOf(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, block := range importBlocks(string(data)) {
		out = append(out, parseQuoted(block)...)
	}
	return out
}

// importBlocks yields the text inside each `import (...)` group and each
// single-line `import "..."` statement.
func importBlocks(src string) []string {
	var blocks []string
	rest := src
	for {
		i := strings.Index(rest, "\nimport")
		if i < 0 {
			return blocks
		}
		rest = rest[i+len("\nimport"):]
		trimmed := strings.TrimLeft(rest, " \t")
		if strings.HasPrefix(trimmed, "(") {
			end := strings.IndexByte(trimmed, ')')
			if end < 0 {
				return blocks
			}
			blocks = append(blocks, trimmed[1:end])
			rest = trimmed[end+1:]
			continue
		}
		nl := strings.IndexByte(trimmed, '\n')
		if nl < 0 {
			return append(blocks, trimmed)
		}
		blocks = append(blocks, trimmed[:nl])
		rest = trimmed[nl+1:]
	}
}

// parseQuoted pulls every double-quoted string out of an import block,
// which correctly skips the surrounding syntax because import paths are
// the only quoted strings there.
func parseQuoted(block string) []string {
	var out []string
	rest := block
	for {
		i := strings.IndexByte(rest, '"')
		if i < 0 {
			return out
		}
		rest = rest[i+1:]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+1:]
	}
}
