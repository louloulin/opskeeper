package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/harness/schema"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/pluginmanifest"
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
// The honest state of the repository today is in the report itself: the
// host cases are covered by the read-only package, and the middleware
// cases (pg / redis / k8s / mq) are not covered at all because those
// adapters live in the control plane as BaseTools and were never packaged.
// Saying that out loud is the point — a coverage number that hid it would
// be a number nobody could act on.

type coverageFlags struct {
	casesDir      string
	pluginsDir    string
	filter        string
	failOnGap     bool
	failIfPartial bool
	jsonOut       bool
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
			fmt.Fprintf(out, "        %-28s %s\n", u, pluginmanifest.CoverageReason(u))
		}
	}
	fmt.Fprintf(out, "\n%d/%d cases fully covered by the shipped plugin fleet\n", complete, total)
	if complete < total {
		fmt.Fprintf(out, "\nThe gaps above are structural: a case whose family no package serves scores zero\n"+
			"on every run, and the leaderboard reports that as the agent being bad.\n")
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
			jr.Reasons = append(jr.Reasons, pluginmanifest.CoverageReason(u))
		}
		body.Cases = append(body.Cases, jr)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(body)
}

func pluginNames(plugins []pluginmanifest.Plugin) []string {
	out := make([]string, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.Name())
	}
	sort.Strings(out)
	return out
}
