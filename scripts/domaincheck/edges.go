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

// closureHit is one type a cut drags along behind a type the consumer named.
type closureHit struct {
	// via is the field path that reaches it, e.g. "Report.Kind". A closure
	// printed without its path is a list of names, and a list of names is
	// not something anybody can check against the code.
	via string
	// name is the type as it was written, qualifier included. The qualifier
	// is the whole point: it is what makes a drag across a domain visible
	// instead of looking like another field of the same struct.
	name string
	// home is the domain that declares it, or "" when nothing in the
	// control plane does — a type from a module below the control plane
	// does not move when an edge is cut, so it is not a cost.
	home string
}

// builtinTypeNames are the predeclared identifiers. They are excluded from
// every closure walk because a struct field typed `string` is not code
// anybody has to carry to core/domain.
var builtinTypeNames = map[string]bool{
	"bool": true, "string": true, "int": true, "int8": true, "int16": true,
	"int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true,
	"uint32": true, "uint64": true, "uintptr": true, "byte": true, "rune": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
	"error": true, "any": true,
}

// fieldRef is one struct field, with the type it was declared as rather than
// the name it was given.
//
// The name is what collectStructFields already indexes, and it is exactly the
// thing that cannot answer this question: the closure is reached through
// `Kind chatruntime.ContainerKind`, and neither half of that line is the
// field's name.
type fieldRef struct {
	field string
	typ   string
	// qual is the package qualifier when the type was written as
	// `pkg.Name`, empty when it was a bare identifier.
	qual string
}

// collectFieldTypes indexes every struct field by package, struct and field
// name, keeping the declared type alongside the declared name.
func collectFieldTypes(sources []source) map[string]map[string][]fieldRef {
	out := map[string]map[string][]fieldRef{}
	for _, src := range sources {
		if src.test || src.file == nil {
			continue
		}
		pkg := pkgKey(src)
		if out[pkg] == nil {
			out[pkg] = map[string][]fieldRef{}
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
				for _, f := range st.Fields.List {
					typ, qual := baseTypeOf(f.Type)
					if typ == "" {
						continue
					}
					for _, id := range f.Names {
						// An unexported field is not part of this edge.
						// A consumer in another package cannot read it, so
						// cutting the edge does not move it, and counting
						// it made `audit.Usecase` look like it dragged
						// nineteen domains behind a four-field struct —
						// every one of them reached through `repo` and
						// `log`. The rest of this file already ignores
						// unexported names for exactly this reason, and a
						// closure that counted them was measuring a
						// different question from the one it is printed
						// under.
						if !id.IsExported() {
							continue
						}
						out[pkg][ts.Name.Name] = append(out[pkg][ts.Name.Name], fieldRef{
							field: id.Name, typ: typ, qual: qual,
						})
					}
				}
			}
		}
	}
	return out
}

// baseTypeOf renders the named type a field's type expression bottoms out in,
// unwrapping the pointers, slices, maps and type parameters that sit between
// the field and the type, and reporting the qualifier when the type was
// written as `pkg.Name`.
//
// It returns "" for a type expression with no name in it — an inline struct,
// an inline interface, a function type — because there is nothing to carry.
// That is also the honest limit of this walk and the report prints it: a type
// reached only through an inline composite literal is invisible here.
func baseTypeOf(expr ast.Expr) (typ, qual string) {
	switch t := expr.(type) {
	case *ast.Ident:
		if builtinTypeNames[t.Name] {
			return "", ""
		}
		return t.Name, ""
	case *ast.StarExpr:
		return baseTypeOf(t.X)
	case *ast.ArrayType:
		return baseTypeOf(t.Elt)
	case *ast.MapType:
		// Both halves, because map[V]Key and map[Key]V are both legal and
		// which one names the interesting type is not something this
		// function can know. Deduplication happens in the walk.
		if typ, qual = baseTypeOf(t.Key); typ != "" {
			return typ, qual
		}
		return baseTypeOf(t.Value)
	case *ast.SelectorExpr:
		name := t.Sel.Name
		if id, ok := t.X.(*ast.Ident); ok {
			return name, id.Name
		}
		return name, ""
	case *ast.IndexExpr:
		return baseTypeOf(t.X)
	case *ast.IndexListExpr:
		return baseTypeOf(t.X)
	case *ast.ChanType:
		return baseTypeOf(t.Value)
	case *ast.ParenExpr:
		return baseTypeOf(t.X)
	}
	return "", ""
}

// nameHomes indexes, for every declared name in the control plane, the domains
// that declare it.
//
// A name with two homes is deliberately not resolved to one of them. Choosing
// between them would be a guess, and decision 233 is a whole section about a
// guess of exactly this shape being reported as a fact.
func nameHomes(kind map[string]map[string]declKind) map[string][]string {
	out := map[string][]string{}
	for pkg, names := range kind {
		d := domainOf(pkg)
		if d == "" {
			continue
		}
		for name := range names {
			out[name] = append(out[name], d)
		}
	}
	for name, ds := range out {
		sort.Strings(ds)
		out[name] = dedupeStrings(ds)
	}
	return out
}

