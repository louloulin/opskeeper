package frontierbound

import (
	"context"
	"errors"
	"fmt"

	auditbiz "github.com/vincent-wuhan/opskeeper/core/domains/biz/audit"
	"github.com/vincent-wuhan/opskeeper/core/floor/tunnel"
)

// AutonomyReplay turns the wire shape of a node's self-heal rows into the
// audit BC's own vocabulary, and hands the batch to the chain.
//
// It lives here rather than in biz/audit for the reason the biz type's own
// comment gives: the wire agrees with the network and the ledger agrees
// with the domain, and the conversion is the only place the two are allowed
// to meet. A field added to the wire for one node's convenience then
// reaches the ledger only if somebody decides it should, rather than
// arriving there by aliasing.
//
// The type carries no state beyond the usecase. A recorder that buffered,
// retried, or reordered would be a second policy about the chain's order,
// and the node's pump already owns that policy: it sends a prefix, it
// waits, and it retries the whole prefix or none of it.
type AutonomyReplay struct {
	uc *auditbiz.Usecase
}

// NewAutonomyReplay returns the recorder the frontierbound Wiring wants.
func NewAutonomyReplay(uc *auditbiz.Usecase) *AutonomyReplay {
	return &AutonomyReplay{uc: uc}
}

// RecordAutonomyReplay converts and records one batch.
//
// A nil usecase is an error rather than a silent zero: the two ways to get
// here with nothing to write into are a boot sequence that skipped the
// audit BC and a future refactor that drops the constructor argument, and
// both of them should make the node's log say "the center could not take
// this" rather than "the center took nothing", which the node reads as
// "keep trying" — the same instruction, but one that names who is at
// fault.
func (a *AutonomyReplay) RecordAutonomyReplay(ctx context.Context, edgeID uint64, rows []tunnel.AutonomyAuditRow) (int, int, error) {
	if a == nil || a.uc == nil {
		return 0, 0, errors.New("frontierbound: autonomy replay has no audit usecase behind it")
	}
	converted := make([]auditbiz.AutonomyReplayRow, 0, len(rows))
	for _, r := range rows {
		converted = append(converted, auditbiz.AutonomyReplayRow{
			At:        r.At,
			Action:    r.Action,
			Package:   r.Package,
			Tool:      r.Tool,
			Target:    r.Target,
			Argv:      r.Argv,
			Kind:      r.Kind,
			Metric:    r.Metric,
			Threshold: r.Threshold,
			Key:       r.Key,
			Verdict:   r.Verdict,
			Reason:    r.Reason,
			Phase:     r.Phase,
			Result:    r.Result,
			ExitCode:  r.ExitCode,
		})
	}
	res, err := a.uc.RecordAutonomyReplay(ctx, edgeID, converted)
	if err != nil {
		return res.Accepted, res.Rejected, fmt.Errorf("autonomy replay: edge %d: %w", edgeID, err)
	}
	return res.Accepted, res.Rejected, nil
}
