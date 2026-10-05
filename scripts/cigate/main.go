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
	"regexp"
	"slices"
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

// Gates is every acceptance gate the plan's section 6 promises runs, in the
// plan's order.
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

// DecisionGates are the gates a decision committed to CI after the plan was
// written, each with the decision that owns it.
//
// They are kept in a second table rather than folded into Gates() because the
// two answer different questions. Gates() is "did the plan's acceptance line
// survive"; this is "did a decision that moved a gate into CI get walked
// back". Merging them would make the first table stop meaning what its name
// says, and the plan's four-gate line is quoted often enough that it has to
// keep meaning it.
//
// pig-tool-scoping-check is here because it is the one gate that answers
// "can the node's agent actually do anything", and for a long time the
// honest answer was "no" -- 0 of 18 tools. A red gate nobody runs is worse
// than a missing one, so it stayed out of CI while red; the moment upstream
// shipped the fix, wiring it in became the only way to keep the property.
// It is also why this table is not a list of gates somebody liked: the
// entry exists because the property went from impossible to check to
// checked, and a table that only grew on preference would not have grown
// here.
//
// broker-pin-check is here because decision 153 built it to own a property
// nothing owned -- "the broker the acceptance tests is the broker that ships"
// -- and a gate that only runs when somebody remembers to type it owns
// nothing. It is also the gate this table caught skipping itself: until
// decision 164 it asked for go.work, which CI does not have, so wiring it in
// alone would have run a check that skipped.
func DecisionGates() []Gate {
	return []Gate{
		{
			Target: "pig-tool-scoping-check",
			Why: "a node may hold a correct profile, a signed package, a gate, an allow-list and an " +
				"audit ledger and still be handed an agent that cannot call anything; PiG shipped the " +
				"provenance fix in v0.4.0, so the question has an answer again and the only thing " +
				"left is to keep asking it (decision 168)",
		},
		{
			Target: "e2e-manager-check",
			Why: "the plan's section 6 end-to-end acceptance — login, RBAC, credentials, the " +
				"gateway streaming to a node credential, MCP, workflows, notifications, RCA, the " +
				"harness — was checked by nothing but a human typing make test-e2e. ci.yml excluded " +
				"the suite because it needs docker, which is what a GitHub Actions runner is; the " +
				"only part that needs more than that is the tunnel-broker pull, and those two tests " +
				"stay in make e2e-delivery-check so a registry rate limit cannot take the other " +
				"twenty-eight down with them (decision 173)",
		},
		{
			Target: "e2e-delivery-check",
			Why: "the last clause of the plan's acceptance line — the node's install directory and " +
				"its process environment hold no cloud vendor key — has a test and had nothing " +
				"that triggered it, so it had been reporting on nothing since decision 132 parked " +
				"it behind the broker container. It stays off the per-push job (a Docker Hub rate " +
				"limit must not block a pull request) and runs nightly in the `delivery` job " +
				"instead (decision 186)",
		},
		{
			Target: "broker-pin-check",
			Why: "every file that names the frontier broker names one version, and the shipped " +
				"spelling (v1.2.5) and the pulled spelling (1.2.5) agree; the release and the " +
				"acceptance suite drifted to two versions once and each file stayed correct " +
				"(decision 153)",
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
	"node holds no cloud vendor key (directory + process environment)":  "an e2e assertion: it needs docker and a real broker container, so it cannot join the per-push job without letting a Docker Hub rate limit block a pull request. It is not absent from CI, though — it runs nightly in the `delivery` job (make e2e-delivery-check), which is where decision 132 put it and where it now has something that triggers it. See decision 132 and decision 186.",
	"release metadata still describes this commit (make version-check)": "a release-time assertion, not a per-push one: it compares RELEASE_VERSION.json's web_hash and teamharness_source_tree against `git rev-parse HEAD:<tree>`, so it can only be green on the commit that was actually signed. Run on every push it was red by construction AND sat in front of the open-source gate, so a private path or a credential about to ship was never checked at all; it now runs in .github/workflows/release.yml, where its comparisons mean something. See decision 166.",
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
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("cigate: all %d acceptance gates (%d named by the plan, %d owned by a decision) "+
		"are defined and invoked by CI, and %s\n",
		len(allGates()), len(Gates()), len(DecisionGates()), triggerSummary(TriggerReachabilityOf(string(ci))))
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
	for _, g := range allGates() {
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
		if _, exempt := SelfExempt[target]; isGate(target, allGates()) || exempt || !looksLikeGate(target) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"ci.yml invokes %q, which looks like an acceptance gate, but it is not in Gates(); "+
				"either add it with its reason or rename it so it does not read like one", target))
	}

	// A gate that runs twenty-eight of the suite's thirty tests is only as
	// honest as the two it skips. That pair is re-derived from the sources
	// rather than read from the Makefile, because a skip list that checks
	// itself is a skip list that can grow.
	if err := brokerSkipAgrees(root, string(makefile)); err != nil {
		problems = append(problems, err.Error())
	}

	// The gates being wired is only half of what a workflow promises; the other
	// half is that a push can start it. Reported with the same discipline --
	// every disagreement, then exit non-zero.
	if reach := TriggerReachabilityOf(string(ci)); reach.Problem() != "" {
		problems = append(problems, reach.Problem())
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

// brokerCallers are the two testenv entry points that need a tunnel broker
// container. SharedFrontier brings the broker up; WithFrontier hands an
// existing one to Start. A test that calls either cannot run without it.
//
// The match is anchored on `testenv.` so that TestMain's teardown call —
// testenv.TerminateSharedFrontier — is not read as a dependency. It appears
// in every e2e run and needs no broker of its own, and matching it would put
// the package's entry point in the skip list.
var brokerCallers = []string{"testenv.SharedFrontier(", "testenv.WithFrontier("}

// topLevelFuncRE finds the start of any top-level func, named or not.
var topLevelFuncRE = regexp.MustCompile(`(?m)^func ([A-Za-z0-9_]+)\(`)

// brokerDependentTests re-derives which e2e tests need a broker container.
//
// It is function-scoped, and the first version of this was file-scoped and
// was wrong within a day: node_agent_delivery_test.go holds both
// TestNodeAgentDelivery (which starts the broker) and
// TestTheGatewayServesAStreamToANodeCredential (which cuts the node out
// entirely and runs anywhere). A file-granular reader either excluded the
// gateway hop along with the delivery test — quietly giving up the hop that
// proves a node credential can get a stream at all — or demanded a broker
// for a test that never dials one.
//
// The extent of a Go function is not something to re-derive from braces, so
// the reader attributes each call to the nearest preceding top-level `func`
// and keeps the ones that are tests. A broker call sitting in a file-scope
// helper therefore attributes to nothing, which is the safe direction: the
// check reports a disagreement and a human looks.
func brokerDependentTests(e2eDir string) ([]string, error) {
	entries, err := os.ReadDir(e2eDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", e2eDir, err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(e2eDir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		current := ""
		for _, line := range strings.Split(string(body), "\n") {
			if m := topLevelFuncRE.FindStringSubmatch(line); m != nil {
				current = m[1]
			}
			if current == "" || !strings.HasPrefix(current, "Test") {
				continue
			}
			for _, caller := range brokerCallers {
				if strings.Contains(line, caller) {
					seen[current] = true
				}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// makefileVar reads a top-level `NAME := value` assignment. Like makeTargets
// it skips recipes, comments and dot-targets, and unlike a regexp over the
// whole file it does not have to reason about backslash continuations.
func makefileVar(src, name string) (string, bool) {
	prefix := name + " :="
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `\`)), true
	}
	return "", false
}

// brokerSkipAgrees is the check on the check.
//
// e2e-manager-check excludes the broker tests by a name list in the Makefile.
// A list nobody compares to the sources is a list that only grows: add a test
// that needs a broker, do not list it, and it fails loudly — fine. Add one
// that does not, list it anyway to get a pipeline green, and the suite quietly
// stops covering whatever it covered. Both directions are compared here, and
// the recipe is required to actually use the variable, so a list that is
// maintained but not wired is reported too.
func brokerSkipAgrees(root, makefileSrc string) error {
	derived, err := brokerDependentTests(filepath.Join(root, "tests", "e2e"))
	if err != nil {
		return err
	}
	if len(derived) == 0 {
		return fmt.Errorf("no e2e test calls %s any more, so E2E_BROKER_TESTS is either "+
			"stale or the harness moved; a skip list that describes nothing is not a skip list, "+
			"it is a way to stop asking", strings.Join(brokerCallers, " or "))
	}

	raw, ok := makefileVar(makefileSrc, "E2E_BROKER_TESTS")
	if !ok {
		return fmt.Errorf("the Makefile no longer defines E2E_BROKER_TESTS; make e2e-manager-check " +
			"silently stops skipping anything, and the broker tests take the suite down with them")
	}
	listed := strings.Split(raw, "|")
	sort.Strings(listed)

	var problems []string
	for _, want := range derived {
		if !slices.Contains(listed, want) {
			problems = append(problems, fmt.Sprintf(
				"%s calls testenv.SharedFrontier or testenv.WithFrontier, so it needs the broker "+
					"container, but E2E_BROKER_TESTS does not name it; add it, or the e2e job will "+
					"fail on a Docker Hub rate limit that has nothing to do with the change", want))
		}
	}
	for _, got := range listed {
		if !slices.Contains(derived, got) {
			problems = append(problems, fmt.Sprintf(
				"E2E_BROKER_TESTS excludes %s, which needs no broker container; a test is only "+
					"allowed on that list if the sources say it pulls the broker, so this one is "+
					"something the suite stops covering", got))
		}
	}
	if !strings.Contains(makefileSrc, "-skip '$(E2E_BROKER_TESTS)'") {
		problems = append(problems,
			"make e2e-manager-check does not pass -skip '$(E2E_BROKER_TESTS)', so the list above "+
				"is maintained correctly and then not used")
	}
	if len(problems) > 0 {
		return fmt.Errorf("the e2e suite's broker exclusion does not match its sources:\n  %s",
			strings.Join(problems, "\n  "))
	}
	return nil
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

// allGates is both tables, in the order they run: the plan's gates first,
// then the decision-owned ones. Every rule that has to see the whole set --
// the reverse-drift check and the report -- goes through here rather than
// through either table, so a gate added to one is seen by the other's rules.
func allGates() []Gate {
	all := Gates()
	all = append(all, DecisionGates()...)
	return all
}

// TriggerReachability is the second promise ci.yml makes about itself: that a
// push to a branch somebody works on can start it at all.
//
// The failure this exists to catch is not hypothetical and it is not subtle.
// ci.yml triggered on `push: branches: [main]`, the entire 2.0 line lives on
// feature/pig, and nobody opened a pull request -- so
// `gh api repos/<this repository>/actions/runs --jq .total_count` answered
// 0 -- the repository is public and the open-source gate rejects the private
// owner's name, so the command is written with a placeholder rather than the
// real slug. Five acceptance gates had been wired into a workflow that had never
// executed once (decision 163 wired them; decision 164 found that one of them
// skipped itself and four others had never reported anything). Every claim
// that CI guards those gates was true on paper and unexecuted in fact.
//
// A one-branch whitelist is the shape of that mistake: it reads as "run on
// pushes", and it is not. So the rule below is narrow on purpose -- a push
// trigger restricted to exactly one branch is reported; anything wider, and
// any workflow that also offers pull_request or workflow_dispatch, passes.
// The escape hatch is to drop `branches:` entirely, which is what the fix did.
type TriggerReachability struct {
	// Restricted is true when the push trigger carries a `branches:` filter.
	Restricted bool
	// Branches is the filter's list, empty when it was absent or written
	// inline in a shape this reader did not recognise.
	Branches []string
	// PushPresent is false when the workflow has no push trigger at all, which
	// is legal (pull_request-only is a real choice) and not reported here.
	PushPresent bool
	// Other is every non-push event name declared under `on:`.
	Other []string
}

// TriggerProblem returns the reason the workflow cannot be started by a push
// to an arbitrary branch, or "" when it can.
//
// The list is deliberately parsed rather than loaded through a YAML library:
// this command already hand-reads the Makefile and the workflow, and adding a
// dependency to ask one yes/no question about an `on:` block would make the
// file's format, not its promise, the thing that changes most often.
func TriggerReachabilityOf(ci string) TriggerReachability {
	var out TriggerReachability
	lines := strings.Split(ci, "\n")

	// The `on:` key. YAML lets a workflow spell it `on:` or `true:`, and only
	// the first is written here; if it is missing the zero value reports no
	// push trigger and the caller decides what that means.
	start := -1
	for i, raw := range lines {
		if raw == "on:" {
			start = i
			break
		}
	}
	if start < 0 {
		return out
	}

	// The body of `on:` runs until the next line that is neither indented nor a
	// comment or blank, and its direct children are the lines at the first
	// indent inside it -- `push:`'s own `branches:` is one level deeper and
	// belongs to the child, not to `on:`. Reading the whole indented region
	// would hand the trigger's filter to whatever event happened to have one.
	type child struct {
		name  string
		start int
		end   int
	}
	var children []child
	childIndent := -1
	for i := start + 1; i < len(lines); i++ {
		raw := lines[i]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") {
			break
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if childIndent < 0 {
			childIndent = indent
		}
		if indent != childIndent {
			continue
		}
		name, _, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		children = append(children, child{name: name, start: i, end: len(lines)})
		if len(children) > 1 {
			children[len(children)-2].end = i
		}
	}

	for _, c := range children {
		if c.name != "push" {
			out.Other = append(out.Other, c.name)
			continue
		}
		out.PushPresent = true
		// A push with an inline value (`push: {}`, `push: # note`) has no
		// nested block and therefore no branch filter.
		if pushIsLeaf(lines[c.start]) {
			continue
		}
		for i := c.start + 1; i < c.end; i++ {
			raw := lines[i]
			if strings.HasPrefix(raw, "#") {
				continue
			}
			name, rest, ok := strings.Cut(raw, ":")
			if !ok || strings.TrimSpace(name) != "branches" {
				continue
			}
			out.Restricted = true
			out.Branches = append(out.Branches, parseBranchList(lines, i, c.end, strings.TrimSpace(rest))...)
		}
	}
	return out
}

// pushIsLeaf reports whether the `push:` line carries an inline value, which
// ends its block and rules out a `branches:` filter under it.
func pushIsLeaf(line string) bool {
	_, rest, ok := strings.Cut(line, ":")
	if !ok {
		return false
	}
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = strings.TrimSpace(rest[:i])
	}
	return rest != ""
}

// parseBranchList reads both spellings of the filter: `branches: [main, ci]`
// on one line, and `branches:` followed by `- main` entries.
func parseBranchList(lines []string, at, end int, inline string) []string {
	inline = strings.Trim(inline, "[]")
	if inline != "" {
		var out []string
		for _, part := range strings.Split(inline, ",") {
			if name := strings.Trim(strings.TrimSpace(part), `"'`); name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	var out []string
	for i := at + 1; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "-") {
			break
		}
		if name := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")), `"'`); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// Problem is the human-readable version of the reachability rule, or "" when
// the workflow can be started by a push to an arbitrary branch.
func (t TriggerReachability) Problem() string {
	if !t.Restricted {
		return ""
	}
	if len(t.Branches) != 1 {
		return ""
	}
	other := strings.Join(t.Other, ", ")
	suffix := ""
	if other != "" {
		suffix = " (the workflow also declares " + other +
			", so a change only reaches CI if somebody keeps opening those)"
	}
	return fmt.Sprintf(
		"ci.yml runs on pushes to %q and nothing else%s, so a commit on any other "+
			"branch is never built or tested by this workflow; drop the `branches:` "+
			"filter so every push runs",
		t.Branches[0], suffix)
}

// triggerSummary is how the reachability rule reads when it holds, so a green
// run states the property rather than leaving it implied.
func triggerSummary(t TriggerReachability) string {
	switch {
	case !t.PushPresent:
		return "the workflow declares no push trigger (pull_request-only is a choice, not an omission)"
	case !t.Restricted:
		return "every push starts the workflow"
	default:
		return "pushes to " + strings.Join(t.Branches, ", ") + " start it"
	}
}

func isGate(target string, gates []Gate) bool {
	for _, g := range gates {
		if g.Target == target {
			return true
		}
	}
	return false
}
