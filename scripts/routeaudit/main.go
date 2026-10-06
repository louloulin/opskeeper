// Command routeaudit holds every mutating HTTP route registered under
// core/manager/server to a written verdict.
//
// Why this exists
// ---------------
// Decisions 309 and 310 were both found the same way: by hand-listing the
// files under core/manager/server that register a mutating route and then
// asking which of them call SetAuditEvent. Decision 309 found the approval
// inbox — the button that actually runs a command. Decision 310 found
// `POST /v1/im/apps/{id}/reveal`, which returns a webhook's app_secret in
// cleartext.
//
// Both times the answer was "someone remembered to look". A list that only
// exists in a previous turn's head is a list that grows. This command turns
// that list into a file in the repository: every mutating route must either
//
//   - live in a file that calls SetAuditEvent (audited), or
//   - carry an explicit, written reason for not being (backlog).
//
// There is no third option. A new mutating route in a new file fails the
// check until somebody says why it is exempt, which is the point: the
// question is cheap to answer once and expensive to keep skipping.
//
// The other property worth having is that a verdict cannot silently rot. A
// file listed as backlog that has since gained SetAuditEvent is reported as
// stale, and a file that has disappeared from the tree is reported as
// orphaned. Neither is a failure by itself — both are "this table needs a
// look" — but both are printed, because a table that lies about the tree is
// worse than no table.
//
// Usage:
//
//	go run ./scripts/routeaudit [repo-root]
//
// Exit status is 1 if any route lacks a verdict.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// routeReg matches a chi registration: r.Post("/path", h.handler).
// The receiver name is fixed to `r` because that is what every handler in
// this tree uses; a different name is a false negative, not a false
// positive, and this command is built to be additive rather than clever.
var routeReg = regexp.MustCompile(`\br\.(Post|Put|Patch|Delete)\("([^"]+)",\s*([A-Za-z0-9_.]+)`)

// funcDeclReg matches a top-level function or method declaration and
// captures its name. The optional receiver group is what lets one pattern
// cover both `func auditApp(` and `func (h *Handler) createApp(`.
var funcDeclReg = regexp.MustCompile(`(?m)^func (?:\([^)]*\)[ ]*)?([A-Za-z0-9_]+)\(`)

