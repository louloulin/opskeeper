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

// routeReg matches a chi registration: <anything>.Post("/path", h.handler).
//
// The receiver is deliberately **any identifier** rather than the `r` this
// tree mostly uses. The first version hard-coded `r`, and core/domains/server/llmgw
// — a whole LLM-proxy file — was then invisible to the gate while looking fully
// accounted for: a verdict for its route existed, the route itself was never
// seen, and the command reported it as an orphan. **A detector that misses a
// route produces the most expensive kind of wrong answer**: it does not fail,
// it fails to fail.
var routeReg = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.(Post|Put|Patch|Delete)\("([^"]+)",\s*([A-Za-z0-9_.]+)`)

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

// Roots are the trees this command holds to a verdict table.
//
// Declared rather than discovered, because "which trees" is the part that goes
// stale quietly. The first version scanned core/manager/server only, and
// core/domains/server — 24 mutating routes across eight files, including the
// secret store — was invisible to it. A gate with an unstated scope answers
// for the part somebody happened to look at, so the scope is a constant
// somebody has to edit, and findUnscannedRoots fails if a mutating route
// appears anywhere else.
//
// The three later entries were found by findUnscannedRoots itself, which is
// the argument for having it: core/manager/iam/server alone holds 17 mutating
// routes — resetPassword, setRole, deleteOrg among them — and the question
// "who changed this user's role" is the single most asked question a control
// plane has to answer.
var Roots = []string{
	"core/manager/server",
	"core/domains/server",
	"core/manager/iam/server",
	"core/manager/higress",
	"cmd/opskeeper",
}

// Verdict is the recorded judgement about one route.
type Verdict struct {
	// File is relative to the repository root, e.g.
	// "core/manager/server/alert/http.go". Repo-relative rather than
	// root-relative because two roots own files of the same name, and a key
	// that cannot say which tree it meant is a key that can match the wrong
	// one.
	File string
	// Route is the registration path, e.g. "/v1/alerts/{id}/silence".
	Route string
	// Handler is the function the route is bound to, e.g. "h.silence".
	//
	// Part of the key, not decoration. Thirteen paths in this repository bind
	// two different handlers — PUT h.update and DELETE h.del on the same
	// secret, PATCH h.updateUser and DELETE h.deleteUser on the same user —
	// and keying on the path alone meant only the first registration was ever
	// checked. That is not a near miss: `DELETE /v1/im/apps/{id}` was reported
	// covered by the verdict written for `PUT` of the same path.
	Handler string
	// Backlog is the written reason this route is not audited yet.
	// Empty means the route is expected to be audited.
	//
	// Two kinds of reason live here and they are not the same claim. Most
	// say the route does not need a row: a read-only probe, a cache drop, a
	// proxy envelope whose per-call rows are written elsewhere. Those routes
	// are settled. The rest open with "洞：" and are holes — a mutating route
	// that genuinely should be on the chain and is not. countGaps counts them
	// so that a green run cannot quietly hide twenty-nine of them.
	Backlog string
}

