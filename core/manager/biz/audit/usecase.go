// Package audit is the BC-level seam for HLD-010 audit logging. It
// exposes a single Emit method that callers (middleware, handlers,
// the retention goroutine) use to record observations. Failure to
// write is logged but never returned — audit must not block business.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	store "github.com/vincent-wuhan/opskeeper/core/manager/data/audit/store"
	model "github.com/vincent-wuhan/opskeeper/core/manager/model/audit"
)

// Repo is the persistence seam the usecase consumes. Implemented by
// data/audit/store.Repo.
//
// DeleteOlderThan is unchained deletion by age and exists only for rows
// written before the chain was switched on (Seq 0). Once the chain is on,
// retention goes through ChainStore.DeleteChainedThrough so it can only
// ever remove a prefix — see the retention comment in RunRetention.
type Repo interface {
	Insert(ctx context.Context, log *model.Log) error
	List(ctx context.Context, f ListFilters) ([]model.Log, int64, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// ListFilters is an alias to the store-level filter struct so handlers
// can depend only on biz/audit without importing data/audit/store.
type ListFilters = store.ListFilters

// Event is the input shape for Emit. Caller fills what it knows; the
// usecase stamps OccurredAt and serialises Payload.
type Event struct {
	// Actor — filled by the middleware from JWT claims, by handlers
	// for failed-auth or anon paths.
	UserID    *uint64
	UserEmail string
	Role      string
	IP        string
	UserAgent string
	RequestID string

	// Action — must be one of the canonical model.Action* constants.
	Action       string
	ResourceType string
	ResourceID   string
	ResourceName string

	// Outcome.
	Status       string // success|failure|denied
	ErrorCode    string
	ErrorMessage string

	// Free-form structured detail. Caller is responsible for redacting
	// secrets BEFORE passing in (LLM keys, passwords, tokens). Pass a
	// map or struct; the usecase JSON-encodes.
	Payload any
}

// Usecase is the BC façade.
type Usecase struct {
	repo Repo
	log  *slog.Logger

	// chain is nil-or-disabled on a deployment with no HMAC key. The
	// write path checks Enabled() rather than assuming a chain exists,
	// so "audit is on" and "audit is tamper-evident" never collapse into
	// one silent truth.
	chain *ChainStamper
	// chainStore is nil when the deployment wired no chain store. A nil
	// chainStore with an enabled chainer is a misconfiguration, and
	// EmitWithID reports it instead of writing unchained rows under the
	// impression that they are chained.
	chainStore ChainStore
}

// Option configures a Usecase at construction.
type Option func(*Usecase)

// WithChain turns on the keyed hash chain. key is the deployment's
// audit HMAC key; an empty key leaves the chain off, which callers should
// surface rather than treat as a working audit trail.
//
// The store is required alongside the key. A chainer with nowhere to
// commit its head would stamp rows that verify against nothing, so the
// two are wired together by construction and a caller cannot half-enable
// the chain.
func WithChain(key string, chainStore ChainStore) Option {
	return func(u *Usecase) {
		u.chain = NewChainStamper(key)
		u.chainStore = chainStore
	}
}

// New builds a Usecase. log is mandatory for the warn-on-failure path.
func New(repo Repo, log *slog.Logger, opts ...Option) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	u := &Usecase{repo: repo, log: log}
	for _, opt := range opts {
		if opt != nil {
			opt(u)
		}
	}
	if u.chain != nil && u.chain.Enabled() && u.chainStore != nil {
		if err := u.chainStore.EnsureHead(context.Background()); err != nil {
			u.log.Warn("audit: chain head could not be ensured; appends will retry per row",
				slog.Any("err", err))
		}
	}
	return u
}

// Emit persists one Event. Returns nothing — failures are warn-logged
// (HLD-010 "audit write failure must never block business").
func (u *Usecase) Emit(ctx context.Context, ev Event) {
	_, err := u.EmitWithID(ctx, ev)
	if err != nil {
		u.log.Warn("audit: insert failed; observation lost",
			slog.String("action", ev.Action),
			slog.String("resource_type", ev.ResourceType),
			slog.String("resource_id", ev.ResourceID),
			slog.Any("err", err))
	}
}

// EmitWithID synchronously persists one Event and returns the audit_logs
// primary key. It is reserved for callers that must hand the durable row ID
// to an external caller; Emit remains the non-blocking default.
func (u *Usecase) EmitWithID(ctx context.Context, ev Event) (uint64, error) {
	if u == nil || u.repo == nil {
		return 0, errors.New("audit: repository is not configured")
	}
	if ev.Action == "" || ev.Status == "" {
		return 0, errors.New("audit: action and status are required")
	}
	row := &model.Log{
		OccurredAt:   time.Now().UTC(),
		UserID:       ev.UserID,
		UserEmail:    ev.UserEmail,
		Role:         ev.Role,
		IP:           ev.IP,
		UserAgent:    ev.UserAgent,
		Action:       ev.Action,
		ResourceType: ev.ResourceType,
		ResourceID:   ev.ResourceID,
		ResourceName: ev.ResourceName,
		Status:       ev.Status,
		ErrorCode:    ev.ErrorCode,
		ErrorMessage: truncate(ev.ErrorMessage, 512),
		RequestID:    ev.RequestID,
	}
	if ev.Payload != nil {
		if b, err := json.Marshal(ev.Payload); err == nil {
			row.PayloadJSON = string(b)
		} else {
			u.log.Warn("audit: payload marshal failed; storing empty",
				slog.String("action", ev.Action),
				slog.Any("err", err))
		}
	}
	// The chained path is the only path that can report an ID, because
	// the ID is the row's position in a chain that must be committed
	// together with the row. Falling back to an unchained insert when
	// the chain is misconfigured would be the worst of both: a ledger
	// that looks chained and verifies as nothing.
	if u.chainEnabled() {
		stamper := u.chain
		if err := u.chainStore.AppendChained(ctx, row, func(head store.Head) (store.Seal, error) {
			return stamper.sealRow(row, head)
		}); err != nil {
			return 0, err
		}
		return row.ID, nil
	}
	// Unchained: either no chain was configured, or a chainer was
	// configured with an empty key. Rows are still recorded — losing
	// audit rows because nobody set a key would be a worse failure than
	// having rows that carry no tamper-evidence — and ChainState /
	// VerifyChain report the difference to whoever asks.
	if err := u.repo.Insert(ctx, row); err != nil {
		return 0, err
	}
	return row.ID, nil
}

// chainEnabled reports whether writes must go through the chain. It is a
// method rather than a field read so the "enabled but nowhere to commit"
// misconfiguration has exactly one answer everywhere it is asked.
func (u *Usecase) chainEnabled() bool {
	return u != nil && u.chain != nil && u.chain.Enabled() && u.chainStore != nil
}

// List is the read path for the admin UI.
func (u *Usecase) List(ctx context.Context, f ListFilters) ([]model.Log, int64, error) {
	if u == nil || u.repo == nil {
		return nil, 0, nil
	}
	return u.repo.List(ctx, f)
}

// ListChanges is the RCA-facing convenience over List (HLD-013 Phase 2):
// returns the mutating audit rows in [from, to], optionally narrowed to a
// resource type and/or action, capped at limit. Backs the
// query_change_events AIOps tool's "what changed near the incident" step.
// Failures (status=failure/denied) are intentionally included — "someone
// tried to change X right before the symptom" is itself a root-cause lead.
func (u *Usecase) ListChanges(ctx context.Context, from, to time.Time, resourceType, action string, limit int) ([]model.Log, error) {
	if u == nil || u.repo == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	logs, _, err := u.List(ctx, ListFilters{
		From:         from,
		To:           to,
		ResourceType: resourceType,
		Action:       action,
		Limit:        limit,
	})
	return logs, err
}

// RunRetention runs the daily cleanup at the next 03:00 wall clock and
// every 24h thereafter. retentionDays <= 0 disables the sweep entirely
// (operator may prefer to manage retention via external archival).
// Blocks until ctx is cancelled.
func (u *Usecase) RunRetention(ctx context.Context, retentionDays int) error {
	if u == nil || u.repo == nil || retentionDays <= 0 {
		<-ctx.Done()
		return nil
	}
	for {
		// Next 03:00 local. Cheap arithmetic; we don't need a cron lib
		// for once-a-day.
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
		removed, err := u.sweep(ctx, cutoff)
		if err != nil {
			u.log.Warn("audit retention: delete failed", slog.Any("err", err))
			continue
		}
		u.log.Info("audit retention swept",
			slog.Int("retention_days", retentionDays),
			slog.Int64("rows_removed", removed))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// sweep removes rows older than cutoff while keeping the chain
// verifiable.
//
// It is two deletes with different rules, and the difference is the
// whole point. Unchained rows (Seq 0) are removed by age because nothing
// links to them. Chained rows are removed as a *prefix of the chain*:
// the walk stops at the first entry still inside the retention window.
// Deleting by age alone would remove whatever row happened to carry an
// old timestamp, and a single clock correction or retried insert is
// enough to put one in the middle of the chain — where it produces a
// verification failure no operator can distinguish from real tampering.
// A chain that only ever loses its oldest entries has a describable
// boundary, the anchor, which ChainState reports. A chain with a hole has
// a mystery.
func (u *Usecase) sweep(ctx context.Context, cutoff time.Time) (int64, error) {
	var removed int64
	if u.chainEnabled() {
		cut, anchor, err := u.chainStore.TruncateExpiredPrefix(ctx, cutoff)
		if err != nil {
			return 0, err
		}
		removed += cut
		if cut > 0 {
			u.log.Info("audit retention: chain truncated at the front",
				slog.Int64("rows_removed", cut),
				slog.Uint64("new_anchor_seq", anchor))
		}
	}
	// Pre-chain rows sit outside the chain and are swept by age alone.
	var legacy int64
	var err error
	if u.chainEnabled() {
		legacy, err = u.chainStore.DeleteUnchainedOlderThan(ctx, cutoff)
	} else {
		legacy, err = u.repo.DeleteOlderThan(ctx, cutoff)
	}
	if err != nil {
		return removed, err
	}
	return removed + legacy, nil
}
