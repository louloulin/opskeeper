package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// graph.go is the shape of the tree the boundary table is about.
//
// check() answers "is the declared shape true". It does not answer "what
// does the shape look like", and stage 3's second item — split the
// monolith into pieces that evolve independently — cannot be planned from a
// pass/fail. So this file prints the graph, and, more usefully, it can
// price a proposed split: hand it a grouping and it reports how many
// declared edges that grouping cuts, and which ones.
//
// It reuses domainOf rather than re-deriving domains, on purpose. A second
// implementation of the collapse rule is a second answer to "what is a
// domain", and the day the two disagree every number this file prints is
// about a tree that does not exist.

// domainGraph is the measured dependency structure.
type domainGraph struct {
	// weight counts the import statements behind an edge. An edge held up
	// by one import and an edge held up by twenty-five are not the same
	// seam, and a split that treats them alike is a guess.
	weight map[edge]int
	// domains is every domain name, including those with no cross-domain
	// edge at all, so the report can say "these four are islands" rather
	// than staying silent about them.
	domains map[string]bool
	// lines is how much code each domain owns, and pkgs is the same
	// measure one level down. They are the size axis, and they answer the
	// question the edge count cannot: a grouping that severs forty imports
	// and relocates three per cent of the tree is not a split, it is a
	// rename with a diagram. Test files are excluded, as everywhere else.
	lines map[string]int
	pkgs  map[string]pkgSize
}

// pkgSize is one package's share of the tree. domain is kept alongside it
// because the whole reason this axis exists is that the two do not agree:
// domainOf collapses biz/aiops/tools onto the domain "aiops", so the
// largest package in the tree can be invisible in the domain view.
type pkgSize struct {
	domain string
	lines  int
	files  int
}

type ranked struct {
	name     string
	in, out  int
	inEdges  int
	outEdges int
}

func buildGraph(sources []source, r rules) *domainGraph {
	g := &domainGraph{
		weight:  map[edge]int{},
		domains: map[string]bool{},
		lines:   map[string]int{},
		pkgs:    map[string]pkgSize{},
	}
	for _, src := range sources {
		from := domainOf(src.path)
		if from == "" {
			continue
		}
		g.domains[from] = true
		if src.test {
			continue
		}
		g.lines[from] += src.lines
		// A file sitting directly in the manager root has no package
		// directory to name it. Today there are none, and a blank row in
		// the report would be read as a rendering bug, so it is named.
		name := src.pkg
		if name == "" {
			name = "(manager root)"
		}
		p := g.pkgs[name]
		p.domain = from
		p.lines += src.lines
		p.files++
		g.pkgs[name] = p
		for _, imp := range src.imports {
			to := domainOf(imp)
			if to == "" || to == from {
				continue
			}
			g.domains[to] = true
			if r.shared[to] != "" {
				continue
			}
			g.weight[edge{from, to}]++
		}
	}
	return g
}

