package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepo lays out the two files check() reads, so a test can mutate one
// of them and see the verdict move.
func writeRepo(t *testing.T, makefile, ci string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(ci), 0o644); err != nil {
		t.Fatalf("write ci.yml: %v", err)
	}
	return root
}

// repoMakefile is a Makefile that defines every gate, in the same shape the
// real one uses (a phony list, a target, a recipe).
func repoMakefile() string {
	var b strings.Builder
	b.WriteString(".PHONY: " + strings.Join(gateNames(), " ") + "\n")
	for _, g := range allGates() {
		b.WriteString(g.Target + ": ## does the thing\n\tgo run ./scripts/x .\n\n")
	}
	// A near-miss: a variable whose name contains a gate, and a target that
	// is not a gate. Neither should count.
	b.WriteString("VERSION := $(shell cat VERSION)\n")
	b.WriteString("help: ## prints help\n\t@echo hi\n")
	return b.String()
}

// repoCI is a ci.yml that runs every gate, each on its own step.
func repoCI() string {
	var b strings.Builder
	for _, g := range allGates() {
		b.WriteString("      - name: " + g.Target + "\n        run: make " + g.Target + "\n")
	}
	return b.String()
}

func gateNames() []string {
	out := make([]string, 0, len(allGates()))
	for _, g := range allGates() {
		out = append(out, g.Target)
	}
	return out
}

func TestTheWiredRepositoryPasses(t *testing.T) {
	if err := check(writeRepo(t, repoMakefile(), repoCI())); err != nil {
		t.Fatalf("a repository that defines and invokes every gate did not pass: %v", err)
	}
}

func TestADroppedCIInvocationIsReported(t *testing.T) {
	for _, g := range allGates() {
		t.Run(g.Target, func(t *testing.T) {
			ci := strings.Replace(repoCI(), "        run: make "+g.Target+"\n", "        run: make help\n", 1)
			err := check(writeRepo(t, repoMakefile(), ci))
			if err == nil {
				t.Fatalf("ci.yml no longer runs %s, and the check passed", g.Target)
			}
			if !strings.Contains(err.Error(), g.Target) {
				t.Errorf("the report does not name the gate that stopped running: %v", err)
			}
		})
	}
}

func TestADroppedMakefileTargetIsReported(t *testing.T) {
	for _, g := range allGates() {
		t.Run(g.Target, func(t *testing.T) {
			mk := strings.Replace(repoMakefile(), g.Target+": ## does the thing\n\tgo run ./scripts/x .\n\n", "", 1)
			err := check(writeRepo(t, mk, repoCI()))
			if err == nil {
				t.Fatalf("the Makefile no longer defines %s, and the check passed", g.Target)
			}
			if !strings.Contains(err.Error(), g.Target) {
				t.Errorf("the report does not name the target that vanished: %v", err)
			}
		})
	}
}

// A gate mentioned in a comment or as a substring of another word is not an
// invocation. This is the mutation that a naive `strings.Contains(ci, target)`
// would pass while the gate never runs.
func TestAMentionIsNotAnInvocation(t *testing.T) {
	ci := strings.Replace(repoCI(), "        run: make eval-gates\n", "        run: echo eval-gates is documented above\n", 1)
	err := check(writeRepo(t, repoMakefile(), ci))
	if err == nil {
		t.Fatal("a mention of eval-gates that never runs it was accepted as an invocation")
	}
	if !strings.Contains(err.Error(), "eval-gates") {
		t.Errorf("the report does not name eval-gates: %v", err)
	}
}

// The table has to carry a reason. A gate nobody can say why it matters is the
// first one deleted, so an empty Why is a defect in the table itself.
func TestEveryGateRecordsWhyItMatters(t *testing.T) {
	for _, g := range allGates() {
		if strings.TrimSpace(g.Target) == "" {
			t.Error("a gate has no target")
		}
		if strings.TrimSpace(g.Why) == "" {
			t.Errorf("gate %q has no reason recorded; a gate with no reason is the first one deleted", g.Target)
		}
	}
}

// makeTargets is the parser the whole check rests on. Four mutations, each a
// shape that would silently widen the set of "defined" targets if it were
// read as one.
func TestMakeTargetsIgnoresWhatIsNotATarget(t *testing.T) {
	src := strings.Join([]string{
		".PHONY: module-check eval-gates",
		"VERSION := 1.2.3",
		"FRONTIER_VERSION ?= v1.2.5",
		"module-check: ## boundary checker",
		"\tgo run ./scripts/modulecheck .",
		"# eval-gates: mentioned only in a comment",
		"",

		"eval-gates: eval-coverage eval-vocabulary",
		"\tgo run ./cmd/opskeeper-eval plugin-coverage",
	}, "\n")
	got := makeTargets(src)
	for _, want := range []string{"module-check", "eval-gates"} {
		if !got[want] {
			t.Errorf("target %q was not read as defined", want)
		}
	}
	for _, reject := range []string{"VERSION", ".PHONY", "eval-gates:", "# eval-gates"} {
		if got[reject] {
			t.Errorf("%q was read as a target but is not one", reject)
		}
	}
}

