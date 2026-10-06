// Package approval is the biz tier for the human propose-confirm inbox
// (HLD-017). Producers (agent cloud-shell, restart_service, flow approval
// nodes) call Propose to queue a dangerous action; a human Approves/Rejects
// in the inbox UI; on Approve the registered executor for that Kind runs the
// action and the result is recorded. Strictly additive — nothing executes
// here unless a producer explicitly proposed it.
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	model "github.com/vincent-wuhan/opskeeper/core/manager/model/approval"
	"github.com/vincent-wuhan/opskeeper/core/base/pkg/errs"
)

// Repo is the persistence contract.
type Repo interface {
	Create(ctx context.Context, a *model.Approval) error
	Get(ctx context.Context, id string) (*model.Approval, error)
	List(ctx context.Context, status string, limit int) ([]*model.Approval, error)
	CountPending(ctx context.Context) (int64, error)
	Decide(ctx context.Context, id string, fields map[string]any) error
	AddSigner(ctx context.Context, id, signersJSON string) error
	SetResult(ctx context.Context, id, status, resultJSON string, executedAt time.Time) error
}

// Executor runs an approved action's payload and returns a result blob.
// Registered per Kind by the producer (e.g. cloud-shell registers
// "shell_command"). Absent executor → Approve just marks the row approved
// (no execution), which is safe.
type Executor func(ctx context.Context, payloadJSON string) (resultJSON string, err error)

// Usecase is the inbox facade.
type Usecase struct {
	repo      Repo
	log       *slog.Logger
	executors map[string]Executor
	// gate decides whether one signature is enough. nil means every approval
	// is single-signer, which is what this code did until decision 362 and
	// what a deployment without a rule file gets.
	gate Gate
}

// NewUsecase wires the repo.
func NewUsecase(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, log: log, executors: map[string]Executor{}}
}

// WithDualSignGate wires the multi-signature rule. It returns the receiver so
// it can be chained onto the constructor at the composition root.
func (u *Usecase) WithDualSignGate(g Gate) *Usecase {
	u.gate = g
	return u
}

// RegisterExecutor wires the execute-on-approve handler for a Kind. Called
// at boot by the producer subsystem (e.g. cloud-shell). Idempotent.
func (u *Usecase) RegisterExecutor(kind string, fn Executor) {
	u.executors[kind] = fn
}

// ProposeInput is what a producer queues.
type ProposeInput struct {
	Kind       string
	Title      string
	Summary    string
	Payload    any    // marshaled to PayloadJSON
	Source     string // SourceAgent / SourceFlow
	SessionID  string
	ProposedBy uint64

	// RiskClass and BlastRadius are the producer's own statement of how far
	// the action reaches. They are columns rather than payload bytes because
	// the dual-sign rules key on them, and a rule that has to parse a payload
	// to find out what it is approving is a rule that will be wrong.
	RiskClass   string
	BlastRadius string
}

// Propose records a pending action. Producer-facing (not admin-gated — the
// producer already ran under the caller's auth).
func (u *Usecase) Propose(ctx context.Context, in ProposeInput) (*model.Approval, error) {
	if strings.TrimSpace(in.Kind) == "" || strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: kind + title required", errs.ErrInvalid)
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	src := in.Source
	if src == "" {
		src = model.SourceAgent
	}
	a := &model.Approval{
		Kind: in.Kind, Title: in.Title, Summary: in.Summary,
		PayloadJSON: string(payload), Source: src, SessionID: in.SessionID,
		Status: model.StatusPending, ProposedBy: in.ProposedBy,
		RiskClass: in.RiskClass, BlastRadius: in.BlastRadius,
	}
	if err := u.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	u.log.Info("approval proposed", slog.String("id", a.ID), slog.String("kind", a.Kind), slog.String("title", a.Title))
	return a, nil
}

// List / Get / CountPending — inbox reads.
func (u *Usecase) List(ctx context.Context, status string, limit int) ([]*model.Approval, error) {
	return u.repo.List(ctx, status, limit)
}
func (u *Usecase) Get(ctx context.Context, id string) (*model.Approval, error) {
	return u.repo.Get(ctx, id)
}
func (u *Usecase) CountPending(ctx context.Context) (int64, error) { return u.repo.CountPending(ctx) }

