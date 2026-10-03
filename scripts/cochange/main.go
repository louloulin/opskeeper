// Command cochange reports which domains change together.
//
// domaincheck answers "which bounded contexts depend on which", from the
// import graph. That is the wrong question for one specific decision: the
// manager split proposal in docs/manager-split.proposed is priced by that
// graph — 41 crossing edges means the cut is cheap — and it says outright
// that the number cannot say whether the cut is *right*, because three facts
// are missing. One of them is which domains ship independently.
//
// The import graph cannot answer it, and the reason is worth stating: an
// import edge says two packages must be *built* together, not that anyone
// ever *changes* them together. A domain can be a leaf that twenty others
// depend on and still be worked on alone most of the time, and that is
// exactly the domain a split should be free to move.
//
// So this reads the other axis: for every commit that touched the control
// plane, which domains did it touch. A domain changed in a commit that
// touched nothing else is being evolved on its own, and that is the
// property "these ship independently" is asking about.
//
// It reports and exits 0, for the same reason deadcode does: this is
// evidence for a judgement, not a verdict. See limits below — the honest
// ones are in Limits, and a reader who skips them will over-trust the
// ranking, because the numbers look more precise than they are.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// layerDirs are the first path segments under core/manager that are a
// layering rather than a domain. The domain is the segment after them,
// which is the same rule domaincheck uses — a second definition would
// produce numbers that do not reconcile with the ones the split is priced
// in, and two disagreeing maps of the same tree are worse than one.
var layerDirs = map[string]bool{
	"biz": true, "server": true, "service": true, "model": true, "data": true,
}

const managerPrefix = "core/manager/"

// Change is one commit's footprint in the control plane.
type Change struct {
	Commit  string
	Subject string
	Domains []string
}

// Solo is the share of a domain's changes that touched no other domain.
//
// It is the ranking this tool exists to produce. A domain at 1.0 was only
// ever worked on alone in the window examined; a domain at 0 never was.
type Solo struct {
	Domain string
	Solo   int
	Total  int
}

// Ratio is Solo's share of Total, or 0 when the domain never changed.
func (s Solo) Ratio() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Solo) / float64(s.Total)
}

// Pair is how often two domains were changed by the same commit.
type Pair struct {
	A, B  string
	Times int
}

// Report is everything this tool found.
type Report struct {
	Commits       int
	Touching      int
	MedianDomains float64
	Solo          []Solo
	Pairs         []Pair
	// Widest is the largest number of domains one commit touched. A
	// commit that touches most of the tree carries no information about
	// coupling, and the reader deserves to know the ceiling.
	Widest int
}

// domainOf maps a path under core/manager to its domain.
//
// A file sitting directly in the module root — go.mod, go.sum — has no
// domain and must not be given one. Counting "go.mod" as a domain that is
// "changed alone" would put a build file at the top of a ranking about
// which bounded contexts evolve independently, which is worse than
// reporting nothing.
func domainOf(path string) string {
	if !strings.HasPrefix(path, managerPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, managerPrefix)
	if !strings.Contains(rest, "/") {
		return ""
	}
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		return ""
	}
	if layerDirs[parts[0]] && len(parts) > 1 {
		return parts[1]
	}
	return parts[0]
}

// summarise turns a set of commits into the report.
//
// Changes are passed in rather than read from git so the arithmetic can be
// tested without a repository, which is the only way to test a ranking
// honestly: a fixture where the answer is obvious by hand.
func summarise(changes []Change) Report {
	r := Report{Commits: len(changes)}

	soloCount := map[string]int{}
	total := map[string]int{}
	pairs := map[[2]string]int{}

	for _, c := range changes {
		ds := c.Domains
		if len(ds) == 0 {
			continue
		}
		r.Touching++
		if len(ds) > r.Widest {
			r.Widest = len(ds)
		}
		for _, d := range ds {
			total[d]++
		}
		if len(ds) == 1 {
			soloCount[ds[0]]++
		}
		sorted := append([]string(nil), ds...)
		sort.Strings(sorted)
		for i := range sorted {
			for j := i + 1; j < len(sorted); j++ {
				pairs[[2]string{sorted[i], sorted[j]}]++
			}
		}
	}

	sizes := make([]int, 0, len(changes))
	for _, c := range changes {
		if len(c.Domains) > 0 {
			sizes = append(sizes, len(c.Domains))
		}
	}
	sort.Ints(sizes)
	if len(sizes) > 0 {
		r.MedianDomains = float64(sizes[len(sizes)/2])
	}

	for d, n := range total {
		r.Solo = append(r.Solo, Solo{Domain: d, Solo: soloCount[d], Total: n})
	}
	sort.Slice(r.Solo, func(i, j int) bool {
		if r.Solo[i].Ratio() != r.Solo[j].Ratio() {
			return r.Solo[i].Ratio() > r.Solo[j].Ratio()
		}
		return r.Solo[i].Domain < r.Solo[j].Domain
	})

	for k, n := range pairs {
		r.Pairs = append(r.Pairs, Pair{A: k[0], B: k[1], Times: n})
	}
	sort.Slice(r.Pairs, func(i, j int) bool {
		if r.Pairs[i].Times != r.Pairs[j].Times {
			return r.Pairs[i].Times > r.Pairs[j].Times
		}
		if r.Pairs[i].A != r.Pairs[j].A {
			return r.Pairs[i].A < r.Pairs[j].A
		}
		return r.Pairs[i].B < r.Pairs[j].B
	})
	return r
}

