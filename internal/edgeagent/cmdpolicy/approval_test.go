package cmdpolicy

import (
	"errors"
	"testing"
	"time"
)

// frozenClock returns a clock that always reports t. Used by approval
// tests so token TTL behaviour is deterministic.
func frozenClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestApprovalCache_PutConsume(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	c := NewApprovalCache().WithClock(frozenClock(now))

	tok := &ApprovalToken{
		TokenID:    "tk-1",
		ProposalID: "prop-1",
		Kind:       "restart_service",
		EdgeID:     "edge-a",
		GrantedAt:  now,
		ExpiresAt:  now.Add(DefaultApprovalTTL),
	}
	if !c.Put(tok) {
		t.Fatalf("Put should succeed for valid token")
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}

	got, err := c.Consume("tk-1", "prop-1", "restart_service", "edge-a")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got.TokenID != "tk-1" {
		t.Errorf("returned TokenID = %q", got.TokenID)
	}
	if c.Len() != 0 {
		t.Errorf("Consume should remove the token; Len = %d", c.Len())
	}
}

func TestApprovalCache_ConsumeRejectsUnknownToken(t *testing.T) {
	c := NewApprovalCache()
	_, err := c.Consume("nope", "p", "k", "e")
	if !errors.Is(err, errApprovalMissing) {
		t.Errorf("expected errApprovalMissing, got %v", err)
	}
}

func TestApprovalCache_ConsumeRejectsExpired(t *testing.T) {
	granted := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	c := NewApprovalCache().WithClock(frozenClock(granted))
	tok := &ApprovalToken{
		TokenID: "tk-exp", ProposalID: "p", Kind: "k", EdgeID: "e",
		GrantedAt: granted, ExpiresAt: granted.Add(time.Minute),
	}
	if !c.Put(tok) {
		t.Fatalf("Put failed")
	}
	// Jump 5 minutes into the future.
	c.WithClock(frozenClock(granted.Add(5 * time.Minute)))
	_, err := c.Consume("tk-exp", "p", "k", "e")
	if !errors.Is(err, errApprovalExpired) {
		t.Errorf("expected errApprovalExpired, got %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("expired token should be evicted, Len = %d", c.Len())
	}
}

func TestApprovalCache_ConsumeMismatch(t *testing.T) {
	c := NewApprovalCache()
	tok := &ApprovalToken{
		TokenID: "tk", ProposalID: "prop-1", Kind: "restart_service", EdgeID: "edge-a",
		GrantedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}
	c.Put(tok)
	cases := []struct {
		name              string
		tokenID, prop, k, e string
		want              error
	}{
		{"proposal_mismatch", "tk", "prop-OTHER", "restart_service", "edge-a", errApprovalMismatch},
		{"kind_mismatch", "tk", "prop-1", "write_file", "edge-a", errApprovalKindMismatch},
		{"edge_mismatch", "tk", "prop-1", "restart_service", "edge-b", errApprovalEdgeMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Consume(tc.tokenID, tc.prop, tc.k, tc.e)
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestApprovalCache_PutRejectsZeroWindow(t *testing.T) {
	c := NewApprovalCache()
	now := time.Now()
	if c.Put(&ApprovalToken{
		TokenID: "x", ProposalID: "p", Kind: "k", EdgeID: "e",
		GrantedAt: now, ExpiresAt: now,
	}) {
		t.Errorf("Put should reject zero-window token")
	}
	if c.Put(&ApprovalToken{TokenID: "", ProposalID: "p", Kind: "k", EdgeID: "e",
		GrantedAt: now, ExpiresAt: now.Add(time.Second)}) {
		t.Errorf("Put should reject empty TokenID")
	}
	if c.Put(nil) {
		t.Errorf("Put(nil) should reject")
	}
}

func TestApprovalCache_EvictExpired(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewApprovalCache().WithClock(frozenClock(start))
	// Two tokens: one expires at start+10s, one at start+5m.
	c.Put(&ApprovalToken{TokenID: "soon", ProposalID: "p", Kind: "k", EdgeID: "e",
		GrantedAt: start, ExpiresAt: start.Add(10 * time.Second)})
	c.Put(&ApprovalToken{TokenID: "later", ProposalID: "p", Kind: "k", EdgeID: "e",
		GrantedAt: start, ExpiresAt: start.Add(5 * time.Minute)})
	// Jump past the first token's expiry.
	c.WithClock(frozenClock(start.Add(30 * time.Second)))
	if n := c.EvictExpired(); n != 1 {
		t.Errorf("EvictExpired = %d, want 1", n)
	}
	if c.Len() != 1 {
		t.Errorf("Len after eviction = %d, want 1", c.Len())
	}
}

func TestCacheApprovalChecker_DeniesWithoutToken(t *testing.T) {
	c := NewApprovalCache()
	fn := CacheApprovalChecker(c, "", "edge-a")
	if err := fn("p", "k"); !errors.Is(err, errApprovalMissing) {
		t.Errorf("empty bearer should deny with errApprovalMissing, got %v", err)
	}
}

func TestCacheApprovalChecker_HappyPath(t *testing.T) {
	c := NewApprovalCache()
	now := time.Now()
	c.Put(&ApprovalToken{
		TokenID: "tk-h", ProposalID: "p-1", Kind: "restart_service", EdgeID: "edge-a",
		GrantedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	fn := CacheApprovalChecker(c, "tk-h", "edge-a")
	if err := fn("p-1", "restart_service"); err != nil {
		t.Errorf("happy-path approval should pass, got %v", err)
	}
	// Single-use: second call must fail.
	if err := fn("p-1", "restart_service"); !errors.Is(err, errApprovalMissing) {
		t.Errorf("second call should deny (single-use), got %v", err)
	}
}