// printSize reports the other axis: not what depends on what, but how much
// code there is to move.
//
// The two axes disagree, and the disagreement is the finding. domainOf
// collapses the five layer trees onto one name, so biz/aiops, model/aiops
// and service/aiops are all "aiops". A domain of three hundred lines can
// therefore sit in the same column as a domain that is one enormous
// package, and no edge count can tell the two apart. Stage 3's second item
// is priced in lines: what a split relocates, not how many seams it cuts.
func (g *domainGraph) printSize(w io.Writer) {
	total := 0
	for _, n := range g.lines {
		total += n
	}
	if total == 0 {
		return
	}

	type sizedPkg struct {
		name string
		pkgSize
	}
	pkgs := make([]sizedPkg, 0, len(g.pkgs))
	for name, p := range g.pkgs {
		pkgs = append(pkgs, sizedPkg{name, p})
	}
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].lines != pkgs[j].lines {
			return pkgs[i].lines > pkgs[j].lines
		}
		return pkgs[i].name < pkgs[j].name
	})

	fmt.Fprintf(w, "\nwhere the code is (%d lines, tests excluded):\n", total)
	for i, p := range pkgs {
		if i >= 10 {
			fmt.Fprintf(w, "  %-34s ... and %d smaller packages\n", "", len(pkgs)-10)
			break
		}
		fmt.Fprintf(w, "  %-34s %6d  %5.1f%%  %2d files  (domain %s)\n",
			p.name, p.lines, 100*float64(p.lines)/float64(total), p.files, p.domain)
	}

	// A domain that is a single package is the case the domain view cannot
	// show: its name says "one concern", but there is nothing under it to
	// pull out, so a split that treats it as a peer of a wide domain is
	// pricing a rename as if it were a decomposition.
	type sizedDomain struct {
		name  string
		lines int
		pkgs  int
	}
	dom := map[string]sizedDomain{}
	for _, p := range g.pkgs {
		d := dom[p.domain]
		d.name = p.domain
		d.lines += p.lines
		d.pkgs++
		dom[p.domain] = d
	}
	ds := make([]sizedDomain, 0, len(dom))
	for _, d := range dom {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].lines != ds[j].lines {
			return ds[i].lines > ds[j].lines
		}
		return ds[i].name < ds[j].name
	})

	fmt.Fprintln(w, "\nthe same tree read as domains (the same code, the other axis):")
	for i, d := range ds {
		if i >= 8 {
			break
		}
		note := ""
		if d.pkgs == 1 {
			note = "  <- one package, nothing under it to split"
		}
		fmt.Fprintf(w, "  %-20s %6d  %5.1f%%  %2d packages%s\n",
			d.name, d.lines, 100*float64(d.lines)/float64(total), d.pkgs, note)
	}
	fmt.Fprintf(w, "\n  %d packages across %d domains. The largest package is %.1f%% of the tree\n",
		len(pkgs), len(ds), 100*float64(pkgs[0].lines)/float64(total))
	fmt.Fprintf(w, "  and the largest domain %.1f%%. A split is priced by the first number.\n",
		100*float64(ds[0].lines)/float64(total))
}

func (g *domainGraph) rank() []ranked {
	byName := map[string]*ranked{}
	for name := range g.domains {
		byName[name] = &ranked{name: name}
	}
	for e, w := range g.weight {
		byName[e.from].out += w
		byName[e.to].in += w
		byName[e.from].outEdges++
		byName[e.to].inEdges++
	}
	out := make([]ranked, 0, len(byName))
	for _, v := range byName {
		out = append(out, *v)
	}
	return out
}

