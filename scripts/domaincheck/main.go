// Command domaincheck enforces the domain boundaries inside the control
// plane's monolith.
//
// The Go module system and go-arch-lint both stop at the module and the
// layer. Inside core/manager neither can see a domain, because the
// components in .go-arch-lint.yml are named after layers (manager_biz,
// manager_model, ...) rather than after what the code is about. The
// consequence is concrete: biz/alert importing biz/loop is
// manager_biz -> manager_biz, which every rule allows, so seven pairs of
// domains in the tree can already reach each other in both directions and
// nothing reports it.
//
// That is the whole argument for this tool. A mutual pair is the definition
// of two things that cannot evolve independently: a change to one is a
// change to the other, and whoever makes it has to read both. Today that is
// invisible, so it is also invisible in review.
//
// What this checker does:
//
//   - derives the domain of every package under core/manager from its path,
//     collapsing the five layer trees onto one name (biz/alert and
//     model/alert are both "alert"), so a domain-shaped context like iam
//     comes out as one domain rather than five;
//   - requires every cross-domain import to be a declared edge, with a
//     reason, in the table below;
//   - requires every pair of domains that reach each other both ways to be
//     a declared cycle, with a reason;
//   - requires the tables themselves to be true: an entry nothing uses is a
//     violation, because a stale reason is worse than no reason — it reads
//     as a justification for something that is no longer true.
//
// Test files are excluded from the rules, matching .go-arch-lint.yml's own
// excludeFiles, and their cross-domain count is printed instead. A test
// reaching across a domain boundary to wire two real things together is
// the boundary being exercised, not broken.
//
// Usage:
//
//	go run ./scripts/domaincheck [repo-root]
//
// Every violation found is printed, not just the first.
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// managerPrefix is the module this checker is about.
const managerPrefix = "github.com/vincent-wuhan/opskeeper/core/manager/"

// layerDirs are the trees a domain's code is scattered across. A path whose
// first segment is one of them names its domain in the second segment;
// anything else is already domain-shaped and names itself.
var layerDirs = map[string]bool{
	"biz": true, "server": true, "data": true, "model": true, "service": true,
}

// edge is one declared dependency from one domain to another.
type edge struct{ from, to string }

// rules is the declared shape of the tree. It is a value rather than a pile
// of package-level maps so that the checker can be exercised against a
// fixture: a test that can only ever run against the shipped tree cannot
// tell "the rule is right" from "the tree happens to fit".
type rules struct {
	shared map[string]string
	edges  map[edge]string
	cycles map[[2]string]string
}

// sharedDomains may be depended on by any domain without a declaration.
//
// "Shared" is not a compliment here, it is a statement about what the tree
// is allowed to know: each of these is a component in
// .go-arch-lint.yml whose mayDependOn names no bounded context, which is
// what makes reaching for it safe. They still have to declare their own
// outbound edges — being depended upon is a privilege, not an exemption.
var sharedDomains = map[string]string{
	"pkg":           "errors, tenant context, credentials, the audit port: no BC imports at all",
	"dataguard":     "field sensitivity classification, shared by authz and the report readers",
	"knowledge":     "the knowledge base and its git-backed model",
	"middleware":    "the HTTP middleware chain the handlers are wrapped in",
	"control":       "cross-cutting control: incidents, repair previews",
	"observability": "tracing and metric exposition",
	"agentteams":    "the cross-language worker protocol",
	"migrate":       "schema migration",
	"migrator":      "migration steps",
	"higress":       "the gateway configuration surface",
}

