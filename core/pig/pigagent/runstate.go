package pigagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/ports"
	"github.com/vincent-wuhan/opskeeper/core/wire"
)

// runState carries the host policy for one turn and is where the kernel
// actually enforces it: every tool call passes through beforeToolCall, and
// every call and refusal passes through afterToolCall.
//
// The policy is deliberately not advisory. A tool that the host has not
// admitted is blocked here regardless of what the model asked for or what
// a plugin claims about itself.
type runState struct {
	mapper *Mapper
	sink   ports.EventSink
	deps   ports.AgentDeps
	k      *Kernel
	req    ports.AgentRequest

	// usage accumulates token spend across the turn so the done frame
	// reports a turn total rather than leaving the console to sum frames.
	usage ports.Usage
	// model is the resolved model's identity, echoed into the done frame so
	// a cost line in the console names the model that incurred it.
	model string
	// blocked records the tool calls this turn refused, by call id, with the
	// reason. PiG settles a refused call as an "immediate" outcome that never
	// reaches the AfterToolCall hook, so without this the refusal would
	// reach neither the console nor the ledger — the one event an incident
	// review most needs. Entries are consumed as their end event arrives.
	blocked map[string]string
	// budgetExhausted latches once the budget checker refuses, so the turn
	// stops for the stated reason instead of re-asking every round trip.
	budgetExhausted bool
	// stopped latches the terminal reason once one is chosen.
	stopped string
}

// errorCode is the stable machine-readable failure code space. A console
// branches on Code; Message is for humans and may change.
const (
	CodeToolBlocked     = "tool_blocked"
	CodeToolFailed      = "tool_failed"
	CodeApprovalDenied  = "approval_denied"
	CodeApprovalExpired = "approval_expired"
	CodeApprovalCancel  = "approval_cancelled"
	CodeBudgetExhausted = "budget_exhausted"
	CodeMaxIterations   = "max_iterations"
	CodeTurnCancelled   = "turn_cancelled"
	CodeTurnTimeout     = "turn_timeout"
	CodeAgentError      = "agent_error"
)

// onEvent translates PiG's event stream into console frames and folds token
// usage into the turn total.
func (r *runState) onEvent(ev agent.AgentEvent) {
	switch e := ev.(type) {
	case agent.TurnStartEvent:
		// The turn counter advances here rather than at MessageStart so the
		// console's iteration column matches completed model round trips.
		r.mapper.TurnStarted()
	case agent.MessageEndEvent:
		r.foldUsage(e.Message)
	case agent.ToolExecutionEndEvent:
		// A call the host refused settles as an immediate outcome: it never
		// runs, so it never reaches AfterToolCall. This is the only place
		// every terminal call is visible, so the refusal is classified and
		// written to the ledger here.
		if reason, blocked := r.blockedCall(e.ToolCallID); blocked {
			e.Result = MarkBlocked(e.Result, reason)
			ev = e
			r.record(ports.AuditEntry{
				Action:  ports.ActionToolBlocked,
				Target:  e.ToolName,
				Class:   string(r.classOf(e.ToolName)),
				Outcome: "blocked",
			})
		}
	}
	for _, f := range r.mapper.Map(ev) {
		if err := r.sink.Emit(context.Background(), f); err != nil {
			// A consumer that has gone away cancels the work it asked for.
			// Returning here stops emitting; the run itself is torn down by
			// the caller's context.
			return
		}
	}
}

// foldUsage accumulates a settled message's token spend.
func (r *runState) foldUsage(msg agent.AgentMessage) {
	asst := msg.Assistant
	if asst == nil {
		return
	}
	u := asst.ObserveUsage()
	if u == nil {
		return
	}
	r.usage.InputTokens += u.Input
	r.usage.OutputTokens += u.Output
	r.usage.CacheReadTokens += u.CacheRead
	r.usage.CacheWriteTokens += u.CacheWrite
	r.usage.CostUSD += u.Cost.Total
	// The mapper owns the frame, so the running total is published to it
	// here rather than read back from the run state when done is built.
	r.mapper.SetUsage(r.usage, r.model)
}

// persist hands a settled message to the host's write path. A persistence
// failure fails the run the same way a throwing listener would upstream: a
// turn whose transcript was not recorded must not be reported as a success.
func (r *runState) persist(msg agent.AgentMessage) error {
	if r.k.opts.Persist == nil {
		return nil
	}
	return r.k.opts.Persist.Persist(context.Background(), r.req.SessionID, msg)
}