// levels is the longest-path layering of the DAG: every domain sits one
// level below the deepest domain that depends on it.
//
// This is the number that bounds stage 3's second item. The tree is a DAG
// today (decision 118), so it has a layering, and a piece that evolves
// independently has to be able to sit somewhere in it. A domain at level
// 0 depends on nothing, which means nothing outside it can change without
// it noticing; a domain deep in the graph sits on top of most of the
// tree. Those are the domains that have to move first.
func (g *domainGraph) levels() map[string]int {
	// in-degree 0 nodes are level 0; peel.
	indeg := map[string]int{}
	adj := map[string][]string{}
	for name := range g.domains {
		indeg[name] = 0
	}
	for e := range g.weight {
		adj[e.from] = append(adj[e.from], e.to)
		indeg[e.to]++
	}
	level := map[string]int{}
	queue := []string{}
	for n, d := range indeg {
		if d == 0 {
			queue = append(queue, n)
		}
	}
	sort.Strings(queue)
	head := 0
	placed := 0
	for head < len(queue) {
		n := queue[head]
		head++
		placed++
		for _, m := range adj[n] {
			if level[m] < level[n]+1 {
				level[m] = level[n] + 1
			}
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	// A node that never reached indeg 0 would mean a cycle. Decision 118
	// removed the last one; if this ever fires, the fix is to look at the
	// cycle, not at this function.
	_ = placed
	for n := range g.domains {
		if _, ok := level[n]; !ok {
			level[n] = 0
		}
	}
	return level
}

func (g *domainGraph) printStructure(w io.Writer) {
	total := 0
	for _, v := range g.weight {
		total += v
	}
	fmt.Fprintf(w, "\ndomain graph: %d domains, %d edges, %d import statements behind them\n",
		len(g.domains), len(g.weight), total)

	rs := g.rank()
	fmt.Fprintln(w, "\nmost depended-on (in-degree = other domains would have to change with it):")
	sorted := append([]ranked(nil), rs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].in != sorted[j].in {
			return sorted[i].in > sorted[j].in
		}
		return sorted[i].name < sorted[j].name
	})
	for i, v := range sorted {
		if i >= 12 || v.in == 0 {
			break
		}
		fmt.Fprintf(w, "  %-16s in %3d across %2d edges   out %3d across %2d\n",
			v.name, v.in, v.inEdges, v.out, v.outEdges)
	}

	fmt.Fprintln(w, "\nmost dependent (out-degree = how much of the rest of the tree it names):")
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].out != sorted[j].out {
			return sorted[i].out > sorted[j].out
		}
		return sorted[i].name < sorted[j].name
	})
	for i, v := range sorted {
		if i >= 8 || v.out == 0 {
			break
		}
		fmt.Fprintf(w, "  %-16s out %3d across %2d edges   in %3d across %2d\n",
			v.name, v.out, v.outEdges, v.in, v.inEdges)
	}

	g.printSize(w)

	lv := g.levels()
	max := 0
	byLevel := map[int][]string{}
	for n, l := range lv {
		byLevel[l] = append(byLevel[l], n)
		if l > max {
			max = l
		}
	}
	fmt.Fprintf(w, "\nlongest-path layering: %d levels (level 0 depends on nothing)\n", max+1)
	for l := 0; l <= max; l++ {
		names := byLevel[l]
		sort.Strings(names)
		if len(names) > 10 {
			names = names[:10]
		}
		fmt.Fprintf(w, "  L%d (%2d): %s\n", l, len(byLevel[l]), strings.Join(names, " "))
	}

	fmt.Fprintln(w, "\nentangled pairs (imports in both directions count together — after decision 118 there are none):")
	type pair struct {
		a, b  string
		total int
	}
	var pairs []pair
	seen := map[string]bool{}
	for e, weight := range g.weight {
		key := e.from + "|" + e.to
		rev := e.to + "|" + e.from
		if seen[key] || seen[rev] {
			continue
		}
		seen[key] = true
		back := 0
		if r, ok := g.weight[edge{e.to, e.from}]; ok {
			back = r
		}
		if back > 0 {
			pairs = append(pairs, pair{e.from, e.to, weight + back})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].total > pairs[j].total })
	if len(pairs) == 0 {
		fmt.Fprintln(w, "  (none — the graph is a DAG)")
	}
	for _, p := range pairs {
		fmt.Fprintf(w, "  %-16s <-> %-16s %d imports\n", p.a, p.b, p.total)
	}
}

// loadGrouping reads a proposed split: one "group = a, b, c" per line.
// It returns the group of each domain, and the groups themselves.
//
// A line ending in a comma continues onto the next one. Real proposals have
// 38 domain names in a group and a hard-wrapped 80-column file is the only
// way to keep the comments next to the names, so refusing to read a wrapped
// group would just push people back to the one version nobody can review.
// The comma makes the continuation explicit: no line is joined that the
// author did not ask to join.
func loadGrouping(path string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	group := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	// A proposal groups 55 domain names; the default 64K line cap is not the
	// binding constraint but the buffer is sized so a wrapped group never
	// trips it.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	pending := ""
	pendingAt := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			// A comment inside a wrapped group ends nothing: the comma on
			// the line above already said the list continues.
			continue
		}
		more := strings.HasSuffix(text, ",")
		if more {
			text = strings.TrimSpace(strings.TrimSuffix(text, ","))
		}
		switch {
		case pending == "":
			pending, pendingAt = text, line
		default:
			// The comma that asked for the continuation is the separator
			// between the two halves. Replacing it with a space would join
			// the last name on one line to the first on the next into a
			// single name that exists in no tree, and the price that comes
			// back would be confidently wrong.
			pending += ", " + text
		}
		if more {
			continue
		}
		if err := addGroup(path, pendingAt, pending, group, &order); err != nil {
			return nil, nil, err
		}
		pending = ""
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if pending != "" {
		return nil, nil, fmt.Errorf("%s:%d: the group ends with a comma and nothing follows it", path, pendingAt)
	}
	if len(order) == 0 {
		return nil, nil, fmt.Errorf("%s: no groups", path)
	}
	return group, order, nil
}

