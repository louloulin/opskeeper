package cmdpolicy

import "time"

// DefaultApprovalTTL is the lifetime of an approval token issued by the
// cloud reviewer worker. Short by design: the token is single-use and
// must outlive only the round-trip from edge proposing → reviewer
// granting → edge consuming. 60 s matches the AGENTS.md "double-sign"
// rule; longer would let stale tokens sit in edge's memory waiting to
// be reused.
const DefaultApprovalTTL = 60 * time.Second

// ApprovalToken is a single-use bearer that authorises one mutating
// call from the Pi side. The cache holds these until consumed or
// expired; a successful Consume removes the entry so it cannot be
// replayed.
//
// The token is bound to (TokenID, ProposalID, Kind, EdgeID). The
// ProposalID comes from the /v1/edge/propose request that preceded
// the grant; the Kind is the action class ("restart_service",
// "write_file", ...). TokenID is the bearer value the Pi side
// presents in the X-Opskeeper-Pi-Approval-Token header.
type ApprovalToken struct {
	TokenID    string
	ProposalID string
	Kind       string
	EdgeID     string
	GrantedAt  time.Time
	ExpiresAt  time.Time
}

// Valid reports whether the token is still within its TTL window.
// Expired-but-not-yet-evicted tokens fail this check.
func (t *ApprovalToken) Valid() bool {
	if t == nil {
		return false
	}
	now := time.Now()
	return !now.Before(t.GrantedAt) && now.Before(t.ExpiresAt)
}

// =====================================================================
// ApprovalCache — in-memory store for live tokens
// =====================================================================

// ApprovalCache is the in-memory token store used by edge. The cache is
// thread-safe; callers may Put / Consume / Len from any goroutine.
//
// In v1 the cache is populated only by the tunnel.v1.pi_approval_grant
// handler on the edge side. A future iteration may hydrate from the
// audit store on cold start, but that requires a strict "replay
// protection" guarantee we don't yet have — for now tokens are
// process-local and lost on edge restart. That is acceptable because
// the cloud side will re-grant on demand (a fresh proposal → fresh
// grant), and the proposed action's effect should be idempotent.
type ApprovalCache struct {
	nowFn  func() time.Time
	tokens map[string]*ApprovalToken
}

// NewApprovalCache returns an empty cache. The wall clock is read from
// time.Now; tests can override via WithClock.
func NewApprovalCache() *ApprovalCache {
	return &ApprovalCache{tokens: map[string]*ApprovalToken{}}
}

// WithClock swaps the clock source. Returns the receiver for chaining.
// Intended for tests only.
func (c *ApprovalCache) WithClock(now func() time.Time) *ApprovalCache {
	if now != nil {
		c.nowFn = now
	}
	return c
}

func (c *ApprovalCache) now() time.Time {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now()
}

// Put stores a token. Caller must populate TokenID, ProposalID, Kind,
// EdgeID, GrantedAt, ExpiresAt. A zero or past ExpiresAt is rejected
// (returns false) so we never cache already-dead tokens.
func (c *ApprovalCache) Put(t *ApprovalToken) bool {
	if t == nil || t.TokenID == "" || t.ProposalID == "" || t.Kind == "" {
		return false
	}
	if !t.ExpiresAt.After(t.GrantedAt) {
		return false
	}
	c.tokens[t.TokenID] = t
	return true
}

// Consume atomically validates and removes a token. On success the
// token is gone from the cache (single-use). On failure the token is
// left in place so legitimate retries during a transient lookup race
// can succeed — but a missing/expired/wrong-binding token is rejected.
//
// Errors are exported via errApproval* sentinels so callers can
// distinguish "no such token" from "expired" from "wrong proposal".
func (c *ApprovalCache) Consume(tokenID, proposalID, kind, edgeID string) (*ApprovalToken, error) {
	if tokenID == "" {
		return nil, errApprovalMissing
	}
	t, ok := c.tokens[tokenID]
	if !ok {
		return nil, errApprovalMissing
	}
	if t.ProposalID != proposalID {
		return nil, errApprovalMismatch
	}
	if t.Kind != kind {
		return nil, errApprovalKindMismatch
	}
	if t.EdgeID != "" && edgeID != "" && t.EdgeID != edgeID {
		return nil, errApprovalEdgeMismatch
	}
	if !c.now().Before(t.ExpiresAt) {
		delete(c.tokens, tokenID)
		return nil, errApprovalExpired
	}
	delete(c.tokens, tokenID)
	return t, nil
}

// Len reports the number of live tokens (after expiring stale ones).
// Mostly for tests + boot logging.
func (c *ApprovalCache) Len() int {
	c.evictExpired()
	return len(c.tokens)
}

// EvictExpired drops tokens whose ExpiresAt has passed. Called
// opportunistically by Len / Consume; not run on a timer (the cache
// is small and Consume's delete-on-expire is the main path).
func (c *ApprovalCache) EvictExpired() int {
	return c.evictExpired()
}

func (c *ApprovalCache) evictExpired() int {
	now := c.now()
	n := 0
	for k, t := range c.tokens {
		if !now.Before(t.ExpiresAt) {
			delete(c.tokens, k)
			n++
		}
	}
	return n
}

// Sentinel errors returned by Consume. Callers (typically the
// /v1/edge/tools/* handlers) map these to HTTP 401 / 403 / 410.
var (
	errApprovalMissing       = approvalError("approval token not found")
	errApprovalMismatch      = approvalError("approval token does not match proposal")
	errApprovalKindMismatch  = approvalError("approval token kind does not match action")
	errApprovalEdgeMismatch  = approvalError("approval token edge_id does not match this edge")
	errApprovalExpired       = approvalError("approval token expired")
)

// approvalError is a typed error so callers can errors.Is against the
// sentinels without parsing Reason strings.
type approvalError string

func (e approvalError) Error() string { return string(e) }

// =====================================================================
// DefaultApprovalChecker — Sandbox integration helper
// =====================================================================

// ApprovalCheckerFunc is the signature Sandbox.Exec calls before
// executing any write-classified segment. Returning nil means the
// proposal is approved and the call may proceed; returning an error
// means the call is denied with that error as the Reason.
//
// Pass nil as Sandbox.ApprovalChecker to disable the gate entirely
// (DefaultReadOnly users don't need it).
type ApprovalCheckerFunc func(proposalID, kind string) error

// CacheApprovalChecker wraps an ApprovalCache as an ApprovalCheckerFunc
// for Sandbox. The bearerToken is the value pulled out of the
// X-Opskeeper-Pi-Approval-Token HTTP header — it is constant per
// request, the per-segment proposalID/kind vary.
//
// Returned closure captures the bearer + cache + edgeID; one closure
// per request, no shared mutable state.
func CacheApprovalChecker(c *ApprovalCache, bearerToken, edgeID string) ApprovalCheckerFunc {
	if c == nil || bearerToken == "" {
		// No cache or no token → always deny. This is intentional:
		// the caller should not invoke the closure when no token is
		// presented; we surface a clean error rather than silently
		// letting a write through.
		return func(string, string) error {
			return errApprovalMissing
		}
	}
	return func(proposalID, kind string) error {
		_, err := c.Consume(bearerToken, proposalID, kind, edgeID)
		return err
	}
}