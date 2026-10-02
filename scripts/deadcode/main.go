// Command deadcode reports production code that only its own tests reach.
//
// domaincheck answers "which bounded contexts depend on which". It works
// at package granularity, and decision 116 found what that granularity
// cannot see: a 569-line migration window inside data/hitl/store that no
// production caller had ever invoked, while 3,869 tests passed. The tests
// were good tests of code nothing ran.
//
// This tool looks one level below a package. A file is reported when every
// symbol it declares — functions, methods, types, vars, consts — is
// unreachable from any non-test file in the tree. The line count that comes
// out is the size of the "wire it up or delete it" backlog, and it is the
// number stage 3 needs before deciding whether the remaining volume is
// split or cut.
//
// It reports and exits 0. That is deliberate, and the reasons are in
// falsePositives below: a name-based reachability walk cannot be made
// sound, because interface satisfaction, reflection, cgo and go:linkname
// all reach code without ever naming it. A gate built on it would train
// people to add escape hatches, and an escape hatch to a deadness checker
// is a comment that says "trust me".
//
// Usage:
//
//	go run ./scripts/deadcode [dir ...]
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// knownContracts are method names the standard library, the runtime and
// the language reach without a visible call site. A method with one of
// these names is never reported even if nothing in the tree names it,
// because "nothing calls MarshalJSON" is not a statement about whether
// json.Marshal will call it.
var knownContracts = map[string]bool{
	"Error": true, "String": true, "Read": true, "Write": true,
	"Close": true, "Len": true, "Less": true, "Swap": true,
	"Scan": true, "Value": true, "MarshalJSON": true, "UnmarshalJSON": true,
	"MarshalYAML": true, "UnmarshalYAML": true, "MarshalText": true,
	"UnmarshalText": true, "GobEncode": true, "GobDecode": true,
	"ServeHTTP": true, "Reset": true, "Equal": true, "Is": true,
	"Format": true, "Greet": true,
}

// falsePositives is the honest list of what this walk cannot see. It is
// printed with every run so that nobody treats the number as a proof.
//
//  1. Reflection. reflect.Value.MethodByName("X") and a template calling a
//     method reach code by string. Grep for the string before believing a
//     method is dead.
//  2. go:linkname and //go:linkname, plus assembly stubs.
//  3. cgo: an exported //export-ed symbol is called from C.
//  4. Struct tags that name a codec: json:"-" is invisible, and so is a
//     yaml tag that a driver looks up by name.
//  5. A method promoted by embedding: the outer type's method set is the
//     union, and nothing in this tree names the promoted name.
//  6. Build tags. A file excluded by the current GOOS/GOARCH is still
//     parsed here, so its symbols are counted as live. That errs toward
//     reporting less, which is the right direction for a report.
const falsePositives = `reflection · go:linkname · cgo //export · struct-tag codecs ·
embedded-method promotion · build tags`

// decl is one symbol a file declares.
type decl struct {
	name     string
	receiver string // "" for a top-level symbol
	pos      token.Position
}

// fileRecord is one parsed .go file.
type fileRecord struct {
	path  string
	test  bool
	decls []decl
	// refs counts, per name, how many times this file names it somewhere
	// that is not a declaration.
	refs map[string]int
	// lines is the file's line count, used to size the report.
	lines int
}

func main() {
	dirs := os.Args[1:]
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	records, err := parseAll(dirs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "deadcode:", err)
		os.Exit(2)
	}
	report := analyse(records)
	report.print(os.Stdout)
	// Always 0. See the package comment.
	os.Exit(0)
}