// beforeToolCall is the host's policy gate. It runs before every tool
// invocation and is the single place a call can be refused.
//
// Order matters. The class is resolved first so a read-only tool never
// touches the approval machinery, and a mutating one cannot reach a provider
// before a human has approved it.
func (r *runState) beforeToolCall(ctx context.Context, toolCallID, toolName string, args json.RawMessage) agent.ToolCallHookResult {
	// A budget refusal ends the run without executing anything further.
	if r.budgetExhausted {
		return r.refuse(toolCallID, r.budgetReason())
	}
	if r.deps.Budget != nil {
		if allowed, reason := r.deps.Budget.Allow(ctx, r.req.SessionID); !allowed {
			r.budgetExhausted = true
			if reason == "" {
				reason = "budget exhausted"
			}
			return r.refuse(toolCallID, reason)
		}
	}

	class := r.classOf(toolName)
	// Read-only calls proceed. Everything else needs a decision from a
	// human, and the gate is the only thing that can produce one.
	if class == domain.ClassRead {
		return agent.ToolCallHookResult{}
	}
	if r.deps.Gate == nil {
		// No gate configured means no way to obtain a decision. A mutating
		// call must be refused rather than run: failing open here would
		// turn a misconfiguration into an unauthorised action.
		return r.refuse(toolCallID, "no approval gate configured")
	}

	digest := CallDigest(toolName, args)
	summary := toolSummary(toolName, args)
	req := ports.ApprovalRequest{
		ID:        toolCallID,
		ToolName:  toolName,
		Class:     class,
		Arguments: append([]byte(nil), args...),
		Summary:   summary,
		// The blast radius is assessed by the host from the resolved
		// target. A tool never sets it, so the narrowest admissible
		// radius is used until the host policy widens it.
		BlastRadius: domain.RadiusNone,
		Target:      toolTarget(args),
		ExpiresAt:   r.k.opts.Now().Add(approvalTTL),
	}
	_ = r.sink.Emit(ctx, r.mapper.Approval(ApprovalProjection{
		RequestID:   req.ID,
		Digest:      digest,
		Tool:        toolName,
		Class:       string(class),
		Summary:     summary,
		BlastRadius: string(domain.RadiusNone),
		Target:      req.Target,
		ExpiresAt:   req.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}))

	decision, err := r.deps.Gate.Request(ctx, req)
	if err != nil {
		reason := "approval failed"
		switch {
		case isGateReason(err, ports.GateDenied):
			reason = CodeApprovalDenied
		case isGateReason(err, ports.GateExpired):
			reason = CodeApprovalExpired
		case isGateReason(err, ports.GateCancelled):
			reason = CodeApprovalCancel
		}
		_ = r.sink.Emit(ctx, r.mapper.ApprovalResolved(req.ID, string(ports.ApprovalDenied), reason))
		return r.refuse(toolCallID, reason+": "+err.Error())
	}

	// A decision is bound to a digest of the exact proposed call. A grant
	// that does not match this digest was issued for a different call and
	// must not authorise this one.
	if decision.Digest != "" && decision.Digest != digest {
		_ = r.sink.Emit(ctx, r.mapper.ApprovalResolved(req.ID, string(ports.ApprovalDenied), "digest mismatch"))
		return r.refuse(toolCallID, "approval digest does not match this call")
	}
	if decision.Decision != ports.ApprovalGranted {
		_ = r.sink.Emit(ctx, r.mapper.ApprovalResolved(req.ID, string(decision.Decision), decision.Note))
		return r.refuse(toolCallID, CodeApprovalDenied)
	}
	_ = r.sink.Emit(ctx, r.mapper.ApprovalResolved(req.ID, string(ports.ApprovalGranted), decision.Note))
	return agent.ToolCallHookResult{}
}

// refuse records that the host declined this call and returns the hook
// result that stops it.
//
// The record is what lets the refusal be reported honestly. A call PiG
// settles as a refusal never runs, so its own result carries nothing but an
// error string; without this the console would render a policy decision as
// a broken tool and the ledger would have no row for it at all.
func (r *runState) refuse(toolCallID, reason string) agent.ToolCallHookResult {
	if r.blocked == nil {
		r.blocked = make(map[string]string)
	}
	r.blocked[toolCallID] = reason
	return agent.ToolCallHookResult{Block: true, Reason: reason}
}

// blockedCall reports whether the host refused this call, consuming the
// record so a call is audited exactly once.
func (r *runState) blockedCall(toolCallID string) (string, bool) {
	reason, ok := r.blocked[toolCallID]
	if !ok {
		return "", false
	}
	delete(r.blocked, toolCallID)
	return reason, true
}

// approvalTTL bounds how long a pending approval stays actionable. A request
// past it is denied rather than blocking the queue indefinitely.
const approvalTTL = 5 * time.Minute

