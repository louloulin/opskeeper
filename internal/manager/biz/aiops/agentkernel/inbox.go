package agentkernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/ports"
	bizapproval "github.com/vincent-wuhan/opskeeper/internal/manager/biz/approval"
	modelapproval "github.com/vincent-wuhan/opskeeper/internal/manager/model/approval"
)

// inbox.go binds the kernel approval gate onto the manager's human
// propose-confirm inbox (biz/approval, HLD-017).
//
// The inbox is the same one the blocking write tools queue into, and that is
// the point: an operator answers kernel-gated calls in the inbox they already
// watch, with the audit trail that already exists behind it. The difference
// is what happens on approve — those tools' kinds have a registered executor
// that runs the action, while a kernel-gated call has none, so approving the
// row records the decision and the tool runs afterwards under the kernel, in
// the caller's own context. Registering an executor for this kind would run
// the call twice.

// KindAgentToolCall is the approval kind a kernel-gated tool call is queued
// under. It deliberately has no executor: see the package comment above.
const KindAgentToolCall = "agent_tool_call"

const (
	// inboxPollInterval is how often Await re-reads the row. Same value as
	// the blocking write tools use, so an operator watching the inbox sees
	// the two resolve at the same latency.
	inboxPollInterval = 1500 * time.Millisecond
	// inboxAwaitBudget bounds how long Await waits when the request carried
	// no expiry. It is a backstop, not the policy: the kernel sets a real
	// ExpiresAt on every request and that is what is honoured when present.
	inboxAwaitBudget = 30 * time.Minute
)

