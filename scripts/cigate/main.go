// Command cigate checks that the acceptance gates the plan names are gates
// something actually runs.
//
// The 2.0 plan's section 6 ends with a line that reads like a checklist:
//
//	acceptance gates: make module-check + make eval-gates +
//	make module-standalone-check, all green
//
// Two of those three ran in CI and one did not. `eval-gates` was green on
// every developer's machine and on every release note, and nothing stopped
// it regressing: the golden corpus could lose a case, `--fail-on-unmeasured-
// axis` could start failing, and no pull request would have gone red. That
// is the shape this repository keeps finding -- a number that is quoted, a
// command somebody typed, and no owner for the property "and it still runs".
//
// domain-check is in the same position and for a sharper reason. It was
// added by decision 111 precisely because `modulecheck` stops at the module
// and `go-arch-lint` stops at the layer, so neither can see a bounded
// context wanting a hand-written cycle in `.go-arch-lint.yml` or in a repo
// convention. A domain gate that only runs when someone remembers to type it
// is a gate that does not exist -- the seven cycles decision 111 was built
// to expose came back twice.
//
// So this command holds a table of the gates the plan promises and checks
// both halves of each promise: the Makefile still defines the target, and
// CI still invokes it. It reports every disagreement rather than the first,
// and it exits non-zero, because its whole reason to exist is that a
// promise nobody executes is indistinguishable from a promise that holds.
//
// What it deliberately does NOT check: whether the gate is currently green.
// `cigate` reads two files and answers "is this wired". Running the gates is
// what CI does next, in the same job, and the answer is on the same page.
// Folding "is it green" in here would make this a gate that runs gates, and
// the reason a regression is invisible is never that the gate was skipped
// twice.
//
// Usage:
//
//	go run ./scripts/cigate [repo-root]
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Gate is one acceptance gate the plan names, and why it has to run somewhere
// other than a developer's memory.
type Gate struct {
	// Target is the make target. It is spelled the way the plan spells it.
	Target string
	// Why is the property that stops being true when this stops running, in
	// the plan's own terms. A gate with no reason recorded here is the first
	// one someone deletes, so the reason is required rather than prose.
	Why string
}

// Gates is every acceptance gate the plan promises runs, in the plan's order.
//
// The list is written down here rather than derived from CI for the same
// reason scripts/nodearch spells out its four targets: a gate derived from
// the thing it is checking cannot catch that thing losing an entry. If
// ci.yml dropped `make eval-gates`, a checker that read ci.yml would drop the
// requirement along with it and still report every promise kept.
func Gates() []Gate {
	return []Gate{
		{
			Target: "module-check",
			Why: "only core/pig may import PiG, and core may not reach infrastructure; " +
				"a break here is a repository-wide PiG upgrade next time instead of a one-module one",
		},
		{
			Target: "eval-gates",
			Why: "the golden corpus is still servable and every case still declares the three " +
				"diagnostic axes; the number is quoted in every release, so its regressions have to be red",
		},
		{
			Target: "module-standalone-check",
			Why: "every module still builds and tests on its own, on the published tags, with no " +
				"workspace file; this is the only build a node or a release reproduces",
		},
		{
			Target: "domain-check",
			Why: "no bounded context reaches another both ways without a declared cycle, and every " +
				"declaration still points at a tree that exists; seven cycles returned twice before this ran anywhere",
		},
	}
}

