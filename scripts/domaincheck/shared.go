package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// The shared-symbol report answers the question the seam report raised and
// could not: decision 231 found that every remaining edge carries data shapes,
// and 0% of aiops's outbound imports touch only a Usecase. So the lever is not
// "turn this Usecase into a port" — there is no such edge. The lever is the
// shapes themselves, and the only thing that makes a shape cheap to move is
// other domains already carrying it.
//
// A shape selected by N domains is N potential edges that one move can close,
// because the type has exactly one home and every consumer can name it there.
// A shape selected by one domain is that domain's private vocabulary and
// moving it buys nothing: the edge that selects it still selects it, just from
// a different path.
//
// That distinction is what this report is for, and it is not derivable by
// reading the graph by eye: the same symbol name appears in packages that do
// not declare it, the same import statement selects nine symbols at once, and
// "which of these is shared" changes with every edit. It is read off the
// parsed tree for the same reason the seam report is — a hand-sorted version
// of this question has already been wrong twice (decisions 228, 230).
type sharedRow struct {
	sym       string
	consumers map[string]bool
	// targets is the set of domains that DECLARE the symbol, which is one
	// in a healthy tree and more when two domains each have their own
	// same-named type — a case worth seeing, because moving one of them
	// would silently change what the other means.
	targets map[string]bool
}

func (r sharedRow) fanOut() int { return len(r.consumers) }

// printShared writes the symbols that more than one domain selects, heaviest
// first. It is a report, not a gate: the tree is allowed to have no shared
// symbols at all, and the number that matters is whether the top of the list
// is worth a move.
func printShared(w io.Writer, sources []source, r rules) {
	// symbol -> consuming domain -> declaring domain
	consumers := map[string]map[string]map[string]bool{}
	// package -> domain, so a symbol's declaring side can be resolved
	pkgDomain := map[string]string{}
	for _, src := range sources {
		d := domainOf(src.path)
		if d == "" {
			continue
		}
		pkgDomain[pkgKey(src)] = d
	}

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
			for sym := range syms {
				if consumers[sym] == nil {
					consumers[sym] = map[string]map[string]bool{}
				}
				if consumers[sym][from] == nil {
					consumers[sym][from] = map[string]bool{}
				}
				consumers[sym][from][to] = true
			}
		}
	}

	var rows []sharedRow
	for sym, byConsumer := range consumers {
		row := sharedRow{sym: sym, consumers: map[string]bool{}, targets: map[string]bool{}}
		for from, targets := range byConsumer {
			row.consumers[from] = true
			for to := range targets {
				row.targets[to] = true
			}
		}
		if len(row.consumers) > 1 {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if len(rows[i].consumers) != len(rows[j].consumers) {
			return len(rows[i].consumers) > len(rows[j].consumers)
		}
		return rows[i].sym < rows[j].sym
	})

	fmt.Fprintln(w, "shared symbols: types more than one bounded context selects")
	fmt.Fprintln(w, "  A shape N domains carry is N edges one move can close, because the type")
	fmt.Fprintln(w, "  would have exactly one home that all of them can name. A shape only one")
	fmt.Fprintln(w, "  domain carries is that domain's private vocabulary — moving it buys")
	fmt.Fprintln(w, "  nothing, the edge that selects it still selects it from a new path.")
	fmt.Fprintln(w, "  cols: consumers = domains that select it; targets = domains that declare it")
	fmt.Fprintln(w, "  (more than one target means two domains each have their own same-named")
	fmt.Fprintln(w, "  type, which is worth seeing before moving either).")
	fmt.Fprintln(w)
	if len(rows) == 0 {
		fmt.Fprintln(w, "  (none — every cross-domain edge selects only private vocabulary)")
		return
	}
	for _, row := range rows {
		flag := ""
		if len(row.targets) > 1 {
			flag = "  <-- same name, several owners"
		}
		fmt.Fprintf(w, "  %2d  %-30s consumers: %-46s targets: %s%s\n",
			len(row.consumers), row.sym,
			strings.Join(sortedNames(row.consumers), " "),
			strings.Join(sortedNames(row.targets), " "),
			flag)
	}
	fmt.Fprintf(w, "\n  %d symbols are selected by more than one domain\n", len(rows))
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
