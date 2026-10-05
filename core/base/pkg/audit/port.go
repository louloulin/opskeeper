// Package audit is the port through which a bounded context declares that
// something happened, without knowing who writes the ledger.
//
// It exists because the alternative was a cycle. Decision 35 put the
// HLD-010 audit chain in core/manager/biz/audit, on the argument that
// every audited row must pass through one throat. That argument holds for
// the *writer*, and it was quietly extended to the *readers*: iam's HTTP
// handlers had to import manager's biz layer, model layer and server
// middleware to name a row. The dependency ran backwards, from a leaf
// bounded context to three packages that sit above it, and the only
// reason it was not an import cycle was that nothing in those three
// packages happened to import iam. That is a coincidence, not a
// boundary.
//
// So the shape of a row moved down here, and the throat stayed where it
// was. What this package can do is describe an event and carry it from a
// handler to the middleware that will emit it. What it cannot do is write
// a row: there is no usecase, no repository, no chain head, no HMAC. A
// caller holding this package can ask to be remembered; only the host
// decides whether that becomes a record, which is the property the audit
// trail is supposed to have.
//
// The invariants this package keeps:
//
//   - It imports no bounded context. core/base/pkg/** is the one tree
//     the architecture rules forbid from reaching into biz, model, data,
//     service or server, so a future split that moves the ledger cannot
//     quietly drag this along with it.
//   - It holds no lifecycle. Nothing here opens a database, starts a
//     goroutine, or decides when a row is written.
//   - The vocabulary below is a closed list. Both the middleware that
//     buckets HTTP status and every handler that stamps a row read it
//     from here, so an action string cannot be spelled two ways.
//
// core/manager/biz/audit and core/manager/model/audit re-export these
// types under their own names, so the rest of the manager module is
// unchanged by this package existing.
package audit

import (
	"context"
	"net/http"
)

// Event is the input shape for Emit. Caller fills what it knows; the
// usecase stamps OccurredAt and serialises Payload.
type Event struct {
	// Actor — filled by the middleware from JWT claims, by handlers
	// for failed-auth or anon paths.
	UserID    *uint64
	UserEmail string
	Role      string
	IP        string
	UserAgent string
	RequestID string

	// Action — must be one of the canonical Action* constants below.
	Action       string
	ResourceType string
	ResourceID   string
	ResourceName string

	// Outcome.
	Status       string // success|failure|denied
	ErrorCode    string
	ErrorMessage string

	// Free-form structured detail. Caller is responsible for redacting
	// secrets BEFORE passing in (LLM keys, passwords, tokens). Pass a
	// map or struct; the usecase JSON-encodes.
	Payload any
}

// Status values, bucketed by the middleware from the HTTP status code.
const (
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusDenied  = "denied"
)