func dedupeStrings(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || in[i-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// closureOf walks from the types a consumer names into the types those types
// reach through their fields, and returns everything a cut would have to
// carry along with them.
//
// Three rules, each of which is a decision rather than a default:
//
//   - Only fields are followed. A type reached solely through a method
//     signature is not counted, and the report says so, because a method
//     signature is a different shape of dependency from a field and mixing
//     them would make the column mean two things.
//   - A hit whose home is a *different* domain is recorded and not walked
//     further. That is the drag: it means cutting this edge does not stay
//     inside this edge.
//   - A hit nothing declares is dropped. It is a type from a module below
//     the control plane or from a third-party module, and cutting an edge
//     does not move it, so counting it would inflate the price with
//     something nobody has to carry.
func closureOf(selected []string, to string, ftypes map[string]map[string][]fieldRef, homes map[string][]string) []closureHit {
	// The producer's own fields, folded across its packages: a domain is
	// reached through one package or several and the closure is the same
	// walk either way.
	prod := map[string][]fieldRef{}
	for pkg, byType := range ftypes {
		if domainOf(pkg) != to {
			continue
		}
		for name, refs := range byType {
			prod[name] = append(prod[name], refs...)
		}
	}
	seen := map[string]bool{}
	for _, s := range selected {
		seen[s] = true
	}
	var out []closureHit
	var walk func(name, path string, depth int)
	walk = func(name, path string, depth int) {
		// Bounded because a struct cannot contain itself by value, but two
		// types can name each other through a pointer and an unbounded
		// walk over that is a hang rather than an answer.
		if depth > 8 {
			return
		}
		for _, ref := range prod[name] {
			if seen[ref.typ] {
				continue
			}
			seen[ref.typ] = true
			hs := homes[ref.typ]
			if len(hs) == 0 {
				// Declared nowhere in the control plane: it is not a
				// control-plane type and cutting the edge does not move it.
				continue
			}
			disp := ref.typ
			if ref.qual != "" {
				disp = ref.qual + "." + ref.typ
			}
			via := path + "." + ref.field
			out = append(out, closureHit{via: via, name: disp, home: strings.Join(hs, "|")})
			if len(hs) == 1 && hs[0] == to {
				walk(ref.typ, via, depth+1)
			}
		}
	}
	for _, s := range selected {
		walk(s, s, 0)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].via != out[j].via {
			return out[i].via < out[j].via
		}
		return out[i].name < out[j].name
	})
	return out
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
	// closure are the types a cut has to carry even though the consumer
	// never names them, because it reaches them through a field of one it
	// does name. See closureOf for why the price below is not the number of
	// symbols the consumer wrote.
	closure []closureHit
}

func (e edgeCost) symbols() int { return len(e.types) + len(e.methods) }

// price is what cutting this edge actually costs: the symbols the consumer
// names, plus the ones it would have to carry behind them.
//
// It is the sort key rather than symbols() because symbols() is the number
// that decided the marketplace -> pluginimport edge was "two symbols" when
// moving it means moving six, two of them out of a third domain. A ranking
// built on the direct count sends the next cut at the wrong edge, which is
// the one thing this report exists to prevent.
func (e edgeCost) price() int { return e.symbols() + len(e.closure) }