// Verdicts is the table. It is deliberately a slice of routes rather than a
// map of files, because the unit that matters is the route: two routes in
// one file routinely need different answers (see mcp/http.go, where the
// four admin CRUD routes are audited and the JSON-RPC transport is not).
var Verdicts = []Verdict{
	{File: "core/manager/server/alert/http.go", Route: "/v1/alerts/incidents/{id}/investigation", Handler: "h.triggerIncidentInvestigation"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alerts/incidents/{id}/ack", Handler: "h.ackIncident"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alerts/incidents/{id}/resolve", Handler: "h.resolveIncident"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alerts/incidents/{id}/silence", Handler: "h.silenceIncident"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/notification-channels", Handler: "h.createChannel"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/notification-channels/{id}", Handler: "h.updateChannel"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/notification-channels/{id}", Handler: "h.deleteChannel"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alert-rules", Handler: "h.createRule"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alert-rules/{id}", Handler: "h.updateRule"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alert-rules/{id}", Handler: "h.deleteRule"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alert-rules/{id}/enabled", Handler: "h.setRuleEnabled"},
	{File: "core/manager/server/aiops/crystallized.go", Route: "/v1/loops/crystallized/{name}/promote", Handler: "h.promoteCrystallized"},
	{File: "core/manager/server/approval/http.go", Route: "/v1/approvals/{id}/approve", Handler: "h.approve"},
	{File: "core/manager/server/approval/http.go", Route: "/v1/approvals/{id}/reject", Handler: "h.reject"},
	{File: "core/manager/server/imbridge/http.go", Route: "/v1/im/apps", Handler: "h.createApp"},
	{File: "core/manager/server/imbridge/http.go", Route: "/v1/im/apps/{id}", Handler: "h.updateApp"},
	{File: "core/manager/server/imbridge/http.go", Route: "/v1/im/apps/{id}", Handler: "h.deleteApp"},
	{File: "core/manager/server/imbridge/http.go", Route: "/v1/im/apps/{id}/reveal", Handler: "h.revealAppSecret"},
	{File: "core/manager/server/imbridge/http.go", Route: "/v1/im/feishu/events", Handler: "h.handleFeishuEvent",
		Backlog: "inbound webhook authenticated by platform signature rather than by a tenant, so a failure row would name nobody"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alert-rules/preview", Handler: "h.previewRule",
		Backlog: "evaluates a draft rule against a 24h backfill and persists nothing; it costs a range query, not a state change"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/notification-channels/{id}/test", Handler: "h.testChannel",
		Backlog: "delivers one test message through the channel and changes no configuration"},
	{File: "core/manager/server/alert/http.go", Route: "/v1/alerts/webhook", Handler: "h.ingestAlertmanager",
		Backlog: "inbound Alertmanager webhook; audited by delivery, not by caller identity"},
	{File: "core/manager/server/mcp/http.go", Route: "/v1/mcp/servers", Handler: "h.create",
		Backlog: "MCP server registration carries credentials; decision 311 found this table had wrongly claimed it was audited"},
	{File: "core/manager/server/mcp/http.go", Route: "/v1/mcp/servers/{id}", Handler: "h.update",
		Backlog: "see /v1/mcp/servers"},
	{File: "core/manager/server/mcp/http.go", Route: "/v1/mcp/servers/{id}", Handler: "h.delete",
		Backlog: "see /v1/mcp/servers"},
	{File: "core/manager/server/mcp/http.go", Route: "/v1/mcp/servers/{id}/test", Handler: "h.test",
		Backlog: "see /v1/mcp/servers"},
	{File: "core/manager/server/mcp/http.go", Route: "/v1/mcp", Handler: "h.jsonRPC",
		Backlog: "JSON-RPC envelope; each dispatched method writes its own mcp_tool_* row, so auditing the envelope would double-count"},
	{File: "core/manager/server/systemhealth/http.go", Route: "/v1/system/health/check", Handler: "h.check",
		Backlog: "read-only probe fan-out; POST only because it carries a target list, and no state changes"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/chat/sessions", Handler: "h.createSession",
		Backlog: "chat session lifecycle — high volume, low consequence; queued behind the execution surfaces"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/chat/sessions/{id}", Handler: "h.closeSession",
		Backlog: "see /v1/chat/sessions"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/chat/sessions/{id}", Handler: "h.renameSession",
		Backlog: "see /v1/chat/sessions"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/chat/sessions/{id}/messages", Handler: "h.postMessage",
		Backlog: "see /v1/chat/sessions"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/chat/sessions/{id}/messages/stream", Handler: "h.postMessageStream",
		Backlog: "see /v1/chat/sessions"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/chat/sessions/{id}/stop", Handler: "h.stopSession",
		Backlog: "see /v1/chat/sessions"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/aiops/query-translate", Handler: "h.queryTranslate",
		Backlog: "a query translation, not a mutation; only looks mutating because it is POST"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/agents/custom", Handler: "h.createUserAgent",
		Backlog: "custom agent definition — changes what the model may do, so it is queued behind the execution surfaces"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/agents/custom/{name}", Handler: "h.updateUserAgent",
		Backlog: "see /v1/agents/custom"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/agents/custom/{name}", Handler: "h.deleteUserAgent",
		Backlog: "see /v1/agents/custom"},
	{File: "core/manager/server/aiops/http.go", Route: "/v1/agents/{name}", Handler: "h.deleteAgent",
		Backlog: "see /v1/agents/custom"},
	{File: "core/manager/server/agentteams/http.go", Route: "/v1/hitl/decide", Handler: "h.hitlDecide"},
	{File: "core/manager/server/dataguard/http.go", Route: "/v1/data-guard/labels", Handler: "h.upsertLabel"},
	{File: "core/manager/server/dataguard/http.go", Route: "/v1/data-guard/labels/{type}/{id}", Handler: "h.overrideLabel"},
	{File: "core/manager/server/dataguard/http.go", Route: "/v1/data-guard/labels/{type}/{id}", Handler: "h.deleteLabel"},
	{File: "core/manager/server/skill/http.go", Route: "/v1/skills/{key}/execute", Handler: "h.execute"},
	{File: "core/manager/server/agentteams/http.go", Route: "/v1/state/{task_id}", Handler: "h.putState",
		Backlog: "AgentTeams worker scratch state, rewritten constantly by running workers; a row per write would drown the chain"},
	{File: "core/manager/server/agentteams/http.go", Route: "/v1/knowledge/docs", Handler: "h.createKnowledgeDoc",
		Backlog: "knowledge ingest"},
	{File: "core/manager/server/agentteams/http.go", Route: "/v1/incidents/events", Handler: "h.recordIncidentEvent",
		Backlog: "incident timeline append"},
	{File: "core/manager/server/agentteams/plugin_http.go", Route: "/v1/plugins/install", Handler: "h.installPlugin",
		Backlog: "plugin install — code reaching the host, high consequence; queued, not forgotten"},
	{File: "core/manager/server/agentteams/plugin_http.go", Route: "/v1/plugins/{id}", Handler: "h.uninstallPlugin",
		Backlog: "see /v1/plugins/install"},
	{File: "core/manager/server/agentteams/plugin_http.go", Route: "/v1/plugins/{id}/enable", Handler: "h.enablePlugin",
		Backlog: "see /v1/plugins/install"},
	{File: "core/manager/server/agentteams/plugin_http.go", Route: "/v1/plugins/{id}/disable", Handler: "h.disablePlugin",
		Backlog: "see /v1/plugins/install"},
	{File: "core/manager/server/agentteams/plugin_http.go", Route: "/v1/plugins/{id}/sync", Handler: "h.syncPlugin",
		Backlog: "see /v1/plugins/install"},
	{File: "core/manager/server/agentteams/plugin_http.go", Route: "/v1/plugins/{id}/push", Handler: "h.pushPlugin",
		Backlog: "see /v1/plugins/install"},
	{File: "core/manager/server/marketplace/http.go", Route: "/v1/marketplace/install", Handler: "h.install",
		Backlog: "marketplace install — same class as /v1/plugins/install"},
	{File: "core/manager/server/marketplace/http.go", Route: "/v1/marketplace/upload", Handler: "h.upload",
		Backlog: "package upload — a new artifact entering the system"},
	{File: "core/manager/server/marketplace/http.go", Route: "/v1/marketplace/import", Handler: "h.importContainer",
		Backlog: "container import — the same reach as upload"},
	{File: "core/manager/server/marketplace/http.go", Route: "/v1/marketplace/installed/{pack_id}", Handler: "h.uninstall",
		Backlog: "see /v1/marketplace/install"},
	{File: "core/manager/server/marketplace/http.go", Route: "/v1/marketplace/installed/{pack_id}/bindings", Handler: "h.setBindings",
		Backlog: "tool bindings for an installed pack — decides which tools are reachable"},
	{File: "core/manager/server/loop/http.go", Route: "/v1/loops/{incident_id}/trigger", Handler: "h.trigger",
		Backlog: "starts a remediation loop, which can reach the executors the approval inbox guards"},
	{File: "core/manager/server/loop/http.go", Route: "/v1/recovery/verify", Handler: "h.verifyRecovery",
		Backlog: "read-mostly recovery verification"},
	{File: "cmd/opskeeper/main.go", Route: "/v1/pages/{id}", Handler: "func",
		Backlog: "洞：页面删除。handler 是在 Register 里就地写的闭包，闸门只能看到 `func` 这个名字——这条路由连一个能指认的函数都没有"},
	{File: "cmd/opskeeper/main.go", Route: "/v1/pages/{id}/share", Handler: "func",
		Backlog: "洞：页面分享。把一个页面交给别人是外发动作，同上，handler 是就地闭包"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/grafana/sync", Handler: "h.syncGrafana",
		Backlog: "向外部 Grafana 推 dashboard：一次对本仓不拥有的系统的外写，它自己的变更记录在 Grafana 侧"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/grafana/test", Handler: "h.testGrafana",
		Backlog: "见 /v1/integrations/prom/test"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/llm/invalidate", Handler: "h.invalidateLLM",
		Backlog: "丢掉 LLM 句柄缓存，逼下一次调用重新读；丢的是缓存，能从已入账的那一行重建"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/loki/test", Handler: "h.testLoki",
		Backlog: "见 /v1/integrations/prom/test"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/prom/test", Handler: "h.testProm",
		Backlog: "读配置、拨号、回报可达性；不写任何状态，POST 只因为它带一个目标列表（与 /v1/system/health/check 同形）"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/tempo/test", Handler: "h.testTempo",
		Backlog: "见 /v1/integrations/prom/test"},
	{File: "core/domains/server/integration/http.go", Route: "/v1/integrations/websearch/test", Handler: "h.testWebSearch",
		Backlog: "见 /v1/integrations/prom/test"},
	{File: "core/domains/server/llmgw/llmgw.go", Route: "/v1/chat/completions", Handler: "h.chatCompletions",
		Backlog: "网关自身。真正要留痕的是每一次调用的用量与模型，那已经在别处逐次入账；在信封上再记一行会把同一次调用数两遍（同 /v1/mcp 的 jsonRPC）"},
	{File: "core/domains/server/monitor/http.go", Route: "/v1/monitor/panels", Handler: "h.create",
		Backlog: "洞：面板定义是展示态，但它仍然是一次写"},
	{File: "core/domains/server/monitor/http.go", Route: "/v1/monitor/panels/{id}", Handler: "h.update",
		Backlog: "洞：见 /v1/monitor/panels h.create"},
	{File: "core/domains/server/monitor/http.go", Route: "/v1/monitor/panels/{id}", Handler: "h.delete",
		Backlog: "洞：见 /v1/monitor/panels h.create"},
	{File: "core/domains/server/nodeagent/http.go", Route: "/v1/node-agents/sessions", Handler: "h.openSession",
		Backlog: "洞：开一个节点 Agent 会话"},
	{File: "core/domains/server/nodeagent/http.go", Route: "/v1/node-agents/sessions/{sid}", Handler: "h.close",
		Backlog: "洞：关会话"},
	{File: "core/domains/server/nodeagent/http.go", Route: "/v1/node-agents/sessions/{sid}/approvals/{requestID}/decide", Handler: "h.decide",
		Backlog: "洞：与决策 309 的审批收件箱同形——批准按钮按下去的那一刻链上应当有一行，这里没有"},
	{File: "core/domains/server/nodeagent/http.go", Route: "/v1/node-agents/sessions/{sid}/messages", Handler: "h.postMessage",
		Backlog: "洞：向节点 Agent 下发消息——这是指令进入执行面的入口"},
	{File: "core/domains/server/nodeagent/http.go", Route: "/v1/node-agents/sessions/{sid}/stop", Handler: "h.stop",
		Backlog: "洞：急停"},
	{File: "core/domains/server/prometheus/http.go", Route: "/v1/prometheus/launch", Handler: "h.launch",
		Backlog: "拉起一次查询会话；会话内的每次 range query 各自入账"},
	{File: "core/domains/server/prometheus/http.go", Route: "/v1/prometheus/query_range", Handler: "h.queryRange",
		Backlog: "只读区间查询，POST 只因为查询体放不进 URL"},
	{File: "core/domains/server/secret/http.go", Route: "/v1/secrets", Handler: "h.create"},
	{File: "core/domains/server/secret/http.go", Route: "/v1/secrets/{id}", Handler: "h.update"},
	{File: "core/domains/server/secret/http.go", Route: "/v1/secrets/{id}", Handler: "h.del"},
	{File: "core/domains/server/setting/http.go", Route: "/v1/system-settings/{category}/{key}", Handler: "h.put"},
	{File: "core/domains/server/setting/http.go", Route: "/v1/system-settings/{category}/{key}", Handler: "h.delete"},
	{File: "core/domains/server/systemupgrade/http.go", Route: "/v1/system/upgrade/check", Handler: "h.check",
		Backlog: "只读检查：问「有没有新版本」，不装任何东西"},
	{File: "core/manager/higress/server.go", Route: "/consumers", Handler: "s.handleAdminCreate",
		Backlog: "洞：建 consumer，凭证由网关自己落库，本仓链上看不到是谁建的"},
	{File: "core/manager/higress/server.go", Route: "/consumers/{name}", Handler: "s.handleAdminDelete",
		Backlog: "洞：删 consumer，删的是一整条访问路径"},
	{File: "core/manager/higress/server.go", Route: "/session/login", Handler: "s.handleLogin",
		Backlog: "网关自己的登录，由网关自己的凭据校验；调用者身份在上游那一跳已经入账"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/agentteams/token", Handler: "h.issueAgentTeamsToken"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/auth/login", Handler: "h.login"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/auth/refresh", Handler: "h.refresh",
		Backlog: "换 token：签发新凭据但不改任何身份状态，login 已经入账，refresh 的那一行记的是「同一个人又来了一次」"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/auth/register", Handler: "h.register"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/orgs", Handler: "h.createOrg",
		Backlog: "洞：建组织"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/orgs/{id}", Handler: "h.updateOrg",
		Backlog: "洞：改组织"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/orgs/{id}", Handler: "h.deleteOrg",
		Backlog: "洞：删组织，连带其成员关系一起消失"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/orgs/{id}/members", Handler: "h.addOrgMember",
		Backlog: "洞：加成员。这是授权的源头动作，链上没有它就无法回答「他为什么能看这个租户」"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/orgs/{id}/members/{user_id}", Handler: "h.updateOrgMember",
		Backlog: "洞：改成员角色"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/orgs/{id}/members/{user_id}", Handler: "h.removeOrgMember",
		Backlog: "洞：移除成员。撤权比授权更需要留痕，而它恰恰没有"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/users", Handler: "h.createUser",
		Backlog: "洞：建用户。同文件里 setRole/deleteUser 已入账，建用户反而没有，是一张不完整的表"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/users/{id}", Handler: "h.updateUser",
		Backlog: "洞：改用户资料。deleteUser 已入账而 updateUser 没有，同一个资源的两个动词一半有一半没有"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/users/{id}", Handler: "h.deleteUser"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/users/{id}/password", Handler: "h.resetPassword",
		Backlog: "洞：重置口令。本仓风险最高的一条写路由——「谁重置了谁的密码」答不出，事后无法追责"},
	{File: "core/manager/iam/server/http.go", Route: "/v1/users/{id}/role", Handler: "h.setRole"},
	{File: "core/manager/server/chatdiagnose/http.go", Route: "/conversations/{id}/promote", Handler: "h.promote",
		Backlog: "洞：把一轮对话晋升为正式结论"},
	{File: "core/manager/server/chatdiagnose/http.go", Route: "/conversations/{id}/reports", Handler: "h.pushReport",
		Backlog: "洞：推送报告，内容是外发的"},
	{File: "core/manager/server/chatdiagnose/http.go", Route: "/diagnose", Handler: "h.diagnose",
		Backlog: "洞：发起一次诊断。会话内的每一步另有其记录，但「谁在什么时候对哪个告警发起了诊断」这一行没有"},
	{File: "core/manager/server/demo/http.go", Route: "/v1/demo/incidents/{incident_id}/approve", Handler: "h.approveScenario",
		Backlog: "演示剧本的批准：数据在 demo 命名空间内，不碰生产；但它走的是同一套审批按钮，链上分不出两者"},
	{File: "core/manager/server/demo/http.go", Route: "/v1/demo/scenarios/{idempotency_key}/workflow/{stage}", Handler: "h.advanceWorkflow",
		Backlog: "演示剧本推进阶段，同样只在 demo 命名空间内"},
	{File: "core/manager/server/hitl/http.go", Route: "/v1/hitl/proposals", Handler: "h.create"},
	{File: "core/manager/server/hitl/http.go", Route: "/v1/hitl/proposals/{id}/approve", Handler: "h.approve"},
	{File: "core/manager/server/hitl/http.go", Route: "/v1/hitl/proposals/{id}/expire", Handler: "h.expire"},
	{File: "core/manager/server/hitl/http.go", Route: "/v1/hitl/proposals/{id}/reject", Handler: "h.reject"},
	{File: "core/manager/server/loop/admin.go", Route: "/{incident_id}/increment", Handler: "deps.incrementRetryCount",
		Backlog: "洞：手工加一次重试次数。它直接决定自愈循环还会不会再试一次，是执行面的一次真实推动"},
	{File: "core/manager/server/loop/admin.go", Route: "/{incident_id}/reset", Handler: "deps.resetRetryCount",
		Backlog: "洞：手工清零重试次数。增和减必须成对入账——只记其一等于没记"},
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
	// Unwalkable are roots that yielded no Go files at all, which means they
	// were declared and never opened.
	Unwalkable []string
	// Unscanned are files outside Roots that register mutating routes.
	// Any hit fails the run: a new HTTP surface must either join Roots with
	// its own verdicts, or be shown to register none.
	Unscanned []string
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
	return len(r.Missing) == 0 && len(r.Stale) == 0 && len(r.Orphan) == 0 &&
		len(r.Gone) == 0 && len(r.Unscanned) == 0 && len(r.Unwalkable) == 0
}