// parseAll reads every .go file under dirs, skipping vendor, testdata,
// dot-directories and generated output.
func parseAll(dirs []string) ([]*fileRecord, error) {
	var out []*fileRecord
	// Callers pass overlapping roots (".", "core", "core/manager"), and
	// walking them separately visits the same file once per root. Dedupe on
	// the absolute path or every finding is reported N times.
	seen := map[string]bool{}
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				switch info.Name() {
				case "vendor", "testdata", ".git", "node_modules":
					return filepath.SkipDir
				}
				if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			abs, aerr := filepath.Abs(path)
			if aerr != nil {
				return aerr
			}
			if seen[abs] {
				return nil
			}
			seen[abs] = true
			rec, err := parseFile(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			out = append(out, rec)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func parseFile(path string) (*fileRecord, error) {
	fset := token.NewFileSet()
	src, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rec := &fileRecord{
		path: filepath.ToSlash(path),
		test: strings.HasSuffix(path, "_test.go"),
		refs: map[string]int{},
	}
	rec.lines = fset.Position(src.End()).Line

	// Pass 1: declarations. Their own names are not references.
	for _, d := range src.Decls {
		switch n := d.(type) {
		case *ast.FuncDecl:
			if n.Recv != nil && len(n.Recv.List) > 0 {
				rec.decls = append(rec.decls, decl{
					name: n.Name.Name, receiver: receiverName(n), pos: fset.Position(n.Name.Pos()),
				})
			} else {
				rec.decls = append(rec.decls, decl{
					name: n.Name.Name, pos: fset.Position(n.Name.Pos()),
				})
			}
		case *ast.GenDecl:
			for _, spec := range n.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					rec.decls = append(rec.decls, decl{name: s.Name.Name, pos: fset.Position(s.Name.Pos())})
				case *ast.ValueSpec:
					for _, nm := range s.Names {
						rec.decls = append(rec.decls, decl{name: nm.Name, pos: fset.Position(nm.Pos())})
					}
				}
			}
		}
	}

	// Pass 2: every identifier that is not a declaration site. Doc comments
	// are not parsed as idents, so a symbol mentioned only in prose does
	// not count as used — which is the whole point: a name in a comment is
	// a claim, not a call.
	ast.Inspect(src, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && !isDeclarationIdent(src, id) {
			rec.refs[id.Name]++
		}
		return true
	})
	return rec, nil
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	var b strings.Builder
	ast.Inspect(fn.Recv.List[0].Type, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			b.WriteString(id.Name)
		}
		return true
	})
	return b.String()
}

