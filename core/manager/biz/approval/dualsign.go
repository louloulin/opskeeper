package approval

import (
	"context"
	"time"
)

// The two pieces ADR-019 needs and this package did not have: somewhere to
// keep the signers, and a way to ask whether there are enough of them.
//
// Both are ports rather than direct calls into biz/hitl. hitl owns the rule
// file; approval owns the row. Whichever of them imported the other would be a
// dependency edge between two domains of the same module, and the direction of
// that edge would be decided by which file somebody opened first. The
// composition root implements Gate over hitl's validator, exactly as it
// assembles the reader-tier gate over the label store and the iam enforcer.

// Signer is one person who has signed an approval.
type Signer struct {
	UserID uint64    `json:"user_id"`
	Role   string    `json:"role"`
	At     time.Time `json:"at"`
}

// Scope is what an approval is asking to be signed for, and it is the row's
// own words rather than anything inferred at decision time: the producer said
// "this is destructive and it reaches a cluster" when it queued the row, and
// re-deciding that at approve time would let the answer change underneath the
// first signer.
type Scope struct {
	Kind        string
	RiskClass   string
	BlastRadius string
}

// Gate answers the one question dual sign asks: are these signatures enough?
type Gate interface {
	// Missing returns the role groups the signers do not yet cover. An empty
	// answer means the signatures are sufficient and the row may be decided.
	//
	// It takes no "required" argument on purpose. An earlier shape of this
	// port asked the gate twice — once for the requirement, once for the
	// verdict — and the second call could only ignore the first, which is a
	// way to make two rules disagree and only ship one.
	Missing(ctx context.Context, scope Scope, signers []Signer) []string
}