// Run walks every tree in Roots and compares it against the table.
//
// Every root, not "the main one plus whatever else is convenient". The first
// version of this function took a single root and then hard-coded
// `core/manager/server` underneath it, so adding a second entry to Roots
// changed the report's "roots scanned: 2" line and nothing else — the whole
// core/domains/server tree, 24 mutating routes including the secret store,
// was declared covered and never opened. A scope that is printed but not
// walked is worse than an unstated one, because it looks like somebody
// checked.
func Run(root string) Result {
	var res Result
	seen := map[string]bool{}
	seenFiles := map[string]bool{}

	for _, tree := range Roots {
		base := filepath.Join(root, filepath.FromSlash(tree))
		files := 0
		routes := 0

		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// Keys are repo-relative, not root-relative. Both roots own a
			// setting/http.go, a secret/http.go and a monitor/http.go, so a
			// short key cannot say which tree it meant — and a verdict that
			// silently matched the wrong tree is the table lying.
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			seenFiles[rel] = true
			files++
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			for _, m := range routeReg.FindAllStringSubmatch(string(src), -1) {
				key := routeKey(rel, m[3], m[4])
				routes++
				if seen[key] {
					// The same handler bound to the same path twice is one unit
					// of audit, not two; saying it twice would be noise that
					// trains people to skim the output.
					continue
				}
				seen[key] = true
				audited := reachesAudit(string(src), m[4])
				v, ok := lookup(key)
				switch {
				case !ok:
					res.Missing = append(res.Missing, key+" — no verdict recorded in scripts/routeaudit")
				case v.Backlog == "" && !audited:
					res.Missing = append(res.Missing, key+" — recorded as audited, but "+rel+"'s handler "+m[4]+" never calls SetAuditEvent")
				case v.Backlog != "" && audited:
					res.Stale = append(res.Stale, key+" — "+rel+"'s handler "+m[4]+" calls SetAuditEvent now, so its backlog reason no longer describes it")
				}
			}
			return nil
		})

		// A root that yields no Go files is a root that was never walked:
		// renamed, moved, or spelled wrong in the constant above. Without this
		// the run would report it as scanned and clean.
		if files == 0 {
			res.Unwalkable = append(res.Unwalkable, tree+" — no .go files found under it, so nothing here was scanned")
		} else if routes == 0 {
			fmt.Fprintf(os.Stderr, "routeaudit: note: %s has %d Go files but no mutating route\n", tree, files)
		}
	}

	res.Unscanned = findUnscannedRoots(root)

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
		case seen[routeKey(v.File, v.Route, v.Handler)]:
		case seenFiles[v.File]:
			res.Orphan = append(res.Orphan, routeKey(v.File, v.Route, v.Handler))
		default:
			res.Gone = append(res.Gone, routeKey(v.File, v.Route, v.Handler)+" — the file is no longer in the tree")
		}
	}
	sort.Strings(res.Missing)
	sort.Strings(res.Stale)
	sort.Strings(res.Orphan)
	sort.Strings(res.Gone)
	sort.Strings(res.Unscanned)
	sort.Strings(res.Unwalkable)
	return res
}