func readHistory(repo, ref string) ([]Change, error) {
	args := []string{"log", "--format=%H"}
	if ref != "" {
		// An empty ref is left off entirely rather than passed as "",
		// because git treats an empty argument as a ref name and fails.
		args = append(args, ref)
	}
	raw, err := git(repo, args...)
	if err != nil {
		return nil, err
	}
	var out []Change
	for _, c := range strings.Fields(raw) {
		if c == "" {
			continue
		}
		files, err := git(repo, "show", "--name-only", "--format=", c, "--", "core/manager")
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		var ds []string
		for _, f := range strings.Fields(files) {
			d := domainOf(f)
			if d != "" && !seen[d] {
				seen[d] = true
				ds = append(ds, d)
			}
		}
		if len(ds) == 0 {
			continue
		}
		subject, _ := git(repo, "log", "-1", "--format=%s", c)
		out = append(out, Change{Commit: c, Subject: strings.TrimSpace(subject), Domains: ds})
	}
	return out, nil
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func printReport(w *os.File, r Report) {
	fmt.Fprintf(w, "cochange: %d commits examined, %d touched the control plane\n",
		r.Commits, r.Touching)
	fmt.Fprintf(w, "  domains per commit: median %.0f, widest %d\n\n",
		r.MedianDomains, r.Widest)
	if r.MedianDomains <= 1 {
		fmt.Fprintf(w, "  a median of one means most commits stay inside a single\n"+
			"  domain, so co-change carries real signal here rather than the\n"+
			"  everything-touches-everything noise a busy monorepo produces.\n\n")
	} else {
		fmt.Fprintf(w, "  a median above one means most commits straddle domains,\n"+
			"  so the ranking below is weak. Treat it as a hint, not evidence.\n\n")
	}

	fmt.Fprintf(w, "  changed alone (the property \"ships independently\" asks about):\n")
	for _, s := range r.Solo {
		fmt.Fprintf(w, "    %-16s %2d / %2d  (%.0f%%)\n", s.Domain, s.Solo, s.Total, s.Ratio()*100)
	}
	fmt.Fprintf(w, "\n  most frequently changed together:\n")
	limit := len(r.Pairs)
	if limit > 15 {
		limit = 15
	}
	for _, p := range r.Pairs[:limit] {
		fmt.Fprintf(w, "    %-16s %-16s %d\n", p.A, p.B, p.Times)
	}
	fmt.Fprintf(w, `
  limits, which matter more than the ranking:
    - the window is this repository's whole history, which is short. A
      domain with few changes has a coarse ratio, and 0/1 and 0/2 are not
      the same evidence.
    - a focused refactoring campaign shows up as independence that is not
      structural. Read a domain's solo commits before believing its ratio.
    - co-change says what changes together, not what must ship together.
      One deployable can hold several domains; the reverse is rarer.
    - none of this is a gate. It is evidence for a judgement that a person
      still has to make.
`)
}

func main() {
	repo := flag.String("repo", ".", "repository to read history from")
	ref := flag.String("ref", "", "ref to read; empty reads HEAD")
	flag.Parse()

	changes, err := readHistory(filepath.Clean(*repo), *ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cochange: "+err.Error())
		os.Exit(2)
	}
	printReport(os.Stdout, summarise(changes))
}
