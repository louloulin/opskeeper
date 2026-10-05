package edge

import (
	"context"

	"github.com/vincent-wuhan/opskeeper/core/base/pkg/errs"
	"github.com/vincent-wuhan/opskeeper/core/domain"
)

// The port is satisfied or the tree does not build. That is the whole guard:
// `alert` and `systemhealth` hold a domain.EdgeQuery, and the only thing that
// keeps this method from quietly disappearing is that something in another
// package is asking for it by name.
var _ domain.EdgeQuery = (*Usecase)(nil)

// ListPresence implements domain.EdgeQuery, the port the alert pipeline's
// staleness gauge and the system-health edge probe hold.
//
// It is the only thing this package exposes to those two domains, and it is a
// projection rather than a pass-through on purpose: both callers read a
// presence question ("is this node registered, when was it last seen, is it
// online") and neither reads a node's identity or its deletion state. Handing
// them `*model.Edge` and letting them pick fields is how a credential column
// ends up reachable from a gauge that has no use for it.
//
// The limit arrives as a plain int because that is all either caller has ever
// passed. A filter struct with a single legal value is a type whose second
// value will be wrong, and this port has no need for one yet.
func (u *Usecase) ListPresence(ctx context.Context, limit int) ([]domain.EdgePresence, error) {
	edges, err := u.List(ctx, ListFilter{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]domain.EdgePresence, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			// A slice of pointers from a query that soft-deletes can
			// carry a nil if a row is scanned into a nil pointer; the
			// two callers both range over the result and would
			// dereference it. Dropping it here keeps that impossible
			// rather than leaving it to every future caller.
			continue
		}
		out = append(out, domain.EdgePresence{
			ID:         e.ID,
			Name:       e.Name,
			Status:     e.Status,
			DeviceID:   e.DeviceID,
			LastSeenAt: e.LastSeenAt,
			CreatedAt:  e.CreatedAt,
		})
	}
	return out, nil
}

// var _ domain.EdgeStatusQuery = (*Usecase)(nil) is the guard for the second
// port, and it is the same guard as the one above for the same reason: a
// method that nothing in another package asks for by name is a method that
// can be deleted by a later reader who sees no caller in this file.
var _ domain.EdgeStatusQuery = (*Usecase)(nil)

// PresenceStatus implements domain.EdgeStatusQuery, the port the webshell
// holds to decide whether it may open a shell onto a node.
//
// It reads one column of the row and hands back one string, because that is
// the whole question: the handler compares it against domain.EdgeStatusOnline
// and puts it in an error message when it does not match. Everything else the
// row carries — the name, the device link, the last-seen stamp, and the six
// credential and bookkeeping columns — stays inside this domain.
//
// The error path is the row being absent. `u.Get` already maps a missing row
// to errs.ErrNotFound, so there is no "gone but not an error" state to
// represent, which is why this returns a string and not a pointer: the caller
// used to hold `*model.Edge` and carry a nil check for a `(nil, nil)` that no
// implementation in this tree can return.
func (u *Usecase) PresenceStatus(ctx context.Context, id uint64) (string, error) {
	edge, err := u.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if edge == nil {
		// Unreachable through u.Get, which is the point: it is here so
		// that a future repo implementation returning (nil, nil) fails
		// here rather than handing a caller a bare string that reads as
		// a presence state nobody wrote.
		return "", errs.ErrNotFound
	}
	return edge.Status, nil
}