// Canonical actions. The naming convention is verb_resource in
// snake_case — kept deliberately small. Sub-flavours (enable vs disable,
// role-change vs password-reset, single vs bulk) live in the payload,
// not the action name, so the UI's action dropdown stays short and the
// audit row's payload tells the operator exactly what changed.
//
// Operator feedback 2026-05-20: the prior 38-action enum mixed
// CRUD verbs with state-transition flavours (rule_enable / rule_disable)
// and per-setting names (llm_key_set / grafana_config_set), which made
// the action filter sprawl. The current set collapses those to the
// underlying verb + a payload that carries the specifics.
const (
	// Note 2026-05-21: auth_login / auth_logout / audit_view dropped.
	// Operator flagged read-only / session-bookkeeping rows as drowning
	// out the mutation signal. We keep auth_login_failed because a
	// brute-force pattern still wants to be visible.
	ActionAuthLoginFailed = "auth_login_failed"

	// User CRUD. role / password / profile field changes all surface as
	// user_update — the payload field carries which field flipped.
	ActionUserCreate = "user_create"
	ActionUserUpdate = "user_update"
	ActionUserDelete = "user_delete"
	ActionUserExport = "user_export"

	// Device CRUD. enable / disable / bulk-delete fold into update /
	// delete + a payload (e.g. {"enabled": false, "count": 3}).
	ActionDeviceUpdate = "device_update"
	ActionDeviceDelete = "device_delete"

	// Alert rule CRUD. enable / disable fold into update with payload
	// {"enabled": <bool>}.
	ActionRuleCreate = "rule_create"
	ActionRuleUpdate = "rule_update"
	ActionRuleDelete = "rule_delete"

	ActionIncidentAck     = "incident_ack"
	ActionIncidentResolve = "incident_resolve"
	ActionIncidentSilence = "incident_silence"

	// Settings umbrella. LLM key / Grafana config / SSH key writes all
	// land here; payload carries {"key": "...", "category": "..."}.
	// Sensitive values are redacted upstream.
	ActionSettingUpdate = "setting_update"
	ActionSettingDelete = "setting_delete"

	ActionChannelCreate = "channel_create"
	ActionChannelUpdate = "channel_update"
	ActionChannelDelete = "channel_delete"

	ActionRepoCreate = "repo_create"
	ActionRepoDelete = "repo_delete"
	ActionRepoSync   = "repo_sync"

	ActionSkillInstall   = "skill_install"
	ActionSkillUninstall = "skill_uninstall"

	// Plugin releases. A release is the action that puts new code —
	// including L2 tools that can restart services — onto hosts, so it is
	// the single operation in this list with the widest blast radius.
	//
	// The four verbs are separate rather than folded into one action with
	// a payload: an operator filtering the audit trail for "who rolled
	// back" is asking a different question from "who shipped this", and a
	// single row type would make both queries a payload scan. Halt and
	// rollback are also two different decisions made by two different
	// people at two different moments, and the trail should say which.
	ActionPluginReleaseStart    = "plugin_release_start"
	ActionPluginReleaseAdvance  = "plugin_release_advance"
	ActionPluginReleaseHalt     = "plugin_release_halt"
	ActionPluginReleaseRollback = "plugin_release_rollback"

	// Autonomy execution. One row per decision the node made on its own
	// while the control plane was unreachable, written locally before the
	// action ran and replayed into this chain when the link came back.
	//
	// It is a single action with the phase in the payload rather than two
	// (started / finished) because the phase is the same decision seen
	// twice, and an operator filtering "what did this node do to itself"
	// wants both halves in one list. The idempotency key that the node
	// consumed also travels in the payload, which is what lets an
	// investigator match a node's self-heal to the approval that would
	// have covered it.
	ActionAutonomyExecute = "autonomy_execute"

	// ActionAgentTeamsTokenIssue covers minting a bearer token for an
	// AgentTeams worker: a credential with a TTL and a tool allow-list.
	//
	// It was being written as an inline string literal by iam's handler,
	// which is exactly what the closed list exists to prevent — the row
	// landed in the table, and the filter dropdown had no way to name it.
	// It is here rather than folded into a generic auth action because the
	// 2026-05-20 cleanup that collapsed this list was about *sprawl* from
	// CRUD verbs and state flavours, not about hiding real mutations: a
	// worker token is a credential someone can act with, and an operator
	// asking "who minted an agent token on my tenant" is asking a
	// different question from "who logged in".
	ActionAgentTeamsTokenIssue = "agentteams_token_issue"

	// The MCP surface's two rows, which were being written as inline
	// literals by server/mcp.
	//
	// They are worth a place in the closed list for a reason the operator
	// feedback of 2026-05-20 did not cover. That cleanup was about sprawl:
	// CRUD verbs and state-transition flavours multiplying into dozens of
	// near-identical filter entries. These two are the opposite — an
	// inbound tool call and a *refused* inbound tool call are the two
	// questions an operator asks first about an MCP deployment ("who is
	// calling my tools" and "who was stopped"), and until they were named
	// here a denied call was in the same table as a successful one with
	// nothing but a payload to tell them apart. Note that a denial also
	// needs its own action, not a status: the two are queried separately
	// and folding them would make one of them a payload scan.
	ActionMCPToolCall      = "mcp_tool_call"
	ActionMCPToolAuthorize = "mcp_tool_authorize"

	// The node plane's own vocabulary (决策 126).
	//
	// core/ports has declared a closed set of node-side actions since
	// before any node could write one, and every one of them was unmapped
	// here — so when a node's rows finally reached the chain there was
	// nowhere to file them. These are that set, one-for-one, with a node_
	// prefix (plus plugin_removed, which core/ports gained in decision 126
	// for the same reason: an install that is recorded and a removal that
	// is not would make the ledger's plugin history an append-only fiction).
	//
	// The prefix is not decoration. An operator asking "what did the AI do
	// on this host" is asking about the node, and "did the operator
	// approve a plugin release" is asking about the console; the two must
	// never sort into one filter, and a bare tool_call would.
	//
	// One-to-one rather than collapsed, deliberately. blocked / failed /
	// allowed are three separate questions an investigator asks first, and
	// the MCP entries above already rejected folding exactly this trio
	// into a status field. Three of these have no writer on the node yet
	// (plugin_loaded, proposal_created, recovery_applied); they are here so
	// the map is total and an action nobody has implemented is refused as
	// an unknown string rather than silently filed under a neighbour.
	ActionNodeToolCall        = "node_tool_call"
	ActionNodeToolBlocked     = "node_tool_blocked"
	ActionNodeToolFailed      = "node_tool_failed"
	ActionNodeApprovalRequest = "node_approval_request"
	ActionNodeApprovalGrant   = "node_approval_grant"
	ActionNodeApprovalDeny    = "node_approval_deny"
	ActionNodeAgentTurn       = "node_agent_turn"
	ActionNodeModelCall       = "node_model_call"
	ActionNodePluginInstall   = "node_plugin_install"
	ActionNodePluginRemove    = "node_plugin_remove"
	ActionNodePluginLoad      = "node_plugin_load"
	ActionNodeProposalCreate  = "node_proposal_create"
	ActionNodeRecoveryApply   = "node_recovery_apply"

	// ActionAgentToolCall is the control plane recording a tool it ran on
	// a node's behalf (决策 203).
	//
	// The node_* family above is not a substitute. Those are rows a node
	// writes about work it did ITSELF, into a local ledger, replayed over
	// agent.audit.entries when the link comes back. This row is the
	// control plane's own record of executing a tool because a node's
	// agent asked it to (the agent.tool RPC), and the node never learns
	// whether the call succeeded, so it cannot write this row itself.
	//
	// The distinction matters the moment a node is compromised. "What did
	// this host do" is answered by the node_* rows; "what did someone
	// else's host make MY control plane read" is answered only by this
	// one, and it is the question with the worse answer if it is missing.
	//
	// A single action rather than a call/failed/blocked trio, unlike the
	// node_* set: those three were split because an operator filters them
	// separately on the node's console. Here the outcome is carried by
	// status (success / failure / denied), which is what the 2026-05-20
	// cleanup asked for, and the two denial reasons this channel can
	// produce — a node naming a session it does not own, and a write-
	// classed tool arriving on a read-only channel — are both legible
	// from the payload without a second filter entry.
	ActionAgentToolCall = "agent_tool_call"
)

