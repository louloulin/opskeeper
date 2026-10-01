package ports

import (
	"context"
	"errors"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/wire"
)

// AgentRequest is one user turn handed to the agent kernel.
type AgentRequest struct {
	SessionID string
	UserID    uint64
	// Role is the caller's system role (admin | user | viewer). The kernel
	// filters the tool bag by it before the turn starts, so a viewer can
	// never reach a mutating tool no matter what a profile requests.
	Role string
	// UserText is the turn verbatim, after any mention rendering.
	UserText string
	// History is the conversation BEFORE this turn, oldest first.
	//
	// It exists on the request rather than being replayed out of the host's
	// session store by the kernel, because the transcript is policy, not
	// plumbing: the host has already applied history-window limits, dropped
	// superseded tool batches, and redacted what a viewer must not see. A
	// kernel that read the session itself would answer a different question
	// from the one the host composed.
	//
	// An empty History is a first turn, not an error.
	History []AgentMessage
	// Selection pins the model for this turn. An unpinned selection
	// resolves to the cluster default at call time.
	Selection domain.ModelSelection
	// WebSearchEnabled gates the search tool for this turn only.
	WebSearchEnabled bool
	// Locale is the console language the reply must use.
	Locale string
	// SystemPrompt is the fully assembled base prompt. The kernel does not
	// compose it; skill and persona assembly happen upstream so a
	// replacement kernel cannot change what the prompt says.
	SystemPrompt string
	// CriticalReminder is the persona's anti-drift text, injected ahead of
	// the turn rather than into the system prompt, so it is re-read each
	// turn instead of being cached with the prompt prefix.
	CriticalReminder string
	// MaxIterations caps tool rounds for the turn. Zero means the kernel
	// default applies.
	MaxIterations int
}

// AgentMessage is one prior turn in the conversation, expressed in the
// vocabulary every kernel already has.
//
// The shape is deliberately minimal. Tool definitions, arguments and results
// are carried as opaque JSON strings rather than typed structures: the host
// stores what the provider said, and a kernel that needs a richer form
// decodes it. Typing them here would force every kernel to agree on a
// provider wire format, which is precisely the coupling the kernel boundary
// exists to prevent.
//
// Exactly one of the role-specific fields is set. A message with none set
// carries no information and a kernel should skip it — silently dropping a
// malformed entry is safer than failing a turn, because the entry came from
// persisted history the operator cannot repair from the console.
type AgentMessage struct {
	// Role is "user" | "assistant" | "tool".
	Role string
	// Content is the text of the message. Empty is legal for an assistant
	// turn that only requested tool calls.
	Content string
	// ToolCalls are the invocations an assistant turn requested.
	ToolCalls []AgentToolCall
	// ToolCallID identifies which assistant tool call a "tool" message
	// answers. Required for role "tool": without it the result cannot be
	// attached to its request and the provider rejects the transcript.
	ToolCallID string
	// ToolName is the tool a "tool" message reports on. Informational —
	// provider correlation uses ToolCallID.
	ToolName string

	// Model and Usage annotate an assistant message the kernel observed
	// accounting for. Both are optional and both are ignored on any other
	// role.
	//
	// They ride here rather than on the turn result because the console
	// shows provenance per message ("the answer above came from glm-4-plus")
	// and the usage ledger sums per row. A kernel that observed them and
	// could not hand them on would force the host to re-ask the provider or
	// to show every turn as unattributed.
	Model string
	Usage *Usage
}

// AgentToolCall is one tool invocation an assistant asked for.
type AgentToolCall struct {
	// ID is the provider-assigned call id, echoed by the answering tool
	// message.
	ID string
	// Name is the tool's wire name.
	Name string
	// Arguments is the raw JSON object the model produced, passed through
	// unmodified.
	Arguments []byte
}

// TurnResult is the settled outcome of one turn.
type TurnResult struct {
	// Content is the final assistant text.
	Content string
	// Iterations counts how many model round trips the turn consumed.
	Iterations int
	// Usage aggregates the turn across every model call it made.
	Usage Usage
	// Stopped reports why the loop ended: "end_turn", "max_iterations",
	// "tool_budget", "cancelled", or "error".
	Stopped string
	// Err is non-nil only when Stopped is "error".
	Err error
}