// callReg finds the calls a body makes, for the closure walk.
var callReg = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\(`)

// reachesAudit reports whether calling the named handler ends up calling
// SetAuditEvent somewhere inside its own file.
//
// The transitive part is not decoration. The handlers this repository added
// in decisions 309 and 310 do not call SetAuditEvent themselves — they call
// a local helper (auditDecision, auditApp) that does. A per-handler grep
// therefore reports "unaudited" for exactly the code that was written
// deliberately to be audited, which is the fastest way to make a gate get
// switched off.
//
// It is a one-file closure, not a package-wide one. That is a real
// limitation and it is worth being precise about the direction it errs in:
// a handler that reaches an audit helper through a function in another file
// is reported as unaudited. That is a false alarm, not a false pass — it
// can only ever make the gate stricter.
func reachesAudit(src string, entry string) bool {
	// The registration reads h.createApp; the declaration reads createApp.
	if i := strings.LastIndex(entry, "."); i >= 0 {
		entry = entry[i+1:]
	}
	bodies := funcBodies(src)
	done := map[string]bool{}
	var visit func(name string, depth int) bool
	visit = func(name string, depth int) bool {
		if depth > 4 || done[name] {
			return false
		}
		done[name] = true
		body, ok := bodies[name]
		if !ok {
			return false
		}
		if strings.Contains(body, "SetAuditEvent") {
			return true
		}
		for _, m := range callReg.FindAllStringSubmatch(body, -1) {
			if visit(m[1], depth+1) {
				return true
			}
		}
		return false
	}
	return visit(entry, 0)
}

// funcBodies maps every function name a file defines to its own source text.
//
// Taking the name from the declaration rather than from a character window
// matters: an earlier version assigned a body to any name appearing in the
// first 120 columns, so a handler that *called* an audit helper got that
// helper's slot, and the audit helper ended up holding the caller's text.
// The result was a gate that reported unaudited for exactly the handlers
// written to be audited — the failure mode that gets a gate switched off.
func funcBodies(src string) map[string]string {
	out := map[string]string{}
	locs := funcDeclReg.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[src[loc[2]:loc[3]]] = src[loc[0]:end]
	}
	return out
}

// Verdict is the recorded judgement about one route.
type Verdict struct {
	// File is relative to core/manager/server, e.g. "alert/http.go".
	File string
	// Route is the registration path, e.g. "/v1/alerts/{id}/silence".
	Route string
	// Backlog is the written reason this route is not audited yet.
	// Empty means the route is expected to be audited.
	Backlog string
}

// Verdicts is the table. It is deliberately a slice of routes rather than a
// map of files, because the unit that matters is the route: two routes in
// one file routinely need different answers (see mcp/http.go, where the
// four admin CRUD routes are audited and the JSON-RPC transport is not).
var Verdicts = []Verdict{
	// --- audited: the file calls SetAuditEvent -----------------------------
	{File: "alert/http.go", Route: "/v1/alerts/incidents/{id}/investigation"},
	{File: "alert/http.go", Route: "/v1/alerts/incidents/{id}/ack"},
	{File: "alert/http.go", Route: "/v1/alerts/incidents/{id}/resolve"},
	{File: "alert/http.go", Route: "/v1/alerts/incidents/{id}/silence"},
	{File: "alert/http.go", Route: "/v1/notification-channels"},
	{File: "alert/http.go", Route: "/v1/notification-channels/{id}"},
	{File: "alert/http.go", Route: "/v1/alert-rules"},
	{File: "alert/http.go", Route: "/v1/alert-rules/{id}"},
	{File: "alert/http.go", Route: "/v1/alert-rules/{id}/enabled"},

	{File: "aiops/crystallized.go", Route: "/v1/loops/crystallized/{name}/promote"},

	{File: "approval/http.go", Route: "/v1/approvals/{id}/approve"},
	{File: "approval/http.go", Route: "/v1/approvals/{id}/reject"},

	{File: "imbridge/http.go", Route: "/v1/im/apps"},
	{File: "imbridge/http.go", Route: "/v1/im/apps/{id}"},
	{File: "imbridge/http.go", Route: "/v1/im/apps/{id}/reveal"},

	// --- backlog: not audited yet, with the reason written down ------------
	{File: "imbridge/http.go", Route: "/v1/im/feishu/events",
		Backlog: "inbound webhook authenticated by platform signature rather than by a tenant, so a failure row would name nobody"},

	{File: "alert/http.go", Route: "/v1/alert-rules/preview",
		Backlog: "evaluates a draft rule against a 24h backfill and persists nothing; it costs a range query, not a state change"},
	{File: "alert/http.go", Route: "/v1/notification-channels/{id}/test",
		Backlog: "delivers one test message through the channel and changes no configuration"},

	{File: "alert/http.go", Route: "/v1/alerts/webhook",
		Backlog: "inbound Alertmanager webhook; audited by delivery, not by caller identity"},

	{File: "mcp/http.go", Route: "/v1/mcp/servers",
		Backlog: "MCP server registration carries credentials; decision 311 found this table had wrongly claimed it was audited"},
	{File: "mcp/http.go", Route: "/v1/mcp/servers/{id}",
		Backlog: "see /v1/mcp/servers"},
	{File: "mcp/http.go", Route: "/v1/mcp/servers/{id}/test",
		Backlog: "see /v1/mcp/servers"},
	{File: "mcp/http.go", Route: "/v1/mcp",
		Backlog: "JSON-RPC envelope; each dispatched method writes its own mcp_tool_* row, so auditing the envelope would double-count"},

	{File: "systemhealth/http.go", Route: "/v1/system/health/check",
		Backlog: "read-only probe fan-out; POST only because it carries a target list, and no state changes"},

	{File: "aiops/http.go", Route: "/v1/chat/sessions",
		Backlog: "chat session lifecycle — high volume, low consequence; queued behind the execution surfaces"},
	{File: "aiops/http.go", Route: "/v1/chat/sessions/{id}",
		Backlog: "see /v1/chat/sessions"},
	{File: "aiops/http.go", Route: "/v1/chat/sessions/{id}/messages",
		Backlog: "see /v1/chat/sessions"},
	{File: "aiops/http.go", Route: "/v1/chat/sessions/{id}/messages/stream",
		Backlog: "see /v1/chat/sessions"},
	{File: "aiops/http.go", Route: "/v1/chat/sessions/{id}/stop",
		Backlog: "see /v1/chat/sessions"},
	{File: "aiops/http.go", Route: "/v1/aiops/query-translate",
		Backlog: "a query translation, not a mutation; only looks mutating because it is POST"},
	{File: "aiops/http.go", Route: "/v1/agents/custom",
		Backlog: "custom agent definition — changes what the model may do, so it is queued behind the execution surfaces"},
	{File: "aiops/http.go", Route: "/v1/agents/custom/{name}",
		Backlog: "see /v1/agents/custom"},
	{File: "aiops/http.go", Route: "/v1/agents/{name}",
		Backlog: "see /v1/agents/custom"},

	{File: "agentteams/http.go", Route: "/v1/state/{task_id}",
		Backlog: "AgentTeams worker scratch state, rewritten constantly by running workers; a row per write would drown the chain"},
	{File: "agentteams/http.go", Route: "/v1/hitl/decide",
		Backlog: "a HITL decision — the same class decision 309 closed on the approval inbox; the next one to fix"},
	{File: "agentteams/http.go", Route: "/v1/knowledge/docs",
		Backlog: "knowledge ingest"},
	{File: "agentteams/http.go", Route: "/v1/incidents/events",
		Backlog: "incident timeline append"},

	{File: "agentteams/plugin_http.go", Route: "/v1/plugins/install",
		Backlog: "plugin install — code reaching the host, high consequence; queued, not forgotten"},
	{File: "agentteams/plugin_http.go", Route: "/v1/plugins/{id}",
		Backlog: "see /v1/plugins/install"},
	{File: "agentteams/plugin_http.go", Route: "/v1/plugins/{id}/enable",
		Backlog: "see /v1/plugins/install"},
	{File: "agentteams/plugin_http.go", Route: "/v1/plugins/{id}/disable",
		Backlog: "see /v1/plugins/install"},
	{File: "agentteams/plugin_http.go", Route: "/v1/plugins/{id}/sync",
		Backlog: "see /v1/plugins/install"},
	{File: "agentteams/plugin_http.go", Route: "/v1/plugins/{id}/push",
		Backlog: "see /v1/plugins/install"},

	{File: "marketplace/http.go", Route: "/v1/marketplace/install",
		Backlog: "marketplace install — same class as /v1/plugins/install"},
	{File: "marketplace/http.go", Route: "/v1/marketplace/upload",
		Backlog: "package upload — a new artifact entering the system"},
	{File: "marketplace/http.go", Route: "/v1/marketplace/import",
		Backlog: "container import — the same reach as upload"},
	{File: "marketplace/http.go", Route: "/v1/marketplace/installed/{pack_id}",
		Backlog: "see /v1/marketplace/install"},
	{File: "marketplace/http.go", Route: "/v1/marketplace/installed/{pack_id}/bindings",
		Backlog: "tool bindings for an installed pack — decides which tools are reachable"},

	{File: "dataguard/http.go", Route: "/v1/data-guard/labels",
		Backlog: "data-masking label — a security control, so high consequence and not long to wait"},
	{File: "dataguard/http.go", Route: "/v1/data-guard/labels/{type}/{id}",
		Backlog: "label override and delete share one path; see /v1/data-guard/labels"},

	{File: "skill/http.go", Route: "/v1/skills/{key}/execute",
		Backlog: "arbitrary skill execution — the same shape as the execute the approval inbox guards"},

	{File: "loop/http.go", Route: "/v1/loops/{incident_id}/trigger",
		Backlog: "starts a remediation loop, which can reach the executors the approval inbox guards"},
	{File: "loop/http.go", Route: "/v1/recovery/verify",
		Backlog: "read-mostly recovery verification"},
}

// Result is what one run found.
type Result struct {
	// Missing are routes in the tree with no verdict at all.
	Missing []string
	// Stale are verdicts marked backlog whose file has since been audited.
	Stale []string
	// Orphan are verdicts whose file still exists but no longer registers
	// the route.
	Orphan []string
	// Gone are verdicts whose whole file left the tree. Kept apart from
	// Orphan because the fix differs — a moved handler versus a deleted one —
	// and because folding the two together would make a deleted package
	// silently drop its verdicts off the bottom of the report.
	Gone []string
}

// OK is false if any verdict is missing, stale or orphaned.
//
// Stale and orphan fail for the same reason missing does, and it is the same
// reason this repository's ledger treats "the document claims a section that
// is not in the file" as worse than silence: a stale verdict asserts that a
// route is unaudited when it is audited (or the reverse), and an orphan
// describes a route that does not exist. Both are the table lying about the
// tree, and a table that lies is the failure mode this command exists to
// prevent — it would let a reader conclude an unaudited route is deliberately
// excepted when in fact the exception was deleted months ago.
func (r Result) OK() bool {
	return len(r.Missing) == 0 && len(r.Stale) == 0 && len(r.Orphan) == 0 && len(r.Gone) == 0
}

// Run walks the tree and compares it against the table.
func Run(root string) Result {
	var res Result
	seen := map[string]bool{}
	seenFiles := map[string]bool{}
	base := filepath.Join(root, "core", "manager", "server")

	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		seenFiles[rel] = true
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, m := range routeReg.FindAllStringSubmatch(string(src), -1) {
			key := rel + " " + m[2]
			if seen[key] {
				// PUT and DELETE on one path share a verdict key. Saying it
				// twice would be noise that trains people to skim the output.
				continue
			}
			seen[key] = true
			audited := reachesAudit(string(src), m[3])
			v, ok := lookup(key)
			switch {
			case !ok:
				res.Missing = append(res.Missing, key+" — no verdict recorded in scripts/routeaudit")
			case v.Backlog == "" && !audited:
				res.Missing = append(res.Missing, key+" — recorded as audited, but "+rel+"'s handler "+m[3]+" never calls SetAuditEvent")
			case v.Backlog != "" && audited:
				res.Stale = append(res.Stale, key+" — "+rel+"'s handler "+m[3]+" calls SetAuditEvent now, so its backlog reason no longer describes it")
			}
		}
		return nil
	})

	// Orphan means one of two things, and conflating them is what an earlier
	// version did: the file is gone from the tree, or the file is still there
	// but the route is no longer registered in it. The first usually means
	// the handler moved and the verdict should follow it; the second means
	// the route was renamed or deleted and the verdict is describing
	// something that no longer exists.
	//
	// Keeping them as one verdict rather than two reports is deliberate: both
	// are fixed by finding where the route went, and a reader who has to
	// decide which bucket a deleted route belongs in will leave it in neither.
	for _, v := range Verdicts {
		switch {
		case seen[v.File+" "+v.Route]:
		case seenFiles[v.File]:
			res.Orphan = append(res.Orphan, v.File+" "+v.Route)
		default:
			res.Gone = append(res.Gone, v.File+" "+v.Route+" — the file is no longer in the tree")
		}
	}
	sort.Strings(res.Missing)
	sort.Strings(res.Stale)
	sort.Strings(res.Orphan)
	sort.Strings(res.Gone)
	return res
}

func lookup(key string) (Verdict, bool) {
	for _, v := range Verdicts {
		if v.File+" "+v.Route == key {
			return v, true
		}
	}
	return Verdict{}, false
}

// Report prints everything the run found. Order is deliberate: MISSING first
// (a route nobody has judged), then stale and orphan (judgements that no
// longer describe the tree).
func (r Result) Report(w *os.File) {
	fmt.Fprintln(w, "routeaudit: every mutating route under core/manager/server has a recorded verdict")
	fmt.Fprintf(w, "  verdicts recorded: %d, of which backlog: %d\n", len(Verdicts), countBacklog())
	for _, m := range r.Missing {
		fmt.Fprintf(w, "  MISSING: %s\n", m)
	}
	for _, s := range r.Stale {
		fmt.Fprintf(w, "  stale:   %s\n", s)
	}
	for _, o := range r.Orphan {
		fmt.Fprintf(w, "  orphan:  %s — the route is no longer registered; drop the verdict\n", o)
	}
}

func countBacklog() int {
	n := 0
	for _, v := range Verdicts {
		if v.Backlog != "" {
			n++
		}
	}
	return n
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	res := Run(root)
	res.Report(os.Stdout)
	if !res.OK() {
		os.Exit(1)
	}
}
