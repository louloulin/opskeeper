package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"sort"
	"strings"
)

// The edge report prices every declared edge by what its consumer actually
// selects from its producer, and it is the first report here that looks at
// method calls rather than only at named symbols.
//
// Why that matters is a hole three earlier reports walked into. The parsed
// `used` map records the symbols a file names *through an import*: a type, a
// constant, a function. It records nothing about `e.edges.List(ctx, ...)`,
// because there the package is named once, in a struct field's type, and every
// later use is a selector on a value. A measurement built on `used` alone
// therefore reports a domain that calls four methods as a domain that calls
// none — and it did exactly that, twice in a row, before this file existed
// (decision 235).
//
// The fix is not a better regular expression. A pattern keyed on the field's
// name misses the moment one domain calls it `edges` and another calls it
// `EdgeUC`, which is precisely the pair that produced a confident zero. So the
// method layer here is resolved from the other end: every exported method the
// target domain declares, then every call to that name in the consumer's
// files. The field's name never enters the question.
//
// Two honest limits, both printed in the header rather than left for the reader
// to discover:
//
//   - A call is attributed to the edge when the calling file imports the
//     target domain. That is necessary and not sufficient: `x.List(` can be a
//     call on something else entirely that happens to share a name. Every hit
//     is printed with its file so it can be checked, and the column is labelled
//     an upper bound because it is one.
//   - Only exported methods are considered, since nothing else is reachable
//     across a package boundary. An edge carried entirely by unexported
//     methods cannot exist in Go, so nothing is lost, but an interface method
//     consumed through an embedded type will be attributed to the interface's
//     own package only if that package declares it.
//
// Unlike the regex measurements it replaces, this reads the tree, so a call
// inside a comment is not a call. That is not a nicety: the commented-out
// `// edges, _ := h.edges.List(` in server/webshell is what made a
// grep-based count report webshell as a consumer of edge when it has not been
// one since that line was commented out.

// methodDecl is one exported method declared in a domain.
type methodDecl struct {
	recv string
	pkg  string
	file string
}

// collectMethods indexes every exported method declared in a domain by its
// name, so a call site can be resolved without knowing what the value it is
// called on was named.
func collectMethods(sources []source) map[string]map[string][]methodDecl {
	out := map[string]map[string][]methodDecl{}
	for _, src := range sources {
		if src.test || src.file == nil {
			continue
		}
		d := domainOf(src.path)
		if d == "" {
			continue
		}
		if out[d] == nil {
			out[d] = map[string][]methodDecl{}
		}
		for _, decl := range src.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 || !fd.Name.IsExported() {
				continue
			}
			out[d][fd.Name.Name] = append(out[d][fd.Name.Name], methodDecl{
				recv: recvTypeName(fd.Recv.List[0].Type),
				pkg:  pkgKey(src),
				file: src.path,
			})
		}
	}
	return out
}

// recvTypeName renders a receiver's base type name: `*Edge` and `Edge[T]` are
// both `Edge`. The generic parameter list is dropped because a dependent names
// the type without it.
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	}
	return "?"
}

// edgeCost is one declared edge, priced.
type edgeCost struct {
	from, to string
	// reason is the declared justification. It is carried because an edge
	// that selects nothing and has a reason reads as a stale declaration,
	// while an edge that selects nothing and has no reason reads as a
	// measurement bug — and telling those two apart is the reader's job,
	// not the report's.
	reason string
	// types are the symbols the consumer names through an import of the
	// producer's domain, with the interface flag resolved.
	types []string
	// ifc counts how many of those are interfaces, because a consumer
	// already holding an interface is holding something that could be
	// re-pointed without moving anything.
	ifc int
	// methods are the producer's exported method names called from the
	// consumer's files, with the file each was seen in.
	methods []string
	// methodSites maps a method name to the consumer file it was seen in.
	methodSites map[string]string
}

func (e edgeCost) symbols() int { return len(e.types) + len(e.methods) }