func TestInvokedTargetsFindsEveryForm(t *testing.T) {
	ci := strings.Join([]string{
		"        run: make module-check",
		"        run: make eval-gates PYTHON=python3",
		"        run: make -C core check",
		"        run: |",
		"          make domain-check && make test",
		"          # make never-runs",
		"        run: echo make not-a-run",
	}, "\n")
	got := invokedTargets(ci)
	for _, want := range []string{"module-check", "eval-gates", "domain-check", "test"} {
		if !got[want] {
			t.Errorf("invocation of %q was missed", want)
		}
	}
	if got["never-runs"] {
		t.Error("a commented-out make invocation was counted as running")
	}
	if got["not-a-run"] {
		t.Error("`echo make not-a-run` was counted as invoking make")
	}
}

// A -check target that is NOT in the table and NOT self-exempt is reverse
// drift: ci.yml runs something gate-shaped the plan never promised. The rule
// has to fire, or the table stops describing the set of gates anyone runs.
func TestAnUnpromisedCheckTargetIsReported(t *testing.T) {
	ci := repoCI() + "        run: make stray-check\n"
	err := check(writeRepo(t, repoMakefile(), ci))
	if err == nil {
		t.Fatal("ci.yml ran a gate-shaped target the table does not promise, and the check passed")
	}
	if !strings.Contains(err.Error(), "stray-check") {
		t.Errorf("the report does not name the stray gate: %v", err)
	}
}

// ...but the checker's own target is exempt, with a reason. Without the
// exemption every run of the real repository would red on ci-gate-check
// itself, and the temptation would be to delete the rule.
func TestTheCheckerExemptsItself(t *testing.T) {
	if _, ok := SelfExempt["ci-gate-check"]; !ok {
		t.Fatal("ci-gate-check is not self-exempt, so wiring this checker into CI would red on itself")
	}
	for target, why := range SelfExempt {
		if strings.TrimSpace(why) == "" {
			t.Errorf("self-exempt %q has no reason recorded", target)
		}
	}
}

// A target that merely ends in a word, not a gate shape, is not reverse
// drift. Otherwise every `make test-e2e` in a workflow would be reported.
func TestANonGateTargetIsNotReported(t *testing.T) {
	ci := repoCI() + "        run: make test-e2e\n        run: make help\n"
	if err := check(writeRepo(t, repoMakefile(), ci)); err != nil {
		t.Fatalf("a workflow running non-gate targets was reported as drift: %v", err)
	}
}

// The plan's own acceptance line names exactly four gates, and Gates() is the
// table that answers "did that line survive". Folding a decision-owned gate
// into it would make the count stop matching the plan, and the number is
// quoted often enough that it has to keep matching.
func TestGatesIsExactlyThePlansFour(t *testing.T) {
	want := map[string]bool{
		"module-check":            true,
		"eval-gates":              true,
		"module-standalone-check": true,
		"domain-check":            true,
	}
	got := Gates()
	if len(got) != len(want) {
		t.Fatalf("Gates() has %d entries, want the plan's 4: %v", len(got), gateNames())
	}
	for _, g := range got {
		if !want[g.Target] {
			t.Errorf("Gates() contains %q, which the plan's acceptance line does not name", g.Target)
		}
	}
}

// Decision 153 built broker-pin-check to own the shipped-versus-tested broker
// property, and decision 164 wired it into CI. If it drops out of the table,
// the reverse-drift rule stops watching it the moment it also leaves ci.yml,
// and the property goes back to having no owner.
func TestBrokerPinCheckIsADecisionGateWithAReason(t *testing.T) {
	var found bool
	for _, g := range DecisionGates() {
		if g.Target != "broker-pin-check" {
			continue
		}
		found = true
		if strings.TrimSpace(g.Why) == "" {
			t.Error("broker-pin-check is a decision gate with no reason recorded")
		}
	}
	if !found {
		t.Fatal("broker-pin-check is not in DecisionGates(); the property decision 153 built has no owner in the gate table")
	}
}

// A decision gate that is not wired is caught exactly like a plan gate. The
// table split is about which promise a gate answers to, not about how strict
// the wiring check is.
func TestADroppedDecisionGateIsReported(t *testing.T) {
	ci := strings.Replace(repoCI(), "        run: make broker-pin-check\n", "        run: make help\n", 1)
	err := check(writeRepo(t, repoMakefile(), ci))
	if err == nil {
		t.Fatal("ci.yml no longer runs broker-pin-check, and the check passed")
	}
	if !strings.Contains(err.Error(), "broker-pin-check") {
		t.Errorf("the report does not name the dropped decision gate: %v", err)
	}
}
