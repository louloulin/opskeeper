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
// The ranking is necessary and not sufficient, and the second half of the
// report is the part that keeps it honest. A share cannot distinguish three
// things that look identical in the number: a domain that is genuinely
// evolved on its own, a domain that was refactored in one sitting, and a
// domain that did not exist until halfway through the window and therefore
// had nothing to co-change with. The last one is the dangerous one, because
// building a feature and shipping a service independently are opposite
// claims wearing the same figure. So the report pairs every ratio with the
// shape behind it — how many separate stretches of work, and how many of
// them landed before the domain had any history at all.
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
	// Runs is how many separate stretches of consecutive control-plane
	// commits this domain's solo work falls into, and LongestRun is the
	// size of the biggest one.
	//
	// These exist because a ratio cannot tell a habit from a campaign. Ten
	// solo commits in one run is one person (or one agent) spending a
	// morning inside a domain; ten solo commits in ten runs is the same
	// domain coming back on its own all quarter. The ratio reads 50% for
	// both, and only one of them is evidence about how the domain ships.
	Runs       int
	LongestRun int
	// Built is how many of this domain's solo commits sit inside its very
	// first run of changes — the stretch in which the domain did not
	// exist yet and nothing else could have touched it.
	//
	// This is the sharpest correction to the whole ranking. A domain that
	// was created inside the measured window scores 100% solo by
	// construction, because there is nothing co-changing with it yet. That
	// is not evidence of independent shipping; it is evidence of a feature
	// being written. The two look identical in the ratio and mean opposite
	// things, so the ratio is unreadable without this number beside it.
	Built int
}

// Campaign is what the run shape looks like in words: a domain whose solo
// work is one block rather than a habit.
type Campaign struct {
	Domain     string
	Solo       int
	Runs       int
	LongestRun int
	Built      int
	Subjects   []string
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
	// Campaigns is every domain with solo work, ordered by the size of its
	// longest run. A domain whose solo work is one run is reported with
	// the subjects of that run, because the reader's only real question
	// is "was that one afternoon or a quarter of work".
	Campaigns []Campaign
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

	// The population every statement below is about: commits that touched
	// at least one domain. A commit that touched none is not a gap in a
	// domain's history, it is simply outside this study, and letting it
	// break a run would understate how long a campaign actually ran — a
	// docs-only commit wedged between two halves of one refactoring is
	// not evidence that the two halves were separate days of work.
	ordered := make([]Change, 0, len(changes))
	for _, c := range changes {
		if len(c.Domains) > 0 {
			ordered = append(ordered, c)
		}
	}

	total := map[string]int{}
	pairs := map[[2]string]int{}
	soloAt := map[string][]int{}
	allAt := map[string][]int{}
	soloChange := map[int]Change{}

	for i, c := range ordered {
		ds := c.Domains
		r.Touching++
		if len(ds) > r.Widest {
			r.Widest = len(ds)
		}
		for _, d := range ds {
			total[d]++
			allAt[d] = append(allAt[d], i)
		}
		if len(ds) == 1 {
			soloAt[ds[0]] = append(soloAt[ds[0]], i)
			soloChange[i] = c
		}
		sorted := append([]string(nil), ds...)
		sort.Strings(sorted)
		for a := range sorted {
			for b := a + 1; b < len(sorted); b++ {
				pairs[[2]string{sorted[a], sorted[b]}]++
			}
		}
	}

	sizes := make([]int, 0, len(ordered))
	for _, c := range ordered {
		sizes = append(sizes, len(c.Domains))
	}
	sort.Ints(sizes)
	if len(sizes) > 0 {
		r.MedianDomains = float64(sizes[len(sizes)/2])
	}

	for d, n := range total {
		runs, longest := soloRuns(soloAt[d])
		// The first run of the domain's OWN changes, not of its solo ones:
		// construction is the opening of the domain's history, and a commit
		// that was part of building it may well have touched a second
		// domain without ceasing to be construction.
		bornRun := firstRun(allAt[d])
		built := countIn(soloAt[d], bornRun)
		r.Solo = append(r.Solo, Solo{
			Domain: d, Solo: len(soloAt[d]), Total: n,
			Runs: runs, LongestRun: longest, Built: built,
		})
		if len(soloAt[d]) > 0 {
			r.Campaigns = append(r.Campaigns, Campaign{
				Domain:     d,
				Solo:       len(soloAt[d]),
				Runs:       runs,
				LongestRun: longest,
				Built:      built,
				Subjects:   subjectsOf(soloChange, longestRun(soloAt[d])),
			})
		}
	}
	sort.Slice(r.Campaigns, func(i, j int) bool {
		// Domains whose solo work is mostly their own construction come
		// first: those are the ratios a reader is most likely to over-trust.
		if (r.Campaigns[i].Built == r.Campaigns[i].Solo) != (r.Campaigns[j].Built == r.Campaigns[j].Solo) {
			return r.Campaigns[i].Built == r.Campaigns[i].Solo
		}
		if r.Campaigns[i].LongestRun != r.Campaigns[j].LongestRun {
			return r.Campaigns[i].LongestRun > r.Campaigns[j].LongestRun
		}
		if r.Campaigns[i].Solo != r.Campaigns[j].Solo {
			return r.Campaigns[i].Solo > r.Campaigns[j].Solo
		}
		return r.Campaigns[i].Domain < r.Campaigns[j].Domain
	})
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

// soloRuns groups a domain's solo positions into maximal runs of
// consecutive control-plane commits, and reports how many there are and how
// long the biggest is.
//
// The input is not required to be sorted; it is sorted here so the caller
// cannot get a different answer by changing the order git hands commits
// back in.
func soloRuns(positions []int) (runs, longest int) {
	if len(positions) == 0 {
		return 0, 0
	}
	sorted := append([]int(nil), positions...)
	sort.Ints(sorted)
	runs = 1
	run := 1
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1]+1 {
			run++
			continue
		}
		if run > longest {
			longest = run
		}
		runs++
		run = 1
	}
	if run > longest {
		longest = run
	}
	return runs, longest
}