// NotInCI is what the plan's acceptance line names but CI deliberately does
// not run, each with the reason.
//
// These are recorded rather than omitted, because "we left it out on purpose"
// and "we forgot about it" look identical from the outside, and the second is
// how this table gets hollowed out. The no-cloud-credential clause in
// particular is an e2e assertion with a `//go:build e2e` tag: it needs a
// docker daemon and a real broker container, which is precisely what the
// fast unit/compile job excludes.
var NotInCI = map[string]string{
	"node holds no cloud vendor key (directory + process environment)": "an e2e assertion: it needs docker and a real broker container, so it lives in make e2e-delivery-check rather than the unit job. See decision 132.",
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := check(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("cigate: all %d plan acceptance gates are defined and invoked by CI\n", len(Gates()))
}

// check reports every gate that is not wired, so one run tells the whole
// story rather than making the reader re-run after each fix.
func check(root string) error {
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return fmt.Errorf("cigate: read Makefile: %w", err)
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		return fmt.Errorf("cigate: read .github/workflows/ci.yml: %w", err)
	}

	defined := makeTargets(string(makefile))
	invoked := invokedTargets(string(ci))

	var problems []string
	for _, g := range Gates() {
		if !defined[g.Target] {
			problems = append(problems, fmt.Sprintf(
				"the Makefile no longer defines %q, so the promise below has nothing to run:\n      %s",
				g.Target, g.Why))
		}
		if !invoked[g.Target] {
			problems = append(problems, fmt.Sprintf(
				"%s is not invoked by .github/workflows/ci.yml; it is green only on machines where somebody remembered to type it, and a regression in it would not fail a pull request:\n      %s",
				g.Target, g.Why))
		}
	}

	// A gate that is wired but no longer promised is the reverse drift: the
	// table says it matters and the Makefile disagrees. Reported, not fixed,
	// because only a human knows which of the two is wrong.
	for target := range invoked {
		if _, exempt := SelfExempt[target]; isGate(target, Gates()) || exempt || !looksLikeGate(target) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"ci.yml invokes %q, which looks like an acceptance gate, but it is not in Gates(); "+
				"either add it with its reason or rename it so it does not read like one", target))
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("plan acceptance gates are not all wired:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// makeTargets is the set of target names the Makefile defines.
//
// A target line's colon is not followed by `=`, which is what separates
// `module-check:` from the assignment `VERSION := 1.2.3` -- both have a colon
// in the first token, and reading the second as a target would let this check
// report a target "defined" that no recipe will ever run. `.PHONY` and other
// dot-targets are excluded, and a commented line is not a definition.
func makeTargets(src string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 || i+1 < len(line) && line[i+1] == '=' {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "=?$") {
			continue
		}
		out[name] = true
	}
	return out
}

// invokedTargets is every make target ci.yml runs.
//
// It reads the file line by line rather than scanning the whole text for
// `make <word>`, because three shapes in a workflow file contain the word
// make without running it: a comment, an `echo`, and a step name. Missing a
// real invocation would let a gate drop out of CI silently, and counting a
// mention would hide the same drift in the other direction -- so both the
// comment strip and the command-position rule are load-bearing.
func invokedTargets(ci string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range strings.Split(ci, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "run:"); ok {
			line = strings.TrimSpace(rest)
		}
		// A shell line can hold several commands; only the ones in command
		// position run make.
		for _, seg := range splitCommands(line) {
			seg = strings.TrimSpace(seg)
			if rest, ok := strings.CutPrefix(seg, "make "); ok {
				out[strings.Fields(rest)[0]] = true
			}
		}
	}
	return out
}

// splitCommands cuts a shell line at the separators that start a new command.
// A quote-aware split is deliberately not needed: the lines this reads are
// `make <target>` invocations and `echo` of them, never quoted separators.
func splitCommands(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool {
		return r == ';' || r == '|' || r == '&'
	})
}

// SelfExempt is what checks gates without being a gate the plan promises.
//
// Recorded rather than skipped by a name rule, because "this check is about
// the wiring of gates, not itself a promised gate" and "somebody added a
// -check target and forgot the table" are the same shape from the outside,
// and the second is how this exemption becomes a hole.
var SelfExempt = map[string]string{
	"ci-gate-check": "this checker: it answers whether the promised gates run, so it is not one of them",
}

// looksLikeGate is the naming shape the reverse-drift rule watches. A target
// ending in -check or named check reads like an acceptance gate to anyone
// scanning ci.yml, so one that is not in Gates() is worth a second look.
func looksLikeGate(target string) bool {
	return strings.HasSuffix(target, "-check") || target == "check"
}

func isGate(target string, gates []Gate) bool {
	for _, g := range gates {
		if g.Target == target {
			return true
		}
	}
	return false
}
