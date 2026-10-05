package domain

import (
	"context"
	"time"
)

// This file is the answer to a measurement, not a design sketch.
//
// The edge domain's Usecase exposes twenty methods. Four bounded contexts call
// into it, and between them they call nine. Two of those four — the alert
// pipeline's staleness gauge and the system-health edge probe — call exactly
// one method, `List`, and both pass the same argument: a limit of 1000 and
// nothing else. They never touch a filter field.
//
// So the seam between them and the edge domain is one method returning six
// columns, and both of them already had a local interface saying so. What they
// could not do was name the types, because the signature said `edgebiz.Edge`
// and `edgemodel.Edge` — a package-shaped boundary, not an interface one
// (decision 218). Moving the six columns here is what turns it into an
// interface boundary, and it is why this file carries no GORM tag and no
// entity: a value the two callers read is not an entity, and importing one
// would put a database row in the contract layer.

// Edge presence states. These are the values the edges table's status column
// is constrained to, repeated here as plain constants so a consumer that only
// counts them does not have to import the model package to name them.
//
// They are declared as untyped string constants rather than a named type on
// purpose: `EdgePresence.Status` stays a `string`, so the edge domain's
// existing `Edge.Status` assigns to it with no conversion and no second
// vocabulary to keep in step. A named type here would be a third spelling of
// the same two values, and this repository has already been bitten by that
// shape twice (decisions 229, 232).
const (
	EdgeStatusOnline  = "online"
	EdgeStatusOffline = "offline"
)

// EdgePresence is the part of a registered node that a consumer outside the
// edge domain is allowed to see.
//
// It is six fields out of fifteen, and the nine it leaves behind are not
// arbitrary: what is left is credentials (`AccessKeyID`, `SecretKeyHash`),
// the soft-delete mechanism (`DeleteMarker`, `DeletedAt`), version self-report
// (`AgentVersion`, `PigVersion`) and bookkeeping (`Description`, `UpdatedAt`,
// `CreatedBy`). A consumer that could reach any of those would be able to
// assert things about a node's identity or its deletion, and neither of the
// two callers here needs either.
//
// `LastSeenAt` is a pointer because the column is nullable and a node that
// has never been seen has no value to report — the gauge falls back to
// `CreatedAt` in that case, and that fallback is a fact about the data, not
// something this type should smooth over.
type EdgePresence struct {
	ID         uint64
	Name       string
	Status     string
	DeviceID   *uint64
	LastSeenAt *time.Time
	CreatedAt  time.Time
}

// EdgeQuery is the port the alert pipeline and the system-health probe hold.
//
// One method, deliberately. A wider port would be a port every future caller
// could reach through, and the reason this edge was worth cutting is that
// neither caller touches anything else. If a second consumer later needs the
// node's plugin health, it gets a second method here with its own
// justification — not a widening of this one by accretion.
type EdgeQuery interface {
	// ListPresence returns up to limit registered nodes, most useful first
	// as the edge domain orders them. The limit is passed as a plain int
	// rather than a filter struct because both callers pass the same
	// constant: a filter type here would be a shape with one legal value,
	// which is a type that exists to be wrong later.
	ListPresence(ctx context.Context, limit int) ([]EdgePresence, error)
}

// EdgeStatusQuery is the port the webshell holds, and it is a second
// interface rather than a second method on EdgeQuery on purpose.
//
// The file above says a wider port is a port every future caller can reach
// through, and it offers the remedy as "a second method here with its own
// justification". Read strictly, that remedy is a second *port*: alert and
// systemhealth hold EdgeQuery to count stale nodes, and handing them a point
// lookup they never asked for is the same accretion one method later. The
// three consumers ask three different questions, and a question that has one
// holder has no business sharing a type with a question that has two.
//
// The answer is one string, not an EdgePresence. This consumer reads exactly
// one column of the fifteen the model carries — the presence state — and the
// projection principle the rest of this file follows is "move the columns the
// caller reads", not "move a row that contains them". A record whose only use
// is to be compared against EdgeStatusOnline is a struct with one legal
// comparison, and the two untyped constants above already exist so that
// naming a state costs no import.
//
// The missing row is an error rather than a zero value, and that is what
// removes a branch from the caller: a `*model.Edge` port can answer
// `(nil, nil)`, so its holder has to carry a "the row is gone" check that no
// implementation in this tree can produce. A string cannot, so the question
// stops being askable.
type EdgeStatusQuery interface {
	// PresenceStatus returns the node's presence state — one of
	// EdgeStatusOnline or EdgeStatusOffline — or an error if the node is
	// not registered. It does not fall back to any other value, because
	// "unknown" is a claim about a node and this port is only allowed to
	// make claims the edges table already makes.
	PresenceStatus(ctx context.Context, id uint64) (string, error)
}