// firstRun returns the opening stretch of a domain's own history: the
// consecutive positions starting at its earliest change.
//
// This is the run in which the domain was being created. A commit inside it
// could not have been a co-change with anything, because the domain that
// would have co-changed with it had not been written yet.
func firstRun(positions []int) map[int]bool {
	in := map[int]bool{}
	if len(positions) == 0 {
		return in
	}
	sorted := append([]int(nil), positions...)
	sort.Ints(sorted)
	start := sorted[0]
	for _, pos := range sorted {
		if pos != start {
			break
		}
		in[pos] = true
		start++
	}
	return in
}

// countIn counts how many of want fall inside in.
func countIn(want []int, in map[int]bool) int {
	n := 0
	for _, pos := range want {
		if in[pos] {
			n++
		}
	}
	return n
}

// longestRun returns the positions of the biggest run, which is the one a
// reader should be shown: it is the stretch that decides whether a ratio
// describes a habit or a single push.
func longestRun(positions []int) []int {
	if len(positions) == 0 {
		return nil
	}
	sorted := append([]int(nil), positions...)
	sort.Ints(sorted)

	best := []int{sorted[0]}
	cur := []int{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1]+1 {
			cur = append(cur, sorted[i])
			continue
		}
		if len(cur) > len(best) {
			best = cur
		}
		cur = []int{sorted[i]}
	}
	if len(cur) > len(best) {
		best = cur
	}
	return best
}

// subjectCap bounds how many commit subjects a campaign line prints. The
// point is to let a reader recognise the shape of the work, and twenty
// lines of it stops being a summary.
const subjectCap = 6

// subjectsOf turns run positions into printable commit subjects, oldest
// first, capped.
func subjectsOf(byPosition map[int]Change, run []int) []string {
	ordered := append([]int(nil), run...)
	sort.Sort(sort.Reverse(sort.IntSlice(ordered))) // the log is newest-first
	out := make([]string, 0, len(ordered))
	for _, pos := range ordered {
		c, ok := byPosition[pos]
		if !ok {
			continue
		}
		subject := c.Subject
		if subject == "" {
			subject = c.Commit
		}
		out = append(out, subject)
		if len(out) == subjectCap {
			break
		}
	}
	return out
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
		fmt.Fprintf(w, "    %-16s %2d / %2d  (%.0f%%)  in %d run(s), longest %d\n",
			s.Domain, s.Solo, s.Total, s.Ratio()*100, s.Runs, s.LongestRun)
	}

	// The ratio above cannot tell a habit from a campaign, and the split
	// proposal turns on exactly that difference. So the shape is printed
	// next to it, with the commits of the biggest run, because the reader's
	// only real question is whether that run was one push or a quarter of
	// recurring work.
	fmt.Fprintf(w, "\n  the runs behind those ratios (consecutive control-plane commits):\n")
	for _, c := range r.Campaigns {
		verdict := "recurring"
		switch {
		case c.Built == c.Solo && c.Solo > 1:
			// Every solo commit is inside the domain's opening run, so
			// the domain did not exist when the earlier ones landed. The
			// ratio measures a feature being written.
			verdict = "CONSTRUCTION, not independence"
		case c.Built*2 >= c.Solo && c.Solo > 1:
			verdict = fmt.Sprintf("mostly construction (%d/%d solo at birth)", c.Built, c.Solo)
		case c.Runs == 1:
			verdict = "ONE CAMPAIGN"
		case c.LongestRun >= 3 && c.LongestRun*2 >= c.Solo:
			verdict = "mostly one campaign"
		}
		fmt.Fprintf(w, "    %-16s %d solo in %d run(s), longest %d, %d at birth  <- %s\n",
			c.Domain, c.Solo, c.Runs, c.LongestRun, c.Built, verdict)
		for _, subject := range c.Subjects {
			fmt.Fprintf(w, "        %s\n", subject)
		}
		if c.Solo > len(c.Subjects) {
			fmt.Fprintf(w, "        ... and %d more in that run\n", c.Solo-len(c.Subjects))
		}
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
      structural. The runs section above is how you tell: a domain whose
      solo work is ONE run has not been shown to ship independently, it has
      been shown to have been refactored in one sitting. A run boundary is
      evidence about a campaign, not about a deployment boundary.
    - worse, a domain CREATED inside this window scores high by
      construction: it had nothing to co-change with yet. Read the "at
      birth" column before reading any ratio. A domain marked CONSTRUCTION
      has not demonstrated independent shipping; it has demonstrated that
      somebody wrote it.
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
