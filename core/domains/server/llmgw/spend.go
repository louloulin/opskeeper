package llmgw

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/vincent-wuhan/opskeeper/core/base/pkg/errs"
)

// This file is the money. Everything else in this package is about a node
// being able to reach a model; this file is about the cluster surviving the
// fact that it can.
//
// The gateway is the only path from the fleet to a provider, and that makes it
// the only place a runaway agent loop can be stopped before it is billed. A
// node is a supervised process, but "supervised" describes the binary: nothing
// in PiG bounds how many turns an investigation takes, how long a reply may
// run, or what a node asks the model to write. So the three things that bound
// spend are enforced here, on the manager's side of the wire, where a node
// cannot reach around them:
//
//   - a token bucket per node, so one sick node cannot spend the fleet's share
//   - the cluster's existing daily token cap, so a fleet of healthy nodes
//     still cannot spend past the operator's ceiling
//   - the caller's own max_completion_tokens, so a request that asked for a
//     bound does not get an unbounded answer
//
// None of these is a new concept. The cap is the same llm.InMemoryBudget the
// console's agent kernel is gated by, and the bucket is the same shape as the
// tool-call limiter in aiops/tools/decorators. What is new is only that a node
// now passes through them, and it is passed through the manager rather than
// trusted to honour a limit on its own host — a limit the node does not
// implement cannot be exceeded by a compromised node.

// Budget is the cluster's spend ceiling, asked twice per call.
//
// It is two methods because a cap that is only consulted is not a cap: the
// check refuses the call that would cross the line, and the record is what
// makes the next check see it. A Budget that implements only one of them is
// either a no-op or a tripwire, and both are worse than no budget because they
// look configured.
//
// The seam is declared here rather than imported so this package does not
// depend on which ledger backs it. *llm.InMemoryBudget satisfies it, and so
// does whatever per-org ledger replaces it in a multi-tenant deployment.
type Budget interface {
	// Check reports whether a call costing about estPromptTokens may run. An
	// error means refused; the gateway does not decide what a refusal says.
	Check(ctx context.Context, estPromptTokens int) error
	// Record adds a settled call's billed token count to the current window.
	Record(ctx context.Context, tokens int) error
}

// Limiter is the per-node request rate gate.
//
// The reason is the edge id rather than a session id: a node running a
// pathological loop is one node, and a limit keyed by anything a node controls
// is a limit a node can spread itself across. Allow reports a reason as well as
// a verdict because a 429 with no explanation is indistinguishable from a
// provider outage in the agent's logs, and an operator debugging the wrong one
// is the failure this avoids.
type Limiter interface {
	Allow(ctx context.Context, edgeID uint64) (allowed bool, reason string)
}

// DefaultEdgeRequestsPerMinute is the per-node request rate when the operator
// configures none.
//
// The number is deliberately generous. A node's investigation is a loop of
// diagnose → tool → diagnose, and a real incident fires several of those per
// node; a limit low enough to be interesting would throttle exactly the
// moment the fleet is most needed. The limit exists to stop a loop, not to
// shape traffic, and 60/minute stops a loop within a second of it starting
// while leaving a human-paced investigation untouched.
const DefaultEdgeRequestsPerMinute = 60

// NewLimiter returns the per-node rate gate for a configured
// requests-per-minute value.
//
// A non-positive value returns nil, and a nil Limiter is a disabled gate
// (Allow is a method on the pointer precisely so that nil reads as "allow"
// without every call site checking). Returning nil rather than a
// zero-rate limiter is the difference between "the operator did not configure
// a limit" and "every node is refused", and conflating them turns a missing
// env var into a fleet that cannot diagnose anything.
func NewLimiter(perMinute int) Limiter {
	// The nil is returned untyped on purpose. Returning newEdgeLimiter's nil
	// *edgeLimiter would put a typed nil in this interface, which is not
	// equal to nil: it would pass every `Limiter != nil` check downstream
	// and only reveal itself as a panic on the first request of the day.
	if limiter := newEdgeLimiter(perMinute, nil); limiter != nil {
		return limiter
	}
	return nil
}

// edgeLimiter is one token bucket per node.
//
// The buckets are keyed on edge id and never removed while the node is active,
// which is correct: a fleet is bounded by the number of enrolled nodes, and a
// bucket that vanished mid-incident would hand the node a fresh allowance at
// the worst possible moment. Idle buckets are swept on a timer instead, so a
// decommissioned node's bucket does not outlive the deployment by much.
type edgeLimiter struct {
	perMinute int
	burst     int
	ttl       time.Duration

	mu        sync.Mutex
	buckets   map[uint64]*rateLimiterBucket
	lastSweep time.Time
	now       func() time.Time
}

type rateLimiterBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newEdgeLimiter returns a limiter allowing perMinute requests per node.
// A non-positive rate disables the gate entirely, which is what an operator
// who set no limit gets — not a limit of zero, which would refuse everything.
func newEdgeLimiter(perMinute int, now func() time.Time) *edgeLimiter {
	if now == nil {
		now = time.Now
	}
	if perMinute <= 0 {
		return nil
	}
	return &edgeLimiter{
		perMinute: perMinute,
		burst:     perMinute,
		ttl:       10 * time.Minute,
		buckets:   map[uint64]*rateLimiterBucket{},
		now:       now,
	}
}

// Allow takes one token for the node, and reports why not when it cannot.
func (l *edgeLimiter) Allow(_ context.Context, edgeID uint64) (bool, string) {
	if l == nil {
		// A nil limiter is a disabled one. It is written as a method rather
		// than checked at every call site so the gate has exactly one
		// "is it configured" answer.
		return true, ""
	}
	now := l.now()

	l.mu.Lock()
	bucket, ok := l.buckets[edgeID]
	if !ok {
		bucket = &rateLimiterBucket{limiter: rate.NewLimiter(rate.Limit(l.perMinute), l.burst)}
		l.buckets[edgeID] = bucket
	}
	bucket.lastSeen = now
	l.sweepLocked(now)
	l.mu.Unlock()

	if bucket.limiter.Allow() {
		return true, ""
	}
	return false, fmt.Sprintf("node %d exceeded %d model requests per minute; "+
		"the investigation is retrying shortly", edgeID, l.perMinute)
}

// sweepLocked drops buckets no node has used within the TTL.
//
// It runs under the same lock as the lookup rather than on its own timer
// because a manager process may hold hundreds of thousands of edge ids over a
// long uptime, and a goroutine that only exists to delete a map entry is a
// goroutine that has to be reasoned about at shutdown. The sweep is O(buckets)
// and runs once per minute at most, which is bounded by the fleet rather than
// by traffic.
func (l *edgeLimiter) sweepLocked(now time.Time) {
	if l.lastSweep.IsZero() {
		l.lastSweep = now
		return
	}
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for id, bucket := range l.buckets {
		if now.Sub(bucket.lastSeen) > l.ttl {
			delete(l.buckets, id)
		}
	}
}

// charge records one settled call against the cluster cap.
//
// The count is the provider's own total, and a provider that reported no
// usage is charged nothing — there is no number to charge, and inventing one
// from a guess is how a cap stops meaning anything. What the caller gets for
// that case is the usage_reported=false on its log line: an operator can see
// that a provider is running unaccounted, which is a fact worth having and is
// not the same as a zero.
func (h *Handler) charge(ctx context.Context, tokens int) {
	if h.opts.Budget == nil || tokens <= 0 {
		return
	}
	if err := h.opts.Budget.Record(ctx, tokens); err != nil {
		// The call is already made and already billed by the provider. A
		// ledger that failed to accept the count is worth a log line and
		// nothing more: there is no second action that would make the
		// provider's bill smaller.
		h.log.Warn("llmgw: usage could not be recorded against the cluster budget",
			slog.Int("tokens", tokens),
			slog.Any("err", err))
	}
}

// admission runs the two gates that decide whether a call may reach a model.
//
// It is one function called from one place so that the order is a decision
// rather than an accident of where each check was written: the rate limit
// first, because it is per node and the node is the party that can be made to
// misbehave, and the budget second, because it is global and a node that is
// merely busy must not be told the cluster is out of money.
func (h *Handler) admission(ctx context.Context, edgeID uint64) error {
	if h.opts.Limiter != nil {
		if allowed, reason := h.opts.Limiter.Allow(ctx, edgeID); !allowed {
			return fmt.Errorf("%w: %s", errs.ErrTooManyAttempts, reason)
		}
	}
	if h.opts.Budget == nil {
		return nil
	}
	// The estimate is zero on purpose. The budget here is the same daily
	// ceiling the console is gated by, and the console asks it the same way:
	// the question is whether any room is left, not what this call will cost.
	// A guess would have to be a second tokenizer, and a wrong guess is a
	// wrong answer to a question about money.
	if err := h.opts.Budget.Check(ctx, 0); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrBudgetExceeded, err)
	}
	return nil
}