// Stop reasons for a turn.
const (
	TurnEndTurn       = "end_turn"
	TurnMaxIterations = "max_iterations"
	TurnToolBudget    = "tool_budget"
	TurnCancelled     = "cancelled"
	TurnError         = "error"
)

// Agent is the agent-loop port.
//
// The kernel owns the ReAct loop: model call, tool dispatch, repeat until
// the model stops asking for tools or a cap is hit. It owns nothing else.
// Persistence, streaming, audit, and approval are injected as the ports on
// AgentDeps, which is what lets one kernel serve the control plane, a
// background investigator, and a per-node pig process with different
// policies and no code changes.
type Agent interface {
	// Run settles one turn. It blocks until the turn completes, the context
	// is cancelled, or a cap is reached. Emitted frames go to the sink
	// supplied on the request context.
	Run(ctx context.Context, req AgentRequest) (*TurnResult, error)
	// Steer injects a message into a turn already in flight, the way a
	// supervisor redirects a running investigation. It returns
	// ErrNotRunning when no turn is active for the session.
	Steer(ctx context.Context, sessionID, text string) error
	// Abort cancels the in-flight turn for a session. It is idempotent and
	// safe to call when nothing is running.
	Abort(ctx context.Context, sessionID string) error
	// Spawn starts a background worker with its own tool bag and system
	// prompt. The returned id is used with Steer, Abort, and Notify.
	Spawn(ctx context.Context, req AgentRequest) (string, error)
	// Notify reports a worker's terminal state to the parent turn, which
	// renders it as a task_notification frame.
	Notify(ctx context.Context, workerID, status, summary string) error
}

// AgentDeps are the host services a kernel must use. A kernel that reaches
// for anything else is bypassing the host's policy and audit guarantees.
type AgentDeps struct {
	// Tools is the tool bag for this turn, already filtered by role and
	// profile. The kernel must not widen it.
	Tools ToolBag
	// Audit records every tool call, block, and failure.
	Audit AuditSink
	// Gate is the sole path for a gated call to execute. A kernel that
	// finds no gate must refuse every non-read tool rather than run it.
	Gate ApprovalGate
	// Model is the synchronous completion path used by the loop.
	Model Chat
	// Budget is consulted before each model call. Returning false ends the
	// turn with TurnToolBudget.
	Budget BudgetChecker
	// Recorder observes each admitted tool call from admission to settle.
	// It is how the console's tool table is populated without the kernel
	// knowing its schema. Optional: nil records nothing.
	Recorder ToolCallRecorder
}

// BudgetChecker reports whether spend may continue.
type BudgetChecker interface {
	// Allow reports whether another model call is permitted. It is
	// consulted before each round trip so a turn that would blow the
	// budget stops cleanly rather than mid-flight.
	Allow(ctx context.Context, sessionID string) (allowed bool, reason string)
}

// ErrSinkClosed is what a sink reports once the consumer it was feeding has
// gone away.
//
// It is a named error rather than a bare one so a caller can tell "this
// console closed the tab" from "this turn failed": the first means stop
// relaying, the second means report it. A sink that invents its own error
// for the first case leaves the caller guessing which of the two it hit.
var ErrSinkClosed = errors.New("ports: event sink is closed")

// EventSink receives streaming frames for one turn.
//
// Implementations must not block indefinitely. A sink that is not reading
// causes the kernel to drop frames and bump the session's Seq gap rather
// than stall the loop: a stalled consumer must never hold a provider
// connection open.
//
// Emit may be called concurrently. A kernel runs the sibling tool calls of
// one assistant turn in parallel, so several goroutines reach the sink at
// once; an implementation that appends to a bare slice will lose frames or
// trip the race detector. Frame ORDER is the mapper's responsibility (it
// hands out the sequence numbers under its own lock), so a sink must not
// try to serialise for that reason — it only has to be safe to call.
type EventSink interface {
	// Emit delivers one frame. Returning an error ends the turn: a
	// consumer that has gone away cancels the work it asked for.
	Emit(ctx context.Context, ev StreamEvent) error
}

// StreamEvent is an alias for the wire frame so a kernel implementation
// needs to import only this package. It is the same type the console
// parses; there is no separate internal event shape to translate through.
type StreamEvent = wire.StreamEvent
