package main

import (
	"fmt"
	"go/ast"
	"io"
	"sort"
	"strings"
)

// The seam report exists because of a classification error, not because
// something was missing.
//
// Decision 228 sorted the remaining cross-domain edges by "what kind of symbol
// does the consumer select" and concluded that everything left was a data shape
// that had to be moved, GORM entities included. Decision 230 then cut
// `flow -> scheduler` — a single interface, implemented in the *consuming*
// domain, whose only weight was one `var _ schedulerbiz.Repo = (*Repo)(nil)`.
// The list had filed it under "data shape, expensive" because the table it was
// built from could not tell an `interface` from a `struct` by name alone.
//
// So this file does what the hand table could not: it reads each edge's actual
// selectors out of the parsed tree and, for the interfaces among them, finds
// which domain holds a compile-time assertion of implementation. The
// discriminator is not "is it cheap to move" — that is a judgement — but "is
// the thing on the other side of this edge a contract or a value", which is a
// fact about the source and can be checked.
//
// The three shapes it reports:
//
//	port-opposite — the consumer selects only interfaces, AND the implementor
//	                is in the consumer's own domain. The port is named on the
//	                far side of the boundary from its only implementation,
//	                which is a naming bug: moving the interface to a shared
//	                floor lets both halves see it and the edge disappears.
//	                Decisions 227 and 230 are both this.
//	port-here     — the consumer selects only interfaces and the implementor is
//	                in the declaring domain. That is an ordinary substitutable
//	                boundary and cutting it would be a real design change.
//	mixed / data  — anything else. A struct on the far side has to move with
//	                its table, its foreign keys, and every other domain that
//	                already stores one. This is the expensive bucket.
type seamRow struct {
	from, to string
	// ifaces and others are the selected symbols, split by kind.
	ifaces, others []string
	// implementor is the domain holding a `var _ pkg.Iface = ...` assertion
	// for one of ifaces, when one was found.
	implementor string
	// weight is the number of import statements behind the edge.
	weight int
}

func (r seamRow) verdict() string {
	if len(r.ifaces) == 0 {
		return "data"
	}
	if len(r.others) > 0 {
		return "mixed"
	}
	if r.implementor == "" {
		return "port-here?"
	}
	if r.implementor == r.from {
		return "port-opposite"
	}
	return "port-here"
}

// printSeams writes the per-edge classification. It is a report, not a gate:
// the tree is allowed to contain any of these verdicts, and the point is to
// show which edges are worth a knife before anyone spends one.
func printSeams(w io.Writer, sources []source, r rules) {
	declared := map[string]map[string]declKind{}
	for _, src := range sources {
		if src.declared != nil {
			declared[pkgKey(src)] = src.declared
		}
	}

	// assertions[importPath][symbol] = the domain that asserts it implements
	// the interface. Only `var _ pkg.Iface = ...` counts: that is the form
	// this tree uses everywhere, and it is the only one that is checked by
	// the compiler, so it is the only one that can be read as fact rather
	// than as a guess about intent.
	assertions := map[string]map[string]string{}
	for _, src := range sources {
		if src.test || src.file == nil {
			continue
		}
		alias := importAliases(src.file)
		ast.Inspect(src.file, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				return true
			}
			if vs.Names[0].Name != "_" {
				return true
			}
			sel, ok := vs.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgID, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, ok := alias[pkgID.Name]
			if !ok {
				return true
			}
			if assertions[path] == nil {
				assertions[path] = map[string]string{}
			}
			assertions[path][sel.Sel.Name] = domainOf(src.path)
			return true
		})
	}

	rows := map[edge]*seamRow{}
	for _, src := range sources {
		if src.test {
			continue
		}
		from := domainOf(src.path)
		if from == "" {
			continue
		}
		for imp, syms := range src.used {
			to := domainOf(imp)
			if to == "" || to == from || r.shared[to] != "" {
				continue
			}
			e := edge{from, to}
			row := rows[e]
			if row == nil {
				row = &seamRow{from: from, to: to}
				rows[e] = row
			}
			row.weight++
			for sym := range syms {
				if declared[imp][sym] == kindInterface {
					row.ifaces = append(row.ifaces, sym)
					if who := assertions[imp][sym]; who != "" && row.implementor == "" {
						row.implementor = who
					}
				} else {
					row.others = append(row.others, sym)
				}
			}
		}
	}

	// The import statement count is on the graph, not on `used` — a file that
	// imports a package without selecting from it is still a dependency the
	// build has to honour. Reuse the graph's own weight so the two reports
	// can never disagree.
	g := buildGraph(sources, r)
	for e := range g.weight {
		if row := rows[e]; row != nil {
			row.weight = g.weight[e]
		}
	}

	keys := make([]edge, 0, len(rows))
	for e := range rows {
		keys = append(keys, e)
	}
	sort.Slice(keys, func(i, j int) bool {
		if rows[keys[i]].verdict() != rows[keys[j]].verdict() {
			return rows[keys[i]].verdict() < rows[keys[j]].verdict()
		}
		if rows[keys[i]].weight != rows[keys[j]].weight {
			return rows[keys[i]].weight > rows[keys[j]].weight
		}
		return keys[i].to < keys[j].to
	})

	fmt.Fprintln(w, "seams: what each cross-domain edge actually carries")
	fmt.Fprintln(w, "  port-opposite  the consumer selects only interfaces and holds the")
	fmt.Fprintln(w, "                implementation itself — the port is named on the far")
	fmt.Fprintln(w, "                side of the boundary from its only implementor. Moving")
	fmt.Fprintln(w, "                it to a shared floor removes the edge (decisions 227, 230).")
	fmt.Fprintln(w, "  port-here     interfaces only, implemented where they are declared. An")
	fmt.Fprintln(w, "                ordinary substitutable boundary; cutting it is a design change.")
	fmt.Fprintln(w, "  mixed         interfaces alongside values. The values have to move too.")
	fmt.Fprintln(w, "  data          no interface at all. Structs, entities, tables.")
	fmt.Fprintln(w)
	for _, e := range keys {
		row := rows[e]
		fmt.Fprintf(w, "  %-13s %3d  %-14s -> %-14s", row.verdict(), row.weight, row.from, row.to)
		if row.implementor != "" {
			fmt.Fprintf(w, " impl-in=%s", row.implementor)
		}
		fmt.Fprintln(w)
		if len(row.ifaces) > 0 {
			fmt.Fprintf(w, "                 iface: %s\n", strings.Join(dedupe(row.ifaces), ", "))
		}
		if len(row.others) > 0 {
			fmt.Fprintf(w, "                 value: %s\n", strings.Join(dedupe(sortedCopy(row.others)), ", "))
		}
	}
}

// importAliases maps the identifier a file refers to an imported package by
// to the import path. An import with no explicit name is referred to by the
// last path segment, which is what the compiler does and therefore what the
// assertions in this tree use.
func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		if name == "" {
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
		}
		out[name] = path
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