// ResourceType buckets used in the resource_type column. Same flat-list
// convention as Action — group in the UI, not in the data.
const (
	ResourceUser     = "user"
	ResourceDevice   = "device"
	ResourceIncident = "incident"
	ResourceSetting  = "setting"
	ResourceRule     = "rule"
	ResourceChannel  = "channel"
	ResourceRepo     = "repo"
	ResourceSkill    = "skill"
	ResourceLLM      = "llm"
	ResourceGitKey   = "git_ssh_key"
	ResourceGrafana  = "grafana"
	ResourceRAG      = "rag"
	ResourceAudit    = "audit"
	ResourceAuth     = "auth"
	// ResourcePlugin names a plugin release. The resource id is the
	// package name, which is what an operator searches for.
	ResourcePlugin = "plugin"
	// ResourceEdge names a node. The resource id is the numeric edge id as
	// a string, which is how every other edge-scoped row in this table
	// already identifies itself.
	ResourceEdge = "edge"

	// ResourceMCPTool names a tool reached over the MCP endpoint. The
	// resource id is the tool name the caller asked for, which is what an
	// operator searches for when a tenant claims a tool "does not exist".
	ResourceMCPTool = "mcp_tool"

	// ResourceAgentTool buckets the rows written by the agent.tool proxy
	// (ActionAgentToolCall). It is separate from ResourceMCPTool because
	// the two are different trust directions and an operator triaging one
	// never wants the other: an MCP row is an external client calling in,
	// this is a node we already authenticated reaching back into the
	// control plane. Keeping them apart is the difference between "an
	// unknown caller is using my tools" and "my fleet is using my tools",
	// which are different pages.
	ResourceAgentTool = "agent_tool"
)

