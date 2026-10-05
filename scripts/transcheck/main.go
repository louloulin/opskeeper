// Command transcheck reports hand-written struct translations and the columns
// they leave behind.
//
// The ledger has named this measurement as the one it cannot take. A
// translation is a composite literal of a type from another package whose
// fields are copied one by one from a value of a struct here: `return
// svc.RuleInput{ RuleKey: in.RuleKey, ... }`. It compiles, it looks reviewed,
// and the day somebody adds a column to the source struct the translation
// keeps compiling and ships the new column as a zero value. The shape that
// makes it invisible is that the omission is not an error anywhere — nothing
// in Go says a struct literal must mention every field, and nothing in this
// repository's gates could see it until decision 259 wrote the guard by hand
// for one call site.
//
// So this is the generalisation of that guard: find the sites, resolve the
// source struct where the receiver is nameable, and print the columns the
// literal does not set. One site is already guarded by hand
// (cmd/opskeeper/alert_rule_wiring.go); the point of this command is that the
// other 120-odd are not guarded at all.
//
// It reports and exits 0, and it is deliberately not a gate. The first two
// sites it resolved by hand were both false positives, and both for a reason
// that is a property of the code rather than of the analysis:
//
//   - a value passed as a separate argument, not as a field. IssueAgentTeamsToken
//     does not copy TTLSeconds into the claims, and must not: the signer takes
//     the TTL as its second parameter. A name-based reading calls that a
//     dropped column.
//   - a renamed column. hitl serialises Payload into PayloadJSON, so the
//     literal sets a field whose name differs from the source's. Same verdict,
//     wrong reason.
//
// Two more classes are structural rather than accidental, and they account for
// most of the rest of what the first run flagged:
//
//   - narrowing. A literal built inside a type switch copies the fields of the
//     branch's own case out of a wider struct, so the other cases' fields are
//     "unset" by construction. Three such sites in service/aiops alone.
//   - flattening. A nested source struct is spread across several flat
//     destination columns — autonomyAuditRow turns Row.Trigger into the wire's
//     Kind/Metric/Threshold triple, and the reason is written in the function's
//     own comment: a nested struct on the wire would be a second place for the
//     trigger vocabulary to drift.
//
// All four are printed on every run, so a reader meets them before the numbers
// rather than after. A gate that reports two of two wrong teaches people to
// write "trust me" next to it, and the ledger has a rule about exactly that.
// The classes are also what an extension has to handle: each one is a shape
// the analysis can learn to see, and until it does, every flag is a place to
// look rather than a defect to fix.
//
// What it cannot see, in full:
//
//  1. Renames and derivations, as above. The receiver's column appears under
//     another name, or is serialised, or is computed in a helper call.
//  2. Values that travel as extra parameters to the enclosing call rather
//     than as fields of the literal.
//  3. The receiver is usually not a parameter. It is a local, a struct field,
//     or the result of another call, and resolving those needs go/types and a
//     per-module load. The resolution rate is printed: on this tree it is a
//     small fraction of the sites, and the sites it misses are NOT reported
//     as clean — they are absent, which is the opposite of a clean bill.
//  4. Only direct struct literals. A translation spread over a builder, or
//     assembled field by field across statements, is invisible.
//  5. A struct literal that sets every field by hand still looks like a
//     translation when the values happen to be selectors, which is why the
//     ratio and the minimum width are thresholds and not definitions.
//
// Usage:
//
//	go run ./scripts/transcheck [dir ...]
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const limitations = `renamed or derived columns · values passed as extra parameters · ` +
	`receivers that are not parameters (see the resolution rate) · builder-style ` +
	`assembly · thresholds rather than definitions`

// knownFalsePositives is the pair of misses this command's own first reading
// produced on this tree. They are printed on every run for the reason given in
// the package comment: a reader has to meet them before the numbers, or the
// numbers teach the wrong lesson.
var knownFalsePositives = []struct {
	kind    string
	where   string
	because string
}{
	{
		"extra argument",
		"iam/biz/user/usecase.go: IssueAgentTeamsToken",
		"TTLSeconds travels as SignAgentTeamsService's second parameter, not as a claim field",
	},
	{
		"rename",
		"hitl/agentteams.go: CreateAgentTeams",
		"Payload arrives as PayloadJSON, a renamed and serialised column",
	},
	{
		"narrowing",
		"service/aiops/service.go (3 sites)",
		"a literal inside a type switch copies the branch's own case out of a wider struct",
	},
	{
		"flattening",
		"cmd/opskeeper-edge/autonomy.go: autonomyAuditRow",
		"Row.Trigger is spread across the wire's Kind/Metric/Threshold triple, on purpose",
	},
}

// minFields is the width below which a literal is not treated as a
// translation. A three-field literal is as likely to be a constructor call
// site or a test fixture as a copy, and the report is only useful while the
// sites in it are worth reading.
const minFields = 5

// projectionRatio is the share of a literal's fields that must be plain
// selector expressions before the literal counts as a copy rather than a
// declaration. A DTO definition assigns literals, calls and conversions; a
// translation assigns `other.Field`.
const projectionRatio = 0.7

