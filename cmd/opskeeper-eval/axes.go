package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/harness/schema"
)

// opskeeper-eval axes — what a case declares about the three diagnostic axes.
//
// The judge scores Localization × Identification × Reason (arXiv:2606.29193);
// this command answers the prior question, which is whether the corpus
// declares enough for those axes to mean anything. A case that declares no
// locus produces no localization number at all, and an axis nobody declared is
// an axis a leaderboard silently averages over nothing.
//
// The three declarations are derived, not asked for in a separate field, and
// the derivation is the whole point of this file: it is the one place where
// "where the fault is" is defined for the corpus. The three sources are the
// case's own authored material, so no case has to be rewritten to be scored:
//
//	Localization   the case id's family segment (`pg/lock-waits` → `pg`) plus
//	               the injection's identity parameters (table: orders,
//	               namespace: test, topic: order.events, …). The parameters
//	               come from schema.InjectStep.Params, which the loader only
//	               started filling in this change — before it, every
//	               localization would have been the family alone.
//	Identification the case id's fault segment, tokenized (`lock-waits` →
//	               ["lock", "waits"])
//	Reason         the case's expected root-cause lines — the observations a
//	               grounded reasoning trace has to contain
//
// Why the case id and not the injection type: the id is the authored name of
// the fault ("lock waits"), while the injection type names the script that
// produced it (`pg.inject_lock_chain`, `pg.begin_txn_hold` for
// `pg/long-running-tx`). Which script ran is an implementation detail of the
// injector; the fault's name is what the corpus promises the agent will find.
//
// Five of the twenty shipped cases name no sub-resource at all: the fault was
// injected on "the host", "the replica", "the redis instance", and the case
// says nothing narrower. Their localization is measured on the family alone
// and reported as coarsened (`~` in the output) rather than propped up with a
// name the injector never used.
//
// A token shorter than three characters is dropped. Two-character fragments
// like the `tx` in `long-running-tx` are not words an agent can be held to —
// "transaction" does not contain "tx" — and keeping them would fail a correct
// answer for a spelling reason.
const minimumAxisTokenLen = 3

// locusIdentityKeys are the injection parameter keys whose values name a
// resource rather than a knob.
//
// The distinction is what makes localization discriminating: `table: orders`
// names the thing the fault happened to and can be missed, while `cores: 4`
// and `target_load: 98` describe how hard the fault was driven and are
// numbers a correct answer has no reason to repeat. A new case that injects
// through a key not listed here still scores — it just scores on its family —
// and `axes` prints the token count, so the coarseness is visible rather than
// assumed.
var locusIdentityKeys = []string{
	"namespace", "deployment", "pod", "pvc", "node", "target_node",
	"service", "host", "instance", "database",
	"table", "tables", "topic", "consumer_group", "queue",
	"broker_id", "key", "path",
}

// diagnosticExpectations is one case's declaration of the three axes.
type diagnosticExpectations struct {
	CaseID    string   `json:"case_id"`
	Locus     []string `json:"locus"`
	FaultType []string `json:"fault_type"`
	Evidence  []string `json:"evidence"`
	// Thin marks a case whose locus is the family alone. Such a case still
	// scores, but every answer that names its own resource family scores 1.0,
	// so the number carries almost nothing. It is reported rather than fixed
	// by inventing a target the injector never used.
	Thin bool `json:"thin,omitempty"`
}

// diagnosticExpectationsOf derives the three axes from a case.
func diagnosticExpectationsOf(c *schema.Case) diagnosticExpectations {
	out := diagnosticExpectations{CaseID: c.ID}
	// The family is not put through axisTokens: the two-character families
	// ("pg", "mq") are exactly the ones the minimum-length rule exists to drop
	// from prose, and dropping the family would leave those cases with no
	// locus at all.
	out.Locus = append(out.Locus, strings.ToLower(strings.TrimSpace(caseFamily(c.ID))))
	for _, param := range c.Inject {
		for _, key := range locusIdentityKeys {
			value, ok := param.Params[key]
			if !ok {
				continue
			}
			for _, token := range scalarTokens(value) {
				out.Locus = append(out.Locus, token)
			}
		}
	}
	out.Locus = uniqueTokens(out.Locus)

	out.FaultType = uniqueTokens(axisTokens(caseFaultName(c.ID)))
	// The family token alone is a locus no answer can miss.
	out.Thin = len(out.Locus) <= 1
	out.Evidence = append([]string(nil), c.Expect.RootCauseLines...)
	return out
}