// printEdges writes every declared edge with what the consumer selects from the
// producer, cheapest first. It is a report, not a gate: the ordering is the
// deliverable, and the question it exists to answer is "which of the remaining
// edges is small enough to cut today".
func printEdges(w io.Writer, sources []source, r rules) {
	methods := collectMethods(sources)
	// The field lists and the interface flags are tree-wide facts, so they
	// are read once. Recomputing them per edge would walk every file once
	// per declared edge, which for forty edges is a minute of work to
	// print a table nobody reads twice.
	fields := collectStructFields(sources)
	kind := collectDeclKinds(sources)

	var costs []edgeCost
	for e, reason := range r.edges {
		if r.shared[e.to] != "" {
			continue
		}
		c := edgeCost{from: e.from, to: e.to, reason: reason, methodSites: map[string]string{}}
		seenType := map[string]bool{}
		for _, src := range sources {
			if src.test {
				continue
			}
			if domainOf(src.path) != e.from {
				continue
			}
			for imp, syms := range src.used {
				if domainOf(imp) != e.to {
					continue
				}
				for sym := range syms {
					if seenType[sym] {
						continue
					}
					seenType[sym] = true
					c.types = append(c.types, sym)
					if kind[imp] != nil && kind[imp][sym] == kindInterface {
						c.ifc++
					}
				}
			}
			// The method layer. Restricted to files that import the
			// producer, because a call on an unrelated value that happens
			// to share a method name is not this edge.
			imports := false
			for _, imp := range src.imports {
				if domainOf(imp) == e.to {
					imports = true
					break
				}
			}
			if !imports {
				continue
			}
			ast.Inspect(src.file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if _, ok := methods[e.to][sel.Sel.Name]; !ok {
					return true
				}
				if _, seen := c.methodSites[sel.Sel.Name]; !seen {
					c.methods = append(c.methods, sel.Sel.Name)
					c.methodSites[sel.Sel.Name] = src.path
				}
				return true
			})
		}
		sort.Strings(c.types)
		sort.Strings(c.methods)
		costs = append(costs, c)
	}
	sort.Slice(costs, func(i, j int) bool {
		if costs[i].symbols() != costs[j].symbols() {
			return costs[i].symbols() < costs[j].symbols()
		}
		if costs[i].from != costs[j].from {
			return costs[i].from < costs[j].from
		}
		return costs[i].to < costs[j].to
	})

	fmt.Fprintln(w, "declared edges, priced by what the consumer selects from the producer")
	fmt.Fprintln(w, "  cheapest first: an edge that selects one or two symbols is a")
	fmt.Fprintln(w, "  candidate for a port, and an edge that selects a wide struct plus a")
	fmt.Fprintln(w, "  spread of methods is a relocation, not a port.")
	fmt.Fprintln(w, "  cols: syms = distinct named types + called methods; ifc = how many of")
	fmt.Fprintln(w, "  the named types are interfaces already; * = the producer declares more")
	fmt.Fprintln(w, "  than one package with that name.")
	fmt.Fprintln(w, "  methods are resolved from the producer's exported method set, not")
	fmt.Fprintln(w, "  from the field name, so a renamed field cannot hide a call. They are")
	fmt.Fprintln(w, "  attributed to files that import the producer, which makes the column an")
	fmt.Fprintln(w, "  UPPER BOUND: a same-named call on an unrelated value would be counted.")
	fmt.Fprintln(w, "  Each hit names its file so it can be checked.")
	fmt.Fprintln(w)
	if len(costs) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	typeOnly, methodOnly, both := 0, 0, 0
	for _, c := range costs {
		switch {
		case len(c.types) == 0 && len(c.methods) == 0:
			// Declared but nothing selected: the edge is drawn by a
			// reason in the table, not by code. It is printed because a
			// declared edge nothing uses is a different problem from an
			// expensive one.
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  (declared, nothing selected: %s)\n", c.symbols(), c.from, c.to, c.reason)
		case len(c.types) == 0:
			methodOnly++
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  %d method(s), no type\n", c.symbols(), c.from, c.to, len(c.methods))
		case len(c.methods) == 0:
			typeOnly++
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  %d type(s), no method\n", c.symbols(), c.from, c.to, len(c.types))
		default:
			both++
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  %d type(s) (%d ifc) + %d method(s)\n",
				c.symbols(), c.from, c.to, len(c.types), c.ifc, len(c.methods))
		}
		for _, t := range c.types {
			mark := ""
			if shape := shapeOf(t, c.to, fields, kind); shape != "" {
				mark = "  " + shape
			}
			if dup := dupOwners(t, c.to, kind); len(dup) > 1 {
				mark += fmt.Sprintf("  <-- also declared in %s", strings.Join(dup, " "))
			}
			fmt.Fprintf(w, "        type   %-24s%s\n", t, mark)
		}
		for _, m := range c.methods {
			recv := "?"
			if d, ok := methods[c.to][m]; ok && len(d) > 0 {
				recv = d[0].recv
			}
			fmt.Fprintf(w, "        method %-24s on %-14s %s\n", m, recv, c.methodSites[m])
		}
	}
	fmt.Fprintf(w, "\n  %d edges: %d carry types only, %d carry methods only, %d carry both\n",
		len(costs), typeOnly, methodOnly, both)
}