// foreignDomains lists the domains a cut reaches into besides the producer's,
// sorted and deduplicated.
//
// A name declared by two domains at once is reported as both, joined by a
// pipe, rather than resolved to one of them: the ambiguity is the answer when
// the question is "whose type is this", because picking a side is the guess
// decision 233 is about.
//
// The comparison is membership and not string inequality. `Decision` is
// declared in pluginimport and in three other domains, so printing it as
// "declared elsewhere" would be false in the direction that matters most — it
// would tell a reader the type has to come from somewhere it already is.
func (e edgeCost) foreignDomains() []string {
	var out []string
	for _, h := range e.closure {
		if e.ownsType(h.home) {
			continue
		}
		for _, d := range strings.Split(h.home, "|") {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return dedupeStrings(out)
}

// ownsType reports whether the producer's own domain is one of a name's
// declaring domains. A name with several owners is owned, and the row says so
// separately, because "ambiguous" and "somebody else's" are different
// amounts of work.
func (e edgeCost) ownsType(home string) bool {
	for _, d := range strings.Split(home, "|") {
		if d == e.to {
			return true
		}
	}
	return false
}

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
	ftypes := collectFieldTypes(sources)
	homes := nameHomes(kind)

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
		// The closure is computed after the walk rather than inside it,
		// because it starts from the *set* of selected types: a type two
		// selected structs both reach is one piece of work, not two, and
		// per-file computation would count it once per file that names it.
		c.closure = closureOf(c.types, e.to, ftypes, homes)
		costs = append(costs, c)
	}
	sort.Slice(costs, func(i, j int) bool {
		if costs[i].price() != costs[j].price() {
			return costs[i].price() < costs[j].price()
		}
		if costs[i].from != costs[j].from {
			return costs[i].from < costs[j].from
		}
		return costs[i].to < costs[j].to
	})

	fmt.Fprintln(w, "declared edges, priced by what cutting one would actually take")
	fmt.Fprintln(w, "  cheapest first: an edge that carries one or two types is a candidate")
	fmt.Fprintln(w, "  for a port, and an edge that carries a wide struct plus a spread of")
	fmt.Fprintln(w, "  methods is a relocation, not a port.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  cols: price = named types + called methods + closure; ifc = how many of")
	fmt.Fprintln(w, "  the named types are interfaces already; * = the producer declares more")
	fmt.Fprintln(w, "  than one package with that name.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  the closure is the part the consumer never writes down. Naming a type")
	fmt.Fprintln(w, "  whose field names another type means moving both, so price counts the")
	fmt.Fprintln(w, "  second one too. Counting only what was named is how an edge carrying")
	fmt.Fprintln(w, "  six types was reported as carrying two, and the ranking that follows")
	fmt.Fprintln(w, "  from that number sends the next cut at the wrong edge.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  methods are resolved from the producer's exported method set, not")
	fmt.Fprintln(w, "  from the field name, so a renamed field cannot hide a call. They are")
	fmt.Fprintln(w, "  attributed to files that import the producer, which makes the column an")
	fmt.Fprintln(w, "  UPPER BOUND: a same-named call on an unrelated value would be counted.")
	fmt.Fprintln(w, "  Each hit names its file so it can be checked.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  three limits of the closure, all of them lower bounds:")
	fmt.Fprintln(w, "    - it follows struct FIELDS only, so a type reachable only through a")
	fmt.Fprintln(w, "      method signature or an inline composite literal is not counted")
	fmt.Fprintln(w, "    - a hit declared by another domain is recorded and not walked into,")
	fmt.Fprintln(w, "      which is why a closure line naming a third domain means the cut")
	fmt.Fprintln(w, "      does not stay inside the edge it was priced for")
	fmt.Fprintln(w, "    - a type no domain declares is dropped, because it belongs to a module")
	fmt.Fprintln(w, "      below the control plane and cutting an edge does not move it")
	fmt.Fprintln(w, "    - only EXPORTED fields are followed, while the shape column below lists")
	fmt.Fprintln(w, "      every field, so an unexported one appears in a shape and never in a")
	fmt.Fprintln(w, "      price. That is not an inconsistency: a dependent in another package")
	fmt.Fprintln(w, "      cannot read an unexported field, so cutting the edge keeps it.")
	fmt.Fprintln(w)
	if len(costs) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	typeOnly, methodOnly, both, dragged := 0, 0, 0, 0
	for _, c := range costs {
		// The breakdown prints the direct count next to the price, because
		// the two disagreeing is the finding. A reader who sees only the
		// price cannot tell a two-symbol edge from a two-symbol edge that
		// drags four more, and those two want opposite decisions.
		extra := ""
		if n := len(c.closure); n > 0 {
			extra = fmt.Sprintf(" + %d closure", n)
			if homes := c.foreignDomains(); len(homes) > 0 {
				dragged++
				extra += fmt.Sprintf(" (reaches %s)", strings.Join(homes, " "))
			}
		}
		switch {
		case len(c.types) == 0 && len(c.methods) == 0:
			// Declared but nothing selected: the edge is drawn by a
			// reason in the table, not by code. It is printed because a
			// declared edge nothing uses is a different problem from an
			// expensive one.
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  (declared, nothing selected: %s)\n", c.price(), c.from, c.to, c.reason)
		case len(c.types) == 0:
			methodOnly++
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  %d method(s), no type%s\n", c.price(), c.from, c.to, len(c.methods), extra)
		case len(c.methods) == 0:
			typeOnly++
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  %d type(s), no method%s\n", c.price(), c.from, c.to, len(c.types), extra)
		default:
			both++
			fmt.Fprintf(w, "  %2d  %-18s -> %-16s  %d type(s) (%d ifc) + %d method(s)%s\n",
				c.price(), c.from, c.to, len(c.types), c.ifc, len(c.methods), extra)
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
		for _, h := range c.closure {
			mark := ""
			switch {
			case !c.ownsType(h.home):
				// The cross-domain case, named rather than summarised:
				// this is the line that says the cut is bigger than the
				// edge it was priced under.
				mark = fmt.Sprintf("  <-- lives in %s, not in %s", h.home, c.to)
			case strings.Contains(h.home, "|"):
				// Owned, but by four packages at once. The type still has
				// to move; what is unresolved is which of the four shapes
				// the mover is carrying.
				mark = fmt.Sprintf("  <-- %s declares it too, so the shape is not settled", h.home)
			}
			fmt.Fprintf(w, "        closure %-24s via %s%s\n", h.name, h.via, mark)
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
	if dragged > 0 {
		// Counted on purpose rather than left to the reader: "an edge that
		// reaches into a third domain" is a different kind of work from one
		// that does not, and the count is what tells a planner how many
		// there are.
		fmt.Fprintf(w, "  %d of them drag a type out of a domain other than the producer's,\n", dragged)
		fmt.Fprintf(w, "  so cutting those is not the single-edge job the price column implies.\n")
	}
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