// isDeclarationIdent reports whether id is the name a declaration binds,
// which is the one occurrence of that name in the file that is not a use.
func isDeclarationIdent(src *ast.File, id *ast.Ident) bool {
	for _, d := range src.Decls {
		switch n := d.(type) {
		case *ast.FuncDecl:
			if n.Name == id {
				return true
			}
		case *ast.GenDecl:
			for _, spec := range n.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name == id {
						return true
					}
				case *ast.ValueSpec:
					for _, nm := range s.Names {
						if nm == id {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// verdict is why a symbol is unreachable. The two tiers are kept apart
// because they call for different decisions: dead means nothing calls it,
// test-only means something does and it is a test, and a test for a
// function nothing calls is the shape decision 116 had to find by hand.
type verdict int

const (
	live verdict = iota
	dead
	testOnly
)

func (v verdict) String() string {
	switch v {
	case dead:
		return "dead"
	case testOnly:
		return "test-only"
	}
	return "live"
}

// result is the analysis over a parsed tree.
type result struct {
	// methodNames is every method name any interface in the tree declares.
	methodNames map[string]bool
	// file report
	files []*fileFinding
	// deadSymbols counts every unreachable symbol, whole file or not.
	deadSymbols int
	// byTier splits those symbols by why they are unreachable.
	byTier map[verdict]int
	// unreachableFiles / unreachableLines count only files where every
	// declared symbol is unreachable, because only then is the line count
	// a claim about how much could go.
	unreachableFiles int
	unreachableLines int
	deadFiles        int
	testOnlyFiles    int
}

// fileFinding is one file with at least one unreachable symbol.
type fileFinding struct {
	path     string
	lines    int
	findings []symbolFinding
	// unreachable is true when every symbol the file declares is
	// unreachable, which is the only case where the file's line count is
	// meaningful as "this many lines could go".
	unreachable bool
}

type symbolFinding struct {
	name string
	why  verdict
}

func analyse(records []*fileRecord) *result {
	res := &result{methodNames: map[string]bool{}, byTier: map[verdict]int{}}

	// An interface method is reached by assignment, never by name. Collect
	// them all first so a satisfying method is never called dead.
	for _, rec := range records {
		if rec.test {
			continue
		}
		collectInterfaceMethods(rec, res.methodNames)
	}

	// name -> set of non-test files that mention it.
	prodMentions := map[string]map[string]bool{}
	// name -> set of test files that mention it.
	testMentions := map[string]map[string]bool{}
	note := func(into map[string]map[string]bool, path string, names map[string]int) {
		for name := range names {
			if into[name] == nil {
				into[name] = map[string]bool{}
			}
			into[name][path] = true
		}
	}
	for _, rec := range records {
		if rec.test {
			note(testMentions, rec.path, rec.refs)
			continue
		}
		note(prodMentions, rec.path, rec.refs)
	}

	for _, rec := range records {
		if rec.test || len(rec.decls) == 0 {
			continue
		}
		var fs []symbolFinding
		for _, d := range rec.decls {
			why := classify(d, rec, prodMentions, testMentions, res.methodNames)
			if why == live {
				continue
			}
			fs = append(fs, symbolFinding{name: d.name, why: why})
		}
		if len(fs) == 0 {
			continue
		}
		f := &fileFinding{
			path:        rec.path,
			lines:       rec.lines,
			findings:    fs,
			unreachable: len(fs) == len(rec.decls),
		}
		if f.unreachable {
			res.unreachableFiles++
			res.unreachableLines += rec.lines
			switch worst(fs) {
			case dead:
				res.deadFiles++
			case testOnly:
				res.testOnlyFiles++
			}
		}
		res.deadSymbols += len(fs)
		res.byTier[worst(fs)] += len(fs)
		res.files = append(res.files, f)
	}
	sort.Slice(res.files, func(i, j int) bool {
		if res.files[i].unreachable != res.files[j].unreachable {
			return res.files[i].unreachable
		}
		if res.files[i].lines != res.files[j].lines {
			return res.files[i].lines > res.files[j].lines
		}
		return res.files[i].path < res.files[j].path
	})
	return res
}

// worst returns the more actionable of two verdicts: something nothing
// names at all is a stronger signal than something only a test names.
func worst(fs []symbolFinding) verdict {
	w := testOnly
	for _, f := range fs {
		if f.why == dead {
			return dead
		}
	}
	return w
}

func classify(
	d decl,
	rec *fileRecord,
	prodMentions, testMentions map[string]map[string]bool,
	ifaces map[string]bool,
) verdict {
	switch d.name {
	case "main", "init":
		// Reached by the runtime, never by a name.
		return live
	}
	if d.receiver != "" && (ifaces[d.name] || knownContracts[d.name]) {
		// Reached by being put in a variable of an interface type.
		return live
	}
	if knownContracts[d.name] {
		return live
	}
	for f := range prodMentions[d.name] {
		if f != rec.path {
			return live
		}
	}
	if rec.refs[d.name] > 0 {
		// Named inside its own file outside the declaration: a same-file
		// call is still a call.
		return live
	}
	for range testMentions[d.name] {
		return testOnly
	}
	return dead
}

func collectInterfaceMethods(rec *fileRecord, out map[string]bool) {
	fset := token.NewFileSet()
	src, err := parser.ParseFile(fset, rec.path, nil, 0)
	if err != nil {
		return
	}
	ast.Inspect(src, func(n ast.Node) bool {
		it, ok := n.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range it.Methods.List {
			for _, name := range m.Names {
				out[name.Name] = true
			}
		}
		return true
	})
}

func (r *result) print(w *os.File) {
	fmt.Fprintf(w, "deadcode: %d symbols unreachable from production code\n", r.deadSymbols)
	fmt.Fprintf(w, "deadcode: %d whole files / %d lines are unreachable (%d files name nothing at all, %d are named only by tests)\n",
		r.unreachableFiles, r.unreachableLines, r.deadFiles, r.testOnlyFiles)
	for _, f := range r.files {
		kind := "partial"
		if f.unreachable {
			kind = worst(f.findings).String()
		}
		parts := make([]string, 0, len(f.findings))
		for _, s := range f.findings {
			parts = append(parts, s.name+":"+s.why.String())
		}
		fmt.Fprintf(w, "  %-10s %6d lines  %-62s %s\n",
			kind, f.lines, f.path, strings.Join(parts, " "))
	}
	fmt.Fprintf(w, "deadcode: this walk cannot see %s\n", falsePositives)
	fmt.Fprintln(w, "deadcode: report only, exit 0 — see the package comment before turning this into a gate")
}