// agentToolCallPayload is what a kernel-gated call stores in the row.
//
// The digest is stored rather than re-derived: it is the kernel's algorithm
// over the exact argument bytes, and a host that recomputed it from a
// re-serialised payload would produce a different digest the moment a key
// order changed — which would make every grant fail the kernel's binding
// check, and read as "the approval did not work".
type agentToolCallPayload struct {
	ToolName    string `json:"tool_name"`
	Digest      string `json:"digest"`
	Arguments   string `json:"arguments"`
	BlastRadius string `json:"blast_radius,omitempty"`
	Target      string `json:"target,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	// ExpiresAt is the request's own deadline, echoed so Await stops waiting
	// exactly when the kernel stops accepting the answer.
	ExpiresAt string `json:"expires_at,omitempty"`
}

// InboxUsecase adapts biz/approval onto the gate's ApprovalInbox port.
type InboxUsecase struct {
	uc *bizapproval.Usecase
	// now defaults to time.Now. Injected so a test can place a row on either
	// side of its expiry without sleeping.
	now func() time.Time
	// poll defaults to inboxPollInterval.
	poll time.Duration
}

// NewInboxUsecase wraps the inbox. A nil usecase yields nil, which the gate
// turns into a refusal rather than a pass-through.
func NewInboxUsecase(uc *bizapproval.Usecase) *InboxUsecase {
	if uc == nil {
		return nil
	}
	return &InboxUsecase{uc: uc, now: time.Now, poll: inboxPollInterval}
}

// Propose queues one call and returns the row id a decision will name.
func (a *InboxUsecase) Propose(ctx context.Context, req ports.ApprovalRequest) (string, error) {
	if a == nil || a.uc == nil {
		return "", ErrGateNotWired
	}
	payload := agentToolCallPayload{
		ToolName:    req.ToolName,
		Digest:      req.Digest,
		Arguments:   string(req.Arguments),
		BlastRadius: string(req.BlastRadius),
		Target:      req.Target,
		SessionID:   req.SessionID,
	}
	if !req.ExpiresAt.IsZero() {
		payload.ExpiresAt = req.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	title := strings.TrimSpace(req.ToolName + ": " + req.Summary)
	if len(title) > 120 {
		title = title[:120] + "…"
	}
	if strings.TrimSpace(title) == "" {
		title = req.ToolName
	}
	row, err := a.uc.Propose(ctx, bizapproval.ProposeInput{
		Kind:      KindAgentToolCall,
		Title:     title,
		Summary:   req.Summary,
		Payload:   payload,
		Source:    modelapproval.SourceAgent,
		SessionID: req.SessionID,
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

// Await blocks until the row reaches a terminal state, the context is
// cancelled, or the request's own deadline passes.
func (a *InboxUsecase) Await(ctx context.Context, id string) (ports.Decision, error) {
	if a == nil || a.uc == nil {
		return ports.Decision{}, ErrGateNotWired
	}
	now := a.now
	if now == nil {
		now = time.Now
	}
	poll := a.poll
	if poll <= 0 {
		poll = inboxPollInterval
	}
	deadline := now().Add(inboxAwaitBudget)

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// A cancelled wait is not a grant. The kernel reports it as its
			// own terminal reason; inventing an approval here would authorise
			// a call nobody answered.
			return ports.Decision{}, &ports.GateError{Reason: ports.GateCancelled, Err: ctx.Err()}
		case <-ticker.C:
			row, err := a.uc.Get(ctx, id)
			if err != nil {
				// Transient read error — keep polling rather than reporting a
				// refusal the operator never made.
				continue
			}
			switch row.Status {
			case modelapproval.StatusApproved, modelapproval.StatusExecuted:
				return ports.Decision{
					RequestID: id,
					Decision:  ports.ApprovalGranted,
				}, nil
			case modelapproval.StatusRejected:
				return ports.Decision{
					RequestID: id,
					Decision:  ports.ApprovalDenied,
					Note:      decisionNote(row),
				}, nil
			case modelapproval.StatusFailed:
				return ports.Decision{
					RequestID: id,
					Decision:  ports.ApprovalDenied,
					Note:      decisionNote(row),
				}, nil
			}
			// Pending. Stop when the request's own deadline has passed: the
			// kernel will refuse the call by then, and a row decided later
			// would authorise nothing.
			if exp, ok := a.expiryOf(row); ok && !now().Before(exp) {
				return ports.Decision{}, &ports.GateError{Reason: ports.GateExpired}
			}
			if now().After(deadline) {
				return ports.Decision{}, &ports.GateError{Reason: ports.GateExpired}
			}
		}
	}
}

// Open lists the outstanding rows for a session, so a console that
// reconnected can re-render its queue.
func (a *InboxUsecase) Open(ctx context.Context, sessionID string) ([]ports.ApprovalRequest, error) {
	if a == nil || a.uc == nil {
		return nil, ErrGateNotWired
	}
	if sessionID == "" {
		return nil, nil
	}
	rows, err := a.uc.List(ctx, modelapproval.StatusPending, inboxOpenLimit)
	if err != nil {
		return nil, err
	}
	out := make([]ports.ApprovalRequest, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.SessionID != sessionID {
			continue
		}
		out = append(out, ports.ApprovalRequest{
			ID:        row.ID,
			ToolName:  strings.TrimSpace(row.Title),
			Digest:    row.PayloadJSON,
			Summary:   row.Summary,
			SessionID: row.SessionID,
		})
	}
	return out, nil
}

// inboxOpenLimit caps how many pending rows Open reads. The store is shared
// with every other producer, so the query is bounded and the session filter
// happens here; a queue larger than this is an operational problem the
// operator sees in the inbox itself.
const inboxOpenLimit = 500

// expiryOf reads the request deadline out of a row's payload. A row whose
// payload cannot be read has no deadline, and only the await budget bounds
// the wait.
func (a *InboxUsecase) expiryOf(row *modelapproval.Approval) (time.Time, bool) {
	if row == nil || row.PayloadJSON == "" {
		return time.Time{}, false
	}
	var payload agentToolCallPayload
	if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
		return time.Time{}, false
	}
	if payload.ExpiresAt == "" {
		return time.Time{}, false
	}
	exp, err := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return exp, true
}

// decisionNote returns the operator's own words, or a stated default.
func decisionNote(row *modelapproval.Approval) string {
	if row == nil {
		return ""
	}
	if row.Reason != nil && strings.TrimSpace(*row.Reason) != "" {
		return *row.Reason
	}
	return fmt.Sprintf("the operator rejected this %s call", row.Title)
}

var _ ApprovalInbox = (*InboxUsecase)(nil)