// Sign records one signature and decides whether the row is now decided.
//
// The name is the change: the old Approve answered "approve" on its first
// call, which is what made dual sign impossible to express no matter what the
// policy said. Sign answers "here is one more signature", and the row stays
// pending until the signers cover what the gate asked for.
//
// The returned bool is "the row is decided now". A caller that is waiting on
// the action needs it: the first signature of a dual-sign row is a real event
// and the person who made it is owed an answer that says so.
func (u *Usecase) Sign(ctx context.Context, signer Signer, id string) (*model.Approval, bool, error) {
	a, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if a.Status != model.StatusPending {
		// ErrNotFound rather than ErrInvalid, and the reason is that this is
		// the code a client already got for "somebody decided this before
		// you did" — the repo's pending guard used to produce it. Deciding
		// what a second click is worth is a product question; changing the
		// answer as a side effect of adding signatures is not.
		return nil, false, fmt.Errorf("%w: approval %s is %s", errs.ErrNotFound, id, a.Status)
	}

	signers := decodeSigners(a.SignersJSON)
	// A person signs once. Recording the same user twice would be the easiest
	// possible way to satisfy a two-signature rule, so the list is keyed by
	// user rather than appended to.
	if !containsSigner(signers, signer.UserID) {
		signers = append(signers, signer)
	}

	counted := dedupeSigners(signers)
	missing := u.missing(ctx, a, counted)
	if len(missing) > 0 {
		blob, mErr := json.Marshal(counted)
		if mErr != nil {
			return nil, false, mErr
		}
		text := string(blob)
		// The row stays pending. AddSigner is guarded on pending the same way
		// Decide is, so a second person cannot overwrite a decision that
		// landed in between.
		if err := u.repo.AddSigner(ctx, id, text); err != nil {
			return nil, false, err
		}
		u.log.Info("approval signed; still waiting",
			slog.String("id", id),
			slog.String("kind", a.Kind),
			slog.Int("signers", len(counted)),
			slog.Any("missing", missing))
		a.SignersJSON = &text
		return a, false, nil
	}

	// Enough signatures. Re-assert the full list on the decision row so that
	// "approved by" and "signed by" are the same fact read two ways.
	blob, err := json.Marshal(signers)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	last := signers[len(signers)-1]
	if err := u.repo.Decide(ctx, id, map[string]any{
		"status": model.StatusApproved, "approved_by": last.UserID, "decided_at": now,
		"signers_json": string(blob),
	}); err != nil {
		return nil, false, err
	}
	return u.runExecutor(ctx, a, id)
}

// runExecutor executes an approved row and records the outcome. Split out of
// the decision so that "enough signatures" and "it ran" are two statements
// rather than one.
func (u *Usecase) runExecutor(ctx context.Context, a *model.Approval, id string) (*model.Approval, bool, error) {
	exec, ok := u.executors[a.Kind]
	if !ok {
		u.log.Warn("approved but no executor for kind", slog.String("id", id), slog.String("kind", a.Kind))
		fresh, err := u.repo.Get(ctx, id)
		return fresh, true, err
	}
	res, runErr := exec(ctx, a.PayloadJSON)
	status := model.StatusExecuted
	if runErr != nil {
		status = model.StatusFailed
		res = fmt.Sprintf(`{"error":%q}`, runErr.Error())
	}
	if err := u.repo.SetResult(ctx, id, status, res, time.Now().UTC()); err != nil {
		u.log.Warn("set approval result failed", slog.String("id", id), slog.Any("err", err))
	}
	fresh, _ := u.repo.Get(ctx, id)
	return fresh, true, nil
}

// missing asks the gate what is still outstanding on this row. No gate, or a
// gate with no rule for this row, means one signature — the behaviour this
// package had before decision 362, and the right default for a deployment
// with no rule file.
func (u *Usecase) missing(ctx context.Context, a *model.Approval, signers []Signer) []string {
	if u.gate == nil {
		if len(signers) == 0 {
			return []string{"one signature"}
		}
		return nil
	}
	return u.gate.Missing(ctx, Scope{
		Kind: a.Kind, RiskClass: a.RiskClass, BlastRadius: a.BlastRadius,
	}, signers)
}

// dedupeSigners keys the list by user, so one person signing twice is one
// signature. The caller already refuses to append a duplicate; this is the
// second half of the same rule, and it is the half that holds when the stored
// list is what is being judged.
func dedupeSigners(signers []Signer) []Signer {
	seen := make(map[uint64]struct{}, len(signers))
	out := make([]Signer, 0, len(signers))
	for _, s := range signers {
		if _, ok := seen[s.UserID]; ok {
			continue
		}
		seen[s.UserID] = struct{}{}
		out = append(out, s)
	}
	return out
}

func decodeSigners(raw *string) []Signer {
	if raw == nil || *raw == "" {
		return nil
	}
	var out []Signer
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		// A row whose signer column cannot be read reads as unsigned. For a
		// row that needs two signatures that is the safe direction — the count
		// restarts and the row waits again — and for a row that needs one it
		// costs a click, which is the price of not guessing.
		return nil
	}
	return out
}

func containsSigner(signers []Signer, userID uint64) bool {
	for _, s := range signers {
		if s.UserID == userID {
			return true
		}
	}
	return false
}

// Reject marks the proposal rejected with a reason. No execution.
func (u *Usecase) Reject(ctx context.Context, approverID uint64, id, reason string) error {
	now := time.Now().UTC()
	return u.repo.Decide(ctx, id, map[string]any{
		"status": model.StatusRejected, "approved_by": approverID,
		"reason": strings.TrimSpace(reason), "decided_at": now,
	})
}