// edges is every cross-domain import that exists on purpose.
//
// Each reason says what the dependency is *for*, not that it is
// convenient. A domain that cannot say why it reaches into another one is a
// domain that should not be reaching.
var edges = map[edge]string{
	{"agentteams", "alert"}: "a worker's finding has to land in the same alert rows the platform shows and be judged by the same rules; a second alert vocabulary would be a second thing to page on",
	{"agentteams", "mcp"}:   "the middleware that authenticates an AgentTeams worker over MCP lives in server/mcp; splitting it would mean two auth chains for one protocol",
	{"aiops", "alert"}:      "the agent raises and silences alerts through the platform's rules rather than carrying a second alert implementation",
	{"aiops", "approval"}:   "a remediation the agent wants to run is queued in the approval domain, which is the HITL path it must not be able to route around",
	{"aiops", "audit"}:      "the agent kernel's LedgerWriter writes agent actions (tool calls, turns) into the same chain an operator reads",
	{"aiops", "device"}:     "an alert names a device and a tool call resolves it to a machine; the agent needs the device vocabulary to say which one",
	{"aiops", "edge"}:       "the agent's tools address nodes through the edge domain; there is no second worth having notion of 'which node'",
	{"aiops", "hitl"}:       "an investigation that needs a human hands the request to the human-in-the-loop domain instead of blocking on a channel of its own",
	{"aiops", "loop"}:       "the investigation loop is the agent's own driver and lives in aiops/loop",
	{"aiops", "skill"}:      "host skills are executed as tools, so the agent's tool bag is assembled from the skill registry",
	{"aiops", "topology"}:   "correlation answers 'what is related to this' from the topology domain instead of a private graph",

	{"aiopsconfig", "aiops"}: "the config service assembles the agent's alert-config and tool surfaces: it configures aiops rather than reimplementing it",
	{"aiopsconfig", "alert"}: "the agent's settings endpoints resolve alert configuration through the alert service",

	{"alert", "aiops"}: "an alert's investigation is driven by the agent's chat runtime; the alert domain asks the agent rather than embedding a second runtime",
	{"alert", "edge"}:  "an alert is raised against a node, and acknowledging it has to update that node's state",

	{"chatdiagnose", "aiops"}: "chat diagnosis runs on the agent's chat runtime",
	{"chatdiagnose", "audit"}: "promoting a chat into an investigation is an operator action and belongs in the chain",
	{"chatdiagnose", "loop"}:  "promoting a chat hands the work to the loop domain, which owns the investigation; the reverse of that edge used to exist because the loop wrote the knowledge base's own rows (decision 114)",

	{"demo", "alert"}: "the scenario seeds and narrates real incident rows, so it writes the production alert model rather than a fixture of it. The alert side asks the scenario whether it owns a firing through a correlator port instead of importing it back (decision 113)",

	{"edge", "device"}: "the edge register flow resolves, creates and updates the host Device behind a node (biz/edge, server/edge). One direction only: a device deletion reaches the edge identities through a revoker the composition root injects rather than by importing them (decision 112)",

	{"flow", "scheduler"}: "a flow step schedules work through the scheduler domain",

	{"frontierbound", "audit"}:  "autonomy replay writes the decisions a node made on its own back into the chain when the tunnel returned (decision 101)",
	{"frontierbound", "edge"}:   "the frontier is the tunnel's node-facing side: it reads node state and change events",
	{"frontierbound", "metric"}: "the tunnel heartbeat answers carry metric snapshots",

	{"grafana", "monitor"}: "grafana monitors are configured from the monitor model",
	{"grafana", "setting"}: "grafana's endpoint and credentials are platform settings",

	{"hitl", "aiops"}:    "an approval's target is often an agent remediation, so the policy names the agent vocabulary",
	{"hitl", "approval"}: "HITL is the approval domain's policy layer and the two share one model",

	{"imbridge", "aiops"}: "the IM bridge delivers an agent finding into a chat channel, so it formats the agent's output",
	{"imbridge", "iam"}:   "the bridge attributes a message to a user, and a model is the one thing two contexts are meant to agree on rather than copy",

	{"integration", "grafana"}: "the integration tests build a real grafana client against a real endpoint",

	{"loop", "aiops"}:  "the loop drives the agent kernel and builds its prompts through the agent's own prompt guard and base-tool contracts",
	{"loop", "alert"}:  "an investigation starts from an alert and closes it, so the loop reads and updates alert state",
	{"loop", "report"}: "a finished investigation produces its output through the report domain",

	{"marketplace", "aiops"}:        "the marketplace lists what an agent can install, which is the agent's tool vocabulary",
	{"marketplace", "pluginimport"}: "installing from the marketplace is the plugin-import domain's job",

	{"mcp", "aiops"}: "an MCP tool call is observed through the agent's tool decorators, so its receipt carries the same gate events a native call does",
	{"mcp", "loop"}:  "an investigation started over MCP enters the same loop as a chat one",

	{"middleware", "audit"}: "the audit middleware is the only thing that turns a handler's request into a write to the chain",

	{"nodeagent", "nodefleet"}: "the node-agent endpoints are the fleet's session handles",

	{"pluginimport", "aiops"}: "an imported plugin becomes part of the agent's tool surface, which is assembled in the chat runtime",

	{"report", "aiops"}: "a report is produced out of an agent conversation",
	{"report", "loop"}:  "a report can also be produced out of a loop investigation",

	{"systemhealth", "alert"}: "the health summary counts active alerts through the alert service",
	{"systemhealth", "edge"}:  "the health summary reports node reachability from the edge domain",

	{"webshell", "device"}: "a terminal session is opened against a device record",
	{"webshell", "edge"}:   "the webshell reaches the node through the edge transport",
}