// goFile is one parsed production file with the import aliases it declares.
type goFile struct {
	dir     string
	path    string
	aliases map[string]string // alias -> import directory
	// selfDir is what a bare type name resolves to.
	selfDir string
}

// typeIndex maps a directory and a type name to that struct's field names.
type typeIndex map[string]map[string][]string

// sites is the report, kept as a named type so it can carry the printer.
type sites []*site

// site is one candidate translation.
type site struct {
	file string
	fn   string
	// dest is the type being constructed, as written.
	dest string
	// source is the struct the columns come from, when it resolved.
	source string
	// carried is every field the literal sets.
	carried int
	// sourceFields is how many fields the source struct has.
	sourceFields int
	// dropped is what the literal does not set, by name.
	dropped []string
	// resolved says whether source was established at all.
	resolved bool
}

func main() {
	dirs := os.Args[1:]
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	files, index, err := scan(dirs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "transcheck:", err)
		os.Exit(2)
	}
	analyse(files, index).print(os.Stdout)
	// Always 0. See the package comment.
	os.Exit(0)
}

func scan(dirs []string) ([]*goFile, typeIndex, error) {
	var files []*goFile
	index := typeIndex{}
	fset := token.NewFileSet()
	for _, root := range dirs {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if skipDir(info.Name(), path) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			// The directory is keyed relative to the scan root, because that
			// is the form an import path can be turned back into: the module
			// prefix is stripped to a repository-relative path, and a
			// fixture's own prefix is matched by its trailing segments. Keying
			// on the absolute path works in the repository and breaks in every
			// fixture, which is the wrong way round.
			rel, relErr := filepath.Rel(root, filepath.Dir(path))
			if relErr != nil {
				rel = filepath.Dir(path)
			}
			rel = filepath.ToSlash(rel)
			f := &goFile{dir: rel, path: path, aliases: map[string]string{}, selfDir: rel}
			for _, imp := range parsed.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				alias := ""
				if imp.Name != nil {
					alias = imp.Name.Name
				} else {
					alias = p[strings.LastIndex(p, "/")+1:]
				}
				f.aliases[alias] = p
			}
			for _, decl := range parsed.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					if index[f.dir] == nil {
						index[f.dir] = map[string][]string{}
					}
					index[f.dir][ts.Name.Name] = fieldNames(st)
				}
			}
			files = append(files, f)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, index, nil
}

func skipDir(name, path string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "__pycache__":
		return true
	}
	// The tools analyse the tree; they are not part of it.
	return strings.HasPrefix(filepath.ToSlash(path), "scripts/")
}

func dirOf(path string) string {
	return filepath.ToSlash(filepath.Dir(path))
}

func fieldNames(st *ast.StructType) []string {
	var out []string
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.IsExported() {
				out = append(out, n.Name)
			}
		}
	}
	return out
}

func analyse(files []*goFile, index typeIndex) sites {
	var out sites
	for _, f := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), f.path, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			params := paramTypes(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if s, ok := translationAt(lit, f, params, index); ok {
					s.file = f.path
					s.fn = fd.Name.Name
					out = append(out, &s)
				}
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].dropped) != len(out[j].dropped) {
			return len(out[i].dropped) > len(out[j].dropped)
		}
		if out[i].sourceFields != out[j].sourceFields {
			return out[i].sourceFields > out[j].sourceFields
		}
		return out[i].file < out[j].file
	})
	return out
}

// translationAt decides whether lit is a copy of one struct into another
// package's type, and resolves the source when the receiver is a parameter.
func translationAt(lit *ast.CompositeLit, f *goFile, params map[string]string, index typeIndex) (site, bool) {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return site{}, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return site{}, false
	}
	if _, isImport := f.aliases[pkg.Name]; !isImport {
		return site{}, false
	}
	carried, projections, receivers := readLiteral(lit)
	if len(carried) < minFields {
		return site{}, false
	}
	if float64(projections)/float64(len(carried)) < projectionRatio {
		return site{}, false
	}
	s := site{dest: pkg.Name + "." + sel.Sel.Name, carried: len(carried)}
	set := map[string]bool{}
	for _, c := range carried {
		set[c] = true
	}
	// The receiver is the identifier most of the projections hang off. A
	// literal that mixes several receivers is still a copy, but the source
	// is then not one struct and resolving it would be a guess.
	if len(receivers) != 1 {
		return s, true
	}
	var recv string
	for r := range receivers {
		recv = r
	}
	expr, ok := params[recv]
	if !ok {
		return s, true
	}
	name := expr
	candidates := []string{f.selfDir}
	if i := strings.LastIndex(expr, "."); i >= 0 {
		alias := expr[:i]
		d, isImport := f.aliases[alias]
		if !isImport {
			return s, true
		}
		name = expr[i+1:]
		candidates = dirCandidates(d)
	}
	fields, ok := lookup(index, candidates, name)
	if !ok {
		return s, true
	}
	s.resolved = true
	s.source = name
	s.sourceFields = len(fields)
	for _, name := range fields {
		if !set[name] {
			s.dropped = append(s.dropped, name)
		}
	}
	return s, true
}