// addGroup records one "group = a, b, c" line. A domain in two groups is an
// error rather than a last-one-wins: the price of a proposal depends on
// which side a domain landed, and quietly picking one hides the mistake that
// made the price wrong.
func addGroup(path string, line int, text string, group map[string]string, order *[]string) error {
	name, members, ok := strings.Cut(text, "=")
	if !ok {
		return fmt.Errorf("%s:%d: want `group = a, b, c`", path, line)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%s:%d: empty group name", path, line)
	}
	*order = append(*order, name)
	for _, m := range strings.Split(members, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if prev, dup := group[m]; dup {
			return fmt.Errorf("%s:%d: %s is in both %s and %s", path, line, m, prev, name)
		}
		group[m] = name
	}
	return nil
}

// printCut prices a proposed split: how many edges it severs, weighted by
// the imports behind them, and which domains the grouping forgets.
func (g *domainGraph) printCut(w io.Writer, grouping map[string]string, order []string) {
	// A domain nobody assigned is the thing most likely to be wrong with
	// the proposal, so it is reported first and loudly.
	var unassigned []string
	for d := range g.domains {
		if grouping[d] == "" {
			unassigned = append(unassigned, d)
		}
	}
	sort.Strings(unassigned)
	var unknown []string
	known := map[string]bool{}
	for d := range g.domains {
		known[d] = true
	}
	for d := range grouping {
		if !known[d] {
			unknown = append(unknown, d)
		}
	}
	sort.Strings(unknown)

	type cutEdge struct {
		from, to string
		w        int
	}
	var cuts []cutEdge
	internal, crossing := 0, 0
	for e, w := range g.weight {
		gf, gto := grouping[e.from], grouping[e.to]
		if gf == "" || gto == "" {
			continue
		}
		if gf == gto {
			internal += w
			continue
		}
		crossing += w
		cuts = append(cuts, cutEdge{e.from, e.to, w})
	}
	sort.Slice(cuts, func(i, j int) bool {
		if cuts[i].w != cuts[j].w {
			return cuts[i].w > cuts[j].w
		}
		if cuts[i].from != cuts[j].from {
			return cuts[i].from < cuts[j].from
		}
		return cuts[i].to < cuts[j].to
	})

	fmt.Fprintf(w, "\nproposed split: %d groups\n", len(order))
	for _, name := range order {
		var members []string
		for d, gname := range grouping {
			if gname == name {
				members = append(members, d)
			}
		}
		sort.Strings(members)
		fmt.Fprintf(w, "  %-14s %2d domains: %s\n", name, len(members), strings.Join(members, " "))
	}
	if len(unassigned) > 0 {
		fmt.Fprintf(w, "\n  %d domain(s) the grouping does not mention: %s\n",
			len(unassigned), strings.Join(unassigned, " "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(w, "\n  %d name(s) in the grouping that are not domains here: %s\n",
			len(unknown), strings.Join(unknown, " "))
	}
	fmt.Fprintf(w, "\n  %d import statements stay inside a group, %d cross one\n", internal, crossing)
	fmt.Fprintln(w, "\n  the edges this split severs, heaviest first:")
	for i, c := range cuts {
		if i >= 15 {
			fmt.Fprintf(w, "  ... and %d more\n", len(cuts)-15)
			break
		}
		fmt.Fprintf(w, "  %3d  %-16s -> %-16s  (%s -> %s)\n", c.w, c.from, c.to,
			grouping[c.from], grouping[c.to])
	}
	if len(cuts) == 0 {
		fmt.Fprintln(w, "  (none — the grouping does not cut a single edge)")
	}
	fmt.Fprintln(w, "\n  A crossing edge is a place the two pieces must be wired together. It is not")
	fmt.Fprintln(w, "  forbidden and it is not cheap: each one is a seam somebody has to hold open.")
	fmt.Fprintln(w, "  Weight is the number of import statements behind it, so a crossing edge worth 25")
	fmt.Fprintln(w, "  is a different proposition from one worth 1.")
}