// caseFamily is the first path segment of the case id.
func caseFamily(id string) string {
	if index := strings.IndexByte(id, '/'); index > 0 {
		return id[:index]
	}
	return ""
}

// caseFaultName is the last path segment of the case id.
func caseFaultName(id string) string {
	if index := strings.LastIndexByte(id, '/'); index >= 0 {
		return id[index+1:]
	}
	return id
}

// axisTokens lower-cases, splits on separators and drops fragments too short
// to be words.
func axisTokens(raw string) []string {
	fields := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == '.' || r == '/'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]rune(field)) < minimumAxisTokenLen {
			continue
		}
		out = append(out, field)
	}
	return out
}

// scalarTokens flattens an injection parameter value into strings. Booleans
// are dropped: `simulate_network_partition: true` is a mode, not a name.
func scalarTokens(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case int:
		return []string{strconv.Itoa(typed)}
	case int64:
		return []string{strconv.FormatInt(typed, 10)}
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, scalarTokens(item)...)
		}
		return out
	default:
		return nil
	}
}

func uniqueTokens(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		lowered := strings.ToLower(strings.TrimSpace(token))
		if lowered == "" || seen[lowered] {
			continue
		}
		seen[lowered] = true
		out = append(out, lowered)
	}
	return out
}

type axesFlags struct {
	casesDir       string
	filter         string
	jsonOut        bool
	failUnmeasured bool
}

func cmdAxes(_ context.Context, args []string) error {
	var f axesFlags
	fs := flag.NewFlagSet("axes", flag.ExitOnError)
	fs.StringVar(&f.casesDir, "cases-dir", "core/harness/cases", "golden case 目录")
	fs.StringVar(&f.filter, "filter", "", "只检查匹配的 case（如 host/ 或 pg/）")
	fs.BoolVar(&f.jsonOut, "json", false, "输出 JSON")
	fs.BoolVar(&f.failUnmeasured, "fail-on-unmeasured-axis", false,
		"存在无法测量任一诊断轴的 case 时以非零码退出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runAxes(f, os.Stdout)
}

type axesReport struct {
	Cases      []diagnosticExpectations `json:"cases"`
	Total      int                      `json:"total"`
	Thin       int                      `json:"thin_locus"`
	Unmeasured int                      `json:"unmeasured"`
}

func runAxes(f axesFlags, out *os.File) error {
	cases, err := schema.NewLoader(f.casesDir).LoadAll()
	if err != nil {
		return fmt.Errorf("load cases from %s: %w", f.casesDir, err)
	}
	report := axesReport{Cases: make([]diagnosticExpectations, 0, len(cases))}
	for _, c := range cases {
		if f.filter != "" && !strings.Contains(c.ID, f.filter) {
			continue
		}
		expectations := diagnosticExpectationsOf(c)
		report.Cases = append(report.Cases, expectations)
		report.Total++
		if expectations.Thin {
			report.Thin++
		}
		// An axis is unmeasured when the case declares nothing that could
		// produce it. Reason is the one axis whose source the schema already
		// requires, so it cannot be missing unless the case itself is broken —
		// which is exactly why its absence is worth failing on.
		if len(expectations.Locus) == 0 || len(expectations.FaultType) == 0 || len(expectations.Evidence) == 0 {
			report.Unmeasured++
		}
	}
	sort.Slice(report.Cases, func(i, j int) bool { return report.Cases[i].CaseID < report.Cases[j].CaseID })

	if f.jsonOut {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(encoded))
	} else {
		fmt.Fprintf(out, "diagnostic axes: Localization × Identification × Reason (2606.29193)\n")
		fmt.Fprintf(out, "cases: %d   coarsened locus (family only): %d   unmeasured: %d\n\n",
			report.Total, report.Thin, report.Unmeasured)
		for _, item := range report.Cases {
			marker := " "
			if item.Thin {
				marker = "~"
			}
			fmt.Fprintf(out, "%s %-26s locus=%v type=%v evidence=%d\n",
				marker, item.CaseID, item.Locus, item.FaultType, len(item.Evidence))
		}
		fmt.Fprintf(out, "\n~ marks a case whose environment names no sub-resource; it scores on its\n"+
			"  family alone, which every answer naming that family satisfies.\n")
	}

	if f.failUnmeasured && report.Unmeasured > 0 {
		return fmt.Errorf("axes: %d of %d cases declare nothing for at least one diagnostic axis",
			report.Unmeasured, report.Total)
	}
	return nil
}