// readLiteral returns the field names a composite literal sets, the count of
// those whose value is a plain selector, and the identifier those selectors
// hang off.
func readLiteral(lit *ast.CompositeLit) (carried []string, projections int, receivers map[string]bool) {
	receivers = map[string]bool{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		carried = append(carried, key.Name)
		proj, ok := kv.Value.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		root, ok := proj.X.(*ast.Ident)
		if !ok {
			// in.Rule.Conditions and friends: the root is still the
			// receiver, two hops out.
			if mid, ok := proj.X.(*ast.SelectorExpr); ok {
				if r2, ok := mid.X.(*ast.Ident); ok {
					projections++
					receivers[r2.Name] = true
					continue
				}
			}
			continue
		}
		projections++
		receivers[root.Name] = true
	}
	return carried, projections, receivers
}

// paramTypes maps each parameter name to the type expression as written.
func paramTypes(fd *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	if fd.Type.Params == nil {
		return out
	}
	for _, f := range fd.Type.Params.List {
		expr := typeString(f.Type)
		if len(f.Names) == 0 {
			continue
		}
		for _, n := range f.Names {
			out[n.Name] = expr
		}
	}
	return out
}

func typeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.ArrayType:
		return "[]" + typeString(t.Elt)
	}
	return fmt.Sprintf("%T", e)
}

// dirCandidates turns an import path into the directories the scan may have
// indexed it under, longest first.
//
// The repository's own module path is stripped to what follows the module
// prefix, which is what the walk keys on. A fixture or a vendored tree names
// something else, so the trailing segments are tried as well rather than
// failing the whole resolution: the report is a reading, and a reading that
// gives up on an unusual prefix reports less than the tree contains.
func dirCandidates(importPath string) []string {
	const prefix = "github.com/vincent-wuhan/opskeeper/"
	var out []string
	if i := strings.Index(importPath, prefix); i >= 0 {
		out = append(out, importPath[i+len(prefix):])
	}
	parts := strings.Split(importPath, "/")
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[i:], "/"))
	}
	if len(out) == 0 {
		out = append(out, importPath)
	}
	return out
}

func lookup(index typeIndex, dirs []string, name string) ([]string, bool) {
	for _, d := range dirs {
		if fields, ok := index[d][name]; ok {
			return fields, true
		}
	}
	return nil, false
}

func (all sites) print(w io.Writer) {
	fmt.Fprintln(w, "hand-written struct translations, and the columns they do not set")
	fmt.Fprintln(w, "  a translation is a composite literal of another package's type whose fields are")
	fmt.Fprintln(w, "  copied one by one from a struct here. adding a column to the source leaves the")
	fmt.Fprintln(w, "  literal compiling, and the new column ships as a zero value.")
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "  %d site(s) with at least %d fields and at least %.0f%% plain projections\n",
		len(all), minFields, projectionRatio*100)

	resolved, flagged := 0, 0
	for _, s := range all {
		if s.resolved {
			resolved++
		}
		if len(s.dropped) > 0 {
			flagged++
		}
	}
	fmt.Fprintf(w, "  %d resolved the source struct, %d of them leave at least one column unset\n", resolved, flagged)
	if resolved < len(all) {
		fmt.Fprintf(w, "  %d did NOT resolve. they are ABSENT from the list below, which is not the\n", len(all)-resolved)
		fmt.Fprintln(w, "  same as being clean: a site this command cannot read is a site nobody is checking.")
	}
	fmt.Fprintln(w, "")

	// The four known misses are printed here, above the sites, because the
	// first version printed them at the end while the comment claimed the
	// reader met them first. A tool whose prose about itself is wrong is the
	// same failure as a gate whose verdict is wrong, and this one was easier
	// to catch only because the claim was written down.
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  four kinds of flag below that are not defects, each one established by reading")
	fmt.Fprintln(w, "  the site rather than by the analysis:")
	for _, k := range knownFalsePositives {
		fmt.Fprintf(w, "    - %-16s %s: %s\n", k.kind, k.where, k.because)
	}
	fmt.Fprintln(w, "  a report whose first answers were all wrong is not a gate. it is a list of")
	fmt.Fprintln(w, "  places to look, and the four lines above are the reason to look sceptically.")

	for _, s := range all {
		if !s.resolved {
			continue
		}
		head := fmt.Sprintf("  %2d/%2d columns  %-34s <- %-22s %s", s.sourceFields-len(s.dropped), s.sourceFields, s.dest, s.source, s.file)
		if len(s.dropped) == 0 {
			fmt.Fprintln(w, head+"  (all set)")
			continue
		}
		fmt.Fprintln(w, head)
		fmt.Fprintf(w, "          unset: %s\n", strings.Join(s.dropped, ", "))
	}
	if resolved == 0 {
		fmt.Fprintln(w, "  (no site resolved its source struct — the run measured nothing)")
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  what this command cannot see, and what it gets wrong:")
	for i, l := range strings.Split(limitations, " · ") {
		fmt.Fprintf(w, "  %d. %s\n", i+1, l)
	}
}
