package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"

	"github.com/vincent-wuhan/opskeeper/core/edge/auditlog"
	"github.com/vincent-wuhan/opskeeper/core/floor/tunnel"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// The node's own ledger, assembled (决策 126).
//
// This file exists because a node could already decide what mattered — the
// gate, the PiG run state and the plugin installer all write through
// core/ports.AuditSink, and the sink they were handed was nil — and could
// not put it anywhere. The chain lives in the manager's database on another
// machine, so "write a row" was only ever half of what those three call
// sites meant by it.
//
// The shape of the answer is autonomy's, down to the three refusals, and
// for the same reason: both are writes to one ordered append-only chain with
// no dedupe key, and the node is the only place that can tell the
// difference between "took none of it" and "refused all of it". The
// difference is the whole ballgame — the first means keep the rows, the
// second means stop counting them.
//
// What is NOT autonomy's is the optionality. buildAutonomy returns nil when
// no manifest asked for self-healing, and that is right for a capability
// nobody declared. The ledger has no such declaration: every gated tool
// call on this host belongs in it, so a node that cannot open one refuses
// to start rather than starting an agent on the host with nowhere to record
// what it did. That is a stronger stance than the rest of the node takes
// and it is deliberate — see the note on buildAuditLedger.

// auditLedgerFile is where the node keeps its own rows, under its own
// working directory for the same reason the autonomy spool is: the evidence
// belongs to this installation and leaves with it.
const auditLedgerFile = "audit-ledger.jsonl"

// auditEntriesSender hands one batch of the node's ledger to the control
// plane, which appends it to the tamper-evident chain.
//
// The refusals are the interesting half, and they are autonomyReplaySender's
// three answers with autonomy's reasons:
//
//   - The call fails: the tunnel is down again. Error, keep the batch.
//   - The center took none and refused none: it cannot place the rows yet —
//     the node has not registered, so the manager has no identity to file
//     them under. Error, keep the batch, retry. Reading this as a permanent
//     refusal is how a backlog dies in the first message after a reconnect.
//   - The center refused some rows for shape. Retrying would ask the same
//     question forever, so they are counted and passed over. It is loud, it
//     is counted, and it is on the health line, because a row that goes this
//     way is a row that will never be evidence.
type auditEntriesSender struct {
	client tunnel.Client
	edgeID func() uint64
	log    *slog.Logger
	// refused counts rows the center will never take. A counter rather
	// than a log line because a node that has been replaying for an hour
	// should not have to be grepped to find out whether it has been
	// throwing evidence away.
	refused *atomic.Uint64
}

// Send delivers rows in order, or reports that the batch has to come again.
func (s auditEntriesSender) Send(ctx context.Context, rows []ports.AuditEntry) error {
	if len(rows) == 0 {
		return nil
	}
	req := tunnel.AuditEntriesRequest{
		EdgeID:  s.edgeID(),
		Entries: make([]tunnel.AuditEntry, 0, len(rows)),
	}
	for _, r := range rows {
		req.Entries = append(req.Entries, tunnel.AuditEntry{
			At:      r.At,
			Actor:   r.Actor,
			Action:  string(r.Action),
			Target:  r.Target,
			Outcome: r.Outcome,
			Class:   r.Class,
			Detail:  r.Detail,
		})
	}
	var resp tunnel.AuditEntriesResponse
	if err := s.client.Call(ctx, tunnel.MethodAgentAuditEntries, req, &resp); err != nil {
		// A transport failure and a refused batch must not collapse into
		// one branch: one is retried, the other is not. The pump's
		// contract is that a returned error keeps the whole batch.
		return fmt.Errorf("node ledger: send %d rows: %w", len(rows), err)
	}
	switch {
	case resp.Accepted+resp.Rejected == len(rows):
		if resp.Rejected > 0 {
			s.refused.Add(uint64(resp.Rejected))
			s.log.Warn("the center refused node ledger rows for shape; they will not be retried",
				slog.Int("accepted", resp.Accepted),
				slog.Int("rejected", resp.Rejected),
				slog.String("reason", resp.Reason))
		}
		return nil
	case resp.Accepted == 0 && resp.Rejected == 0:
		// "Took none of it" is not "refused all of it": the center is not
		// ready to place these rows, and the honest instruction is to keep
		// them.
		return fmt.Errorf("node ledger: the center accepted none of %d rows; the batch stays on disk", len(rows))
	default:
		// A count that is neither "all" nor "none" is a center this build
		// does not understand. Guessing which half it took is how rows are
		// lost; keeping the whole batch costs one more round trip.
		return fmt.Errorf("node ledger: the center reported %d accepted and %d rejected of %d rows",
			resp.Accepted, resp.Rejected, len(rows))
	}
}

// auditStack is what a node holds for its own ledger.
type auditStack struct {
	sink *auditlog.Sink
	pump *auditlog.Pump
	// refused counts rows the center took one look at and would not keep.
	// Read by Health so the number exists even when nobody reads logs.
	refused *atomic.Uint64
}

// buildAuditLedger opens the node's ledger and builds its drain.
//
// Unlike buildAutonomy this never returns a nil stack and never treats the
// ledger as optional. The reasoning is the reverse of autonomy's, and it
// turns on what is missing when the file will not open: a node with no
// ledger is a node whose policy gate records nothing, so the tool it blocks
// and the tool it allows leave the same trace. Every other safety property
// on this node — the role ceiling, the allow-list, the socket — still holds,
// which is exactly what makes the missing record dangerous: the node looks
// like it is guarding the host while the evidence of what it did is on a
// disk that does not exist. Failing the boot says "this node cannot be
// trusted to record", which is a sentence an operator can act on; running
// anyway says nothing at all.
//
// The pump is built here and started by the caller, which is autonomy's
// split for autonomy's reason: this runs before the tunnel has registered,
// and a loop that begins by draining into a manager that cannot yet place
// the rows has a boot log that claims something is wrong when nothing is.
func buildAuditLedger(
	client tunnel.Client,
	obs autonomyObservations,
	cwd string,
	log *slog.Logger,
) (*auditStack, error) {
	sink, err := auditlog.Open(filepath.Join(cwd, auditLedgerFile), 0)
	if err != nil {
		return nil, fmt.Errorf("node audit ledger: %w", err)
	}
	refused := &atomic.Uint64{}
	pump, err := auditlog.NewPump(auditlog.PumpOptions{
		Sink: sink,
		Sender: auditEntriesSender{
			client: client, edgeID: obs.EdgeID, log: log, refused: refused,
		},
		// A function rather than a Link: the only question this pump asks
		// is whether the tunnel is answering. The heartbeat is the witness
		// that actually proves the manager is there — a socket that has
		// not failed yet only shows that the network stack accepted a
		// write.
		Reachable: func() bool {
			online, _ := obs.LinkReach()
			return online
		},
		Log: log,
	})
	if err != nil {
		sink.Close()
		return nil, fmt.Errorf("node audit replay pump: %w", err)
	}
	log.Info("node audit ledger open", slog.String("path", sink.Path()))
	return &auditStack{sink: sink, pump: pump, refused: refused}, nil
}

// Health is the shape a node's health page renders, so the numbers exist
// even before anything reads them.
func (a *auditStack) Health() map[string]any {
	pending, err := a.pump.Pending()
	out := map[string]any{
		"pending": pending,
		"refused": a.refused.Load(),
		"path":    a.sink.Path(),
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return out
}