// findUnscannedRoots reports Go files outside every root that register a
// mutating route. A new HTTP surface is the exact thing this command exists to
// catch, so a surface that lives in a tree Roots does not name would otherwise
// be invisible — which is how core/domains/server stayed invisible for as long
// as it did.
//
// The command's own source and the test trees are excluded: the first would
// otherwise register its own regex as a route, and the second exists to be
// scanned.
func findUnscannedRoots(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			switch rel {
			case ".git", "node_modules", "vendor", "dist":
				return filepath.SkipDir
			}
			for _, tree := range Roots {
				if rel == tree {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		if rel == "scripts/routeaudit/main.go" {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if routeReg.Match(src) {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// routeKey is the identity of one audited unit: the file it is registered in,
// the path, and the handler it is bound to.
func routeKey(file, route, handler string) string {
	return file + " " + route + " " + handler
}

func lookup(key string) (Verdict, bool) {
	for _, v := range Verdicts {
		if routeKey(v.File, v.Route, v.Handler) == key {
			return v, true
		}
	}
	return Verdict{}, false
}

// Report prints everything the run found. Order is deliberate: MISSING first
// (a route nobody has judged), then stale and orphan (judgements that no
// longer describe the tree).
func (r Result) Report(w *os.File) {
	fmt.Fprintln(w, "routeaudit: every mutating route under "+strings.Join(Roots, ", ")+" has a recorded verdict")
	fmt.Fprintf(w, "  roots declared: %d, verdicts recorded: %d, of which backlog: %d\n",
		len(Roots), len(Verdicts), countBacklog())
	fmt.Fprintf(w, "  audited: %d, settled exemption: %d, acknowledged gap: %d\n",
		len(Verdicts)-countBacklog(), countBacklog()-countGaps(), countGaps())
	for _, m := range r.Missing {
		fmt.Fprintf(w, "  MISSING: %s\n", m)
	}
	for _, s := range r.Stale {
		fmt.Fprintf(w, "  stale:   %s\n", s)
	}
	for _, o := range r.Orphan {
		fmt.Fprintf(w, "  orphan:  %s — the route is no longer registered; drop the verdict\n", o)
	}
	for _, g := range r.Gone {
		fmt.Fprintf(w, "  gone:    %s — the whole file left the tree; drop the verdict\n", g)
	}
	for _, e := range r.Unwalkable {
		fmt.Fprintf(w, "  UNWALKABLE: %s\n", e)
	}
	for _, u := range r.Unscanned {
		fmt.Fprintf(w, "  UNSCANNED: %s — add it to routeaudit.Roots and judge its routes\n", u)
	}
}

// gapPrefix marks a backlog entry as an acknowledged hole rather than a
// settled exemption.
const gapPrefix = "洞："

func countGaps() int {
	n := 0
	for _, v := range Verdicts {
		if strings.HasPrefix(v.Backlog, gapPrefix) {
			n++
		}
	}
	return n
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
