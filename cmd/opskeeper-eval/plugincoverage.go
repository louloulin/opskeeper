package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"
	"github.com/vincent-wuhan/opskeeper/core/harness/schema"
)

// opskeeper-eval plugin-coverage — the answer to "can the fleet we ship
// actually pass the cases we score it on".
//
// The 20 golden cases name capabilities as `<family>.<method>`, and the
// fleet's capabilities come from plugin packages. Nothing joined the two
// vocabularies, so a case could score a permanent zero and the leaderboard
// would report it as the agent being bad rather than as the case being
// unpassable. This subcommand prints the join, names the reason for every
// gap, and can fail a build on it.
//
// The join is on the tool, not the family, and that changed what this
// command says about the repository. It used to compare a case's family
// against the families the packages serve, and it read 20/20: every case
// fully covered by the shipped fleet. That number was an artifact. The
// read-only packages ship pg.lock_waits, so pg.kill_session counted as
// covered by it, and the same inference hid every other remediation
// expectation in the suite — which is to say it hid every write the fleet
// has deliberately not shipped yet. Zero of the twenty cases were actually
// coverable.
//
// The report now says so, and says which method is missing rather than
// which family, because "0/20" is a number somebody argues with and
// "pg.kill_session is not packaged" is a package somebody writes.

type coverageFlags struct {
	casesDir   string
	pluginsDir string
	filter     string
	failOnGap  bool
	jsonOut    bool
}

func cmdPluginCoverage(_ context.Context, args []string) error {
	var f coverageFlags
	fs := flag.NewFlagSet("plugin-coverage", flag.ExitOnError)
	fs.StringVar(&f.casesDir, "cases-dir", "core/harness/cases", "golden case 目录")
	fs.StringVar(&f.pluginsDir, "plugins-dir", "plugins/pig-ops", "插件包目录")
	fs.StringVar(&f.filter, "filter", "", "只检查匹配的 case（如 host/ 或 pg/）")
	fs.BoolVar(&f.failOnGap, "fail-on-gap", false, "存在未被覆盖的 case 期望时以非零码退出")
	fs.BoolVar(&f.jsonOut, "json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runPluginCoverage(f, os.Stdout)
}

func runPluginCoverage(f coverageFlags, out *os.File) error {
	plugins, err := pluginmanifest.LoadAll(f.pluginsDir)
	if err != nil {
		return fmt.Errorf("load plugins from %s: %w", f.pluginsDir, err)
	}
	loader := schema.NewLoader(f.casesDir)
	cases, err := loader.LoadAll()
	if err != nil {
		return fmt.Errorf("load cases from %s: %w", f.casesDir, err)
	}
	if f.filter != "" {
		kept := cases[:0]
		for _, c := range cases {
			if strings.Contains(c.ID, f.filter) {
				kept = append(kept, c)
			}
		}
		cases = kept
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })

	reports := make([]pluginmanifest.CaseCoverage, 0, len(cases))
	for _, c := range cases {
		// The expectations that matter are both halves: a case that can
		// diagnose but not remediate is as unpassable as one that can do
		// neither, and counting only root causes would hide the second.
		expectations := append(append([]string{}, c.Expect.RootCauseLines...), c.Expect.RemediationOptions...)
		reports = append(reports, pluginmanifest.CoverageOf(c.ID, expectations, plugins))
	}

	if f.jsonOut {
		return emitCoverageJSON(out, plugins, reports)
	}
	emitCoverageText(out, plugins, reports)

	gaps := 0
	for _, r := range reports {
		if !r.Complete() {
			gaps++
		}
	}
	if f.failOnGap && gaps > 0 {
		return fmt.Errorf("%d of %d cases name capabilities no plugin package provides", gaps, len(reports))
	}
	return nil
}

func emitCoverageText(out *os.File, plugins []pluginmanifest.Plugin, reports []pluginmanifest.CaseCoverage) {
	fmt.Fprintf(out, "Plugin capability coverage\n")
	fmt.Fprintf(out, "  packages: %d (%s)\n", len(plugins), strings.Join(pluginNames(plugins), ", "))
	total, complete := 0, 0
	for _, r := range reports {
		total++
		mark := "GAP "
		if r.Complete() {
			mark = "ok  "
			complete++
		}
		fmt.Fprintf(out, "  %s %-28s", mark, r.CaseID)
		if r.Complete() {
			fmt.Fprintf(out, "served by %s\n", strings.Join(r.Packages, ", "))
			continue
		}
		fmt.Fprintf(out, "uncovered: %d\n", len(r.Uncovered))
		for _, u := range r.Uncovered {
			fmt.Fprintf(out, "        %-28s %s\n", u, reasonFor(r, u))
		}
	}
	fmt.Fprintf(out, "\n%d/%d cases fully covered by the shipped plugin fleet\n", complete, total)
	if complete < total {
		// Every case here names a remediation as well as a diagnosis, and a
		// case whose remediation no package ships cannot be passed by any
		// agent, however good. Saying so is the entire point of the command:
		// the leaderboard reports the same run as a zero and calls it the
		// agent's score.
		//
		// What is missing is not phase D's B3 batch — that is restart_service
		// and the config changes, and the repair package ships all five of
		// those. What is missing is the middleware write half: the adapters
		// have implemented pg.kill_session and k8s.drain and the rest for a
		// long time, and no package has ever declared them. The case files
		// are ahead of the fleet, not the other way round, and each line
		// above names a tool that could be packaged rather than a case that
		// ought to be deleted.
		fmt.Fprintf(out, "\nThe gaps above are structural, not model failures. A case naming a\n"+
			"remediation no package ships scores zero on every run, and a leaderboard\n"+
			"reads that as the agent being bad.\n\n"+
			"Every shipped package is read-only, and these are the middleware writes:\n"+
			"the control plane's adapters already implement them, so each line above\n"+
			"names a tool that could be packaged rather than a case that ought to go.\n")
	}
}

func emitCoverageJSON(out *os.File, plugins []pluginmanifest.Plugin, reports []pluginmanifest.CaseCoverage) error {
	type jsonReport struct {
		CaseID    string   `json:"case_id"`
		Complete  bool     `json:"complete"`
		Packages  []string `json:"packages"`
		Uncovered []string `json:"uncovered"`
		Reasons   []string `json:"reasons,omitempty"`
	}
	body := struct {
		Packages []string     `json:"packages"`
		Cases    []jsonReport `json:"cases"`
	}{Packages: pluginNames(plugins)}
	for _, r := range reports {
		jr := jsonReport{CaseID: r.CaseID, Complete: r.Complete(), Packages: r.Packages, Uncovered: r.Uncovered}
		for _, u := range r.Uncovered {
			jr.Reasons = append(jr.Reasons, reasonFor(r, u))
		}
		body.Cases = append(body.Cases, jr)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(body)
}

// reasonFor is the method-level reason for one uncovered expectation.
//
// CaseCoverage computes these where the fleet is known, which is the only
// place the answer can distinguish "this family ships, this method does
// not" from "this family is the control plane's". The fallback keeps the
// report total if a reason is ever missing rather than dropping the line:
// an unexplained gap is the one failure mode this whole command exists to
// prevent, and it should not be reachable by a slice index.
func reasonFor(r pluginmanifest.CaseCoverage, uncovered string) string {
	for i, u := range r.Uncovered {
		if u == uncovered && i < len(r.Reasons) {
			return r.Reasons[i]
		}
	}
	return pluginmanifest.CoverageReason(uncovered)
}

func pluginNames(plugins []pluginmanifest.Plugin) []string {
	out := make([]string, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.Name())
	}
	sort.Strings(out)
	return out
}