// The write side: what a bounded context holds to declare that something
// happened.
//
// The package header says this one "cannot write a row", and that is still
// true — there is no usecase here, no repository, no chain head, no HMAC.
// What these three interfaces add is the *other* half of the sentence: a
// caller that holds only this package can still say "this happened", and
// the host decides whether that becomes a record.
//
// They exist because of decision 272, and the thing they replace is worth
// naming. Four bounded contexts — chatdiagnose, aiops, middleware and
// frontierbound — each reached for `*audit.Usecase`, the concrete façade in
// core/domains/biz/audit. That is four declared cross-domain edges pointing
// at the ledger, and the price the tool computed for them was 3 + 5 + 6 +
// 6 = 20 to cut, which reads cheap until you notice what the number
// measures: named types plus called methods. Three of the four were calling
// exactly one method on it.
//
// The edges were not decoration. `middleware` is in core/domains and the
// façade is in core/domains, so that one happens to be legal; the other
// three reach from core/manager down into a domain it is not, and the only
// reason the module graph tolerates it is that core/manager already depends
// on core/domains for unrelated reasons. A reader of the import alone sees a
// control plane that writes audit rows directly, which is the same shape as
// the iam → manager back-edge decision 38 spent a module split removing.
//
// Three interfaces rather than one, and the split is not tidiness:
//
//   - Sink and IDSink differ in whether the row's sequence number comes
//     back. The agent kernel needs it (it correlates its own entries with
//     the ones the middleware wrote); the HTTP middleware and chatdiagnose
//     do not, and handing them a method that returns a value they discard
//     is how a binding ends up depending on chain state it has no business
//     knowing about.
//   - Verifier is separate for the reason agentkernel's own comment already
//     gave: "a binding that could both write and check its own writes is a
//     binding whose Verify result means nothing". Folding VerifyChain into
//     Sink would hand every writer the ability to certify the chain.
//
// None of them is satisfied by anything in this package. There is no
// implementation here to find, and that is the point — a caller can be
// built, tested and reasoned about holding one of these without a database
// anywhere in the picture.
type Sink interface {
	// Emit records one event. It does not return an error and must not
	// block: a handler that fails because a log row could not be written
	// has turned a storage hiccup into a failed request. A deployment
	// that wants failures surfaced wires the binding to report them
	// out of band.
	Emit(ctx context.Context, ev Event)
}

// IDSink is a Sink that stamps the row's chain sequence number and hands
// it back. Callers that interleave their own rows with the host's — the
// agent kernel's gate decisions sit between two HTTP rows and an incident
// review has to order all three — need the number; everything else should
// hold a Sink.
//
// The error is returned to the caller rather than swallowed because the
// kernel's own Record treats it as advisory (it ignores it) while its
// *tests* assert on it, and an interface that cannot express the difference
// forces one of those two to be wrong.
type IDSink interface {
	EmitWithID(ctx context.Context, ev Event) (uint64, error)
}

// Verifier walks the host's tamper-evident chain and reports the first row
// that does not check out.
//
// It is deliberately not part of Sink. See the note above.
type Verifier interface {
	VerifyChain(ctx context.Context) error
}

// contextKey points to a mutable *slot in the request context.
//
// The slot is installed by the audit middleware before the inner
// middleware chain runs, so handlers down the chain can write to it even
// after intermediate middlewares (auth, otel, ...) have re-wrapped the
// request via r.WithContext — the pointer survives wrapping because the
// value stored is a *slot, not the Event itself. An earlier impl that
// set the Event by mutating *r broke whenever any middleware between the
// audit middleware and the handler called r.WithContext, which is most
// of them.
type contextKey struct{}

type slot struct {
	ev  Event
	set bool
}

// WithSlot installs the empty slot the audit middleware will read after
// the handler returns. It is exported so that the middleware and the
// handlers share one definition of the slot rather than two that agree
// today.
func WithSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, &slot{})
}

// SetAuditEvent records the explicit Event the handler wants audited.
// Safe to call even outside an audit middleware chain — it just no-ops
// if the slot isn't installed.
func SetAuditEvent(r *http.Request, ev Event) {
	if r == nil {
		return
	}
	s, ok := r.Context().Value(contextKey{}).(*slot)
	if !ok || s == nil {
		return
	}
	s.ev = ev
	s.set = true
}

// GetAuditEvent returns the stashed Event, if any.
func GetAuditEvent(ctx context.Context) (Event, bool) {
	s, ok := ctx.Value(contextKey{}).(*slot)
	if !ok || s == nil || !s.set {
		return Event{}, false
	}
	return s.ev, true
}