// afterToolCall writes the ledger entry for a settled call. Both outcomes
// are recorded: a refusal is exactly the event an incident review needs.
func (r *runState) afterToolCall(ctx context.Context, toolCallID, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
	outcome := "success"
	action := ports.ActionToolCall
	switch {
	case isBlocked(result):
		outcome, action = "blocked", ports.ActionToolBlocked
	case result.IsError:
		outcome, action = "error", ports.ActionToolFailed
	}
	r.record(ports.AuditEntry{Action: action, Target: toolName, Class: string(r.classOf(toolName)), Outcome: outcome})
	return agent.AfterToolCallResult{}
}

// record writes one ledger row on the turn's behalf.
//
// The entry is derived here, by the host, from the gate's own decision. A
// plugin can neither forge nor suppress it: it has no path to this sink, and
// the classification comes from the tool bag the host assembled rather than
// from anything the tool reported about itself.
func (r *runState) record(entry ports.AuditEntry) {
	if r.deps.Audit == nil {
		return
	}
	entry.At = r.k.opts.Now().UTC()
	entry.Actor = "agent:" + r.req.SessionID
	_ = r.deps.Audit.Record(context.Background(), entry)
}

// finishTurn decides whether the loop continues.
//
// The budget check runs here rather than before the model call so a turn
// that has already produced its answer is not cut short by a budget that
// was exhausted *by* that answer.
func (r *runState) finishTurn(ctx context.Context, turn agent.AgentTurnContext) (*agent.AgentTurnDecision, error) {
	if r.budgetExhausted {
		r.stopped = ports.TurnToolBudget
		return &agent.AgentTurnDecision{Action: agent.AgentTurnEnd}, nil
	}
	if r.deps.Budget != nil {
		if allowed, _ := r.deps.Budget.Allow(ctx, r.req.SessionID); !allowed {
			r.budgetExhausted = true
			r.stopped = ports.TurnToolBudget
			return &agent.AgentTurnDecision{Action: agent.AgentTurnEnd}, nil
		}
	}
	// The loop stops on its own when the model stops asking for tools;
	// that is the normal end_turn path and needs no decision here.
	return nil, nil
}

// result builds the turn outcome from the messages the run returned.
func (r *runState) result(messages []agent.AgentMessage) *ports.TurnResult {
	res := &ports.TurnResult{
		Iterations: r.mapper.Iteration(),
		Usage:      r.usage,
		Stopped:    r.stopped,
	}
	if res.Stopped == "" {
		res.Stopped = ports.TurnEndTurn
	}
	// The last assistant message is the turn's answer.
	for i := len(messages) - 1; i >= 0; i-- {
		if asst := messages[i].Assistant; asst != nil {
			res.Content = ai.ContentText(asst.Content)
			break
		}
	}
	return res
}

func (r *runState) budgetReason() string { return "budget exhausted for session " + r.req.SessionID }

// classOf resolves a tool's blast-radius class from the tool bag. An
// unknown tool is unclassified and therefore destructive: a tool the host
// cannot classify must not be treated as read-only.
func (r *runState) classOf(name string) domain.ToolClass {
	for _, t := range r.deps.Tools.Tools() {
		if t.Schema().Name == name {
			c := t.Schema().Class
			if !c.Valid() || c == domain.ClassUnknown {
				return domain.ClassDestructive
			}
			return c
		}
	}
	return domain.ClassDestructive
}

// CallDigest binds an approval decision to one exact call.
//
// The digest covers the tool name and the exact argument bytes. A grant
// therefore authorises precisely the call an operator saw, and a re-planned
// call with different arguments does not inherit it.
func CallDigest(toolName string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0})
	h.Write(args)
	return hex.EncodeToString(h.Sum(nil))
}

// toolSummary renders a one-line operator-facing description of a call.
func toolSummary(name string, args json.RawMessage) string {
	summary := name
	if t := toolTarget(args); t != "" {
		summary += " on " + t
	}
	return summary
}

// toolTarget pulls a display target out of the arguments, if the tool
// declared one. It is presentation only: the digest, not this string, is
// what binds an approval.
func toolTarget(args json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	for _, key := range []string{"target", "resource", "host", "node", "namespace", "service", "path"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// isGateReason reports whether err carries the given approval reason.
func isGateReason(err error, reason string) bool {
	var ge *ports.GateError
	if ok := asGateError(err, &ge); ok {
		return ge.Reason == reason
	}
	return false
}

func asGateError(err error, target **ports.GateError) bool {
	for err != nil {
		if ge, ok := err.(*ports.GateError); ok {
			*target = ge
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// unused keeps the wire import referenced where the mapper is the only
// consumer of these frame types.
var _ = wire.StreamToolStart