// pair canonicalises a mutual pair so that the table can be written in
// either order. Map iteration produces the pair as (from, to) in whichever
// direction some package happened to import the other first, and a table
// that only matches one of the two spellings is a table that fails on an
// unrelated reordering.
func pair(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// cycles are the domain pairs that already reach each other both ways.
//
// Every one of these is debt, and none of them is a design. They are listed
// because the alternative is a checker that goes red on the day it is
// written, which is a checker people turn off. Each reason says what the
// cycle is made of today, so the next person can see what would have to
// change to cut it.
var cycles = map[[2]string]string{
	{"aiops", "alert"}: "the agent raises alerts and the alert domain asks the agent to investigate them; cutting this means alerts dispatch an investigation id instead of calling a runtime",
	{"aiops", "hitl"}:  "the agent requests a human and the approval policy names agent remediations; cutting this means the policy reads a remediation descriptor rather than the agent's vocabulary",
	{"aiops", "loop"}:  "the agent kernel drives the loop and the loop builds the agent's prompts; this is the largest cycle in the tree and the reason the registry seam (decision 104) exists",
	{"loop", "report"}: "the loop produces a report and a report is produced from a loop investigation; cutting this means the report domain subscribes rather than being called",
}

// defaultRules is the shipped boundary.
func defaultRules() rules {
	return rules{shared: sharedDomains, edges: edges, cycles: cycles}
}

// source is one parsed file: where it is, and what it imports.
type source struct {
	path    string
	imports []string
	test    bool
}

// check reports every way the tree disagrees with the rules.
func check(sources []source, r rules) []string {
	var violations []string

	// observed collects what the tree actually does, so the tables can be
	// checked in the other direction too.
	observed := map[edge]bool{}
	domains := map[string]bool{}

	for _, src := range sources {
		from := domainOf(src.path)
		if from == "" {
			continue
		}
		domains[from] = true
		if src.test {
			continue
		}
		for _, imp := range src.imports {
			to := domainOf(imp)
			if to == "" || to == from {
				continue
			}
			domains[to] = true
			if r.shared[to] != "" {
				continue
			}
			observed[edge{from, to}] = true
		}
	}

	for e := range observed {
		if _, ok := r.edges[e]; !ok {
			violations = append(violations, fmt.Sprintf(
				"%s imports %s, which is not a declared domain edge. Either this is a seam that "+
					"should not exist, or it needs a reason in scripts/domaincheck/main.go",
				e.from, e.to))
		}
	}
	for e := range r.edges {
		if !observed[e] {
			why := r.edges[e]
			if len(why) > 60 {
				why = why[:60] + "..."
			}
			violations = append(violations, fmt.Sprintf(
				"%s -> %s is declared but no longer happens (%s). Delete the entry and the reason "+
					"with it: a stale justification is worse than none", e.from, e.to, why))
		}
	}

	// A pair that reaches each other both ways is a cycle, whether or not
	// anyone declared it. The observation is directional and the table key
	// is canonical, so the test is "is the reverse edge also observed".
	mutual := map[[2]string]bool{}
	for e := range observed {
		if observed[edge{from: e.to, to: e.from}] {
			mutual[pair(e.from, e.to)] = true
		}
	}
	for p := range mutual {
		if _, ok := r.cycles[p]; !ok {
			violations = append(violations, fmt.Sprintf(
				"%s and %s now reach each other both ways. That is a cycle between two things that "+
					"therefore cannot evolve independently. Cut one direction, or declare the pair in "+
					"cycles with what it would take to cut it", p[0], p[1]))
		}
	}
	for p, why := range r.cycles {
		if !mutual[p] {
			if len(why) > 60 {
				why = why[:60] + "..."
			}
			violations = append(violations, fmt.Sprintf(
				"%s and %s are declared as a cycle but no longer reach each other both ways (%s). "+
					"Delete the entry: the debt is paid or the edge moved", p[0], p[1], why))
		}
	}

	// A domain that is declared shared but is gone, or a shared tree that
	// turned into a normal domain, is the same class of stale table.
	for name := range r.shared {
		if !domains[name] {
			violations = append(violations, fmt.Sprintf(
				"%s is declared a shared domain but no package belongs to it; delete the entry", name))
		}
	}

	sort.Strings(violations)
	return violations
}

// domainOf maps an import path to the domain that owns it, or "" when the
// path is outside the manager module.
//
// A package sitting directly in a layer directory (core/manager/biz, with
// no domain segment) comes back as the layer's own name. It is almost
// certainly a mistake — no domain is one layer — but returning "" for it
// would exempt its imports from every rule in this file, and a boundary
// check that a misplaced file can switch off is not a boundary check.
func domainOf(path string) string {
	if !strings.HasPrefix(path, managerPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, managerPrefix)
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		return ""
	}
	if layerDirs[parts[0]] && len(parts) > 1 {
		return parts[1]
	}
	return parts[0]
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	sources, stats, err := parseTree(filepath.Join(root, "core", "manager"), defaultRules())
	if err != nil {
		fmt.Fprintln(os.Stderr, "domaincheck: "+err.Error())
		os.Exit(2)
	}

	r := defaultRules()
	violations := check(sources, r)
	fmt.Printf("domaincheck: %d domains, %d shared, %d declared edges, %d declared cycles, "+
		"%d test-only cross-domain imports (excluded, as in .go-arch-lint.yml)\n",
		stats.domains, len(r.shared), len(r.edges), len(r.cycles), stats.testOnlyEdges)

	if len(violations) == 0 {
		fmt.Println("domaincheck: every domain boundary holds")
		return
	}
	fmt.Fprintf(os.Stderr, "domaincheck: %d domain violation(s):\n", len(violations))
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, "  "+v)
	}
	os.Exit(1)
}

type treeStats struct {
	domains       int
	testOnlyEdges int
}

func parseTree(dir string, r rules) ([]source, treeStats, error) {
	fset := token.NewFileSet()
	var sources []source
	domains := map[string]bool{}
	testOnly := 0

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		importPath := managerPrefix + filepath.ToSlash(rel)
		importPath = strings.TrimSuffix(importPath, ".go")
		isTest := strings.HasSuffix(path, "_test.go")
		src := source{path: importPath, test: isTest}
		for _, imp := range file.Imports {
			if v, err := strconv.Unquote(imp.Path.Value); err == nil {
				src.imports = append(src.imports, v)
			}
		}
		sources = append(sources, src)
		if d := domainOf(importPath); d != "" {
			domains[d] = true
		}
		if isTest {
			from := domainOf(importPath)
			for _, imp := range src.imports {
				to := domainOf(imp)
				if to != "" && to != from && r.shared[to] == "" {
					testOnly++
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, treeStats{}, err
	}
	if len(sources) == 0 {
		return nil, treeStats{}, fmt.Errorf("no Go files under %s; the walk is broken, not the boundaries", dir)
	}
	return sources, treeStats{domains: len(domains), testOnlyEdges: testOnly}, nil
}
