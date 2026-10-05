package edge

import (
	"context"

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