// shapeOf renders a selected struct's field list, or says why it cannot.
func shapeOf(sym, to string, fields map[string]map[string][]string, kind map[string]map[string]declKind) string {
	pkgs := packagesOf(to, fields)
	for _, p := range pkgs {
		if f := fields[p][sym]; len(f) > 0 {
			return fmt.Sprintf("{%s}", strings.Join(f, " "))
		}
		if kind[p] != nil {
			if _, ok := kind[p][sym]; ok {
				return "not a struct (interface, alias or constant)"
			}
		}
	}
	return ""
}

// dupOwners lists the producer packages that declare a symbol, when there is
// more than one. A name with two owners is the case decision 233 wrote a whole
// section about, so the report says so instead of printing one of them.
func dupOwners(sym, to string, kind map[string]map[string]declKind) []string {
	var out []string
	for _, p := range packagesOf(to, kind) {
		if kind[p] != nil {
			if _, ok := kind[p][sym]; ok {
				out = append(out, strings.TrimPrefix(p, managerPrefix+"/"))
			}
		}
	}
	sort.Strings(out)
	return out
}

// packagesOf lists the import paths belonging to a domain. pkgKey already
// returns an import path in the same namespace src.path uses, so the domain of
// a package key is read with the same function every other path goes through
// rather than by re-deriving it from the prefix.
func packagesOf[V any](to string, keys map[string]map[string]V) []string {
	var out []string
	for p := range keys {
		if domainOf(p) == to {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// collectStructFields indexes every struct's field list by package and name, so
// a selected type can be printed with the shape a mover would have to carry
// instead of just its name.
func collectStructFields(sources []source) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for _, src := range sources {
		if src.test || src.file == nil {
			continue
		}
		pkg := pkgKey(src)
		if out[pkg] == nil {
			out[pkg] = map[string][]string{}
		}
		for _, decl := range src.file.Decls {
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
				if !ok || st.Fields == nil {
					continue
				}
				var names []string
				for _, f := range st.Fields.List {
					for _, id := range f.Names {
						names = append(names, id.Name)
					}
				}
				out[pkg][ts.Name.Name] = names
			}
		}
	}
	return out
}

// collectDeclKinds folds the per-file declared maps into per-package ones, so
// a name declared in a sibling file of the same package is still found.
func collectDeclKinds(sources []source) map[string]map[string]declKind {
	out := map[string]map[string]declKind{}
	for _, src := range sources {
		if src.declared == nil {
			continue
		}
		p := pkgKey(src)
		if out[p] == nil {
			out[p] = map[string]declKind{}
		}
		for name, k := range src.declared {
			out[p][name] = k
		}
	}
	return out
}
