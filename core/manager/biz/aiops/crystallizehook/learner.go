// Package crystallizehook adapts the closed loop's recovery evidence to the
// crystalliser's ledger.
//
// It is the production wiring the plan's item 7 was missing. The crystallise
// package already knows how to decide "this pattern has earned a runbook",
// and the loop package already knows how to say "this run verified cleanly
// with this argv". What no build had was the object that holds one and is
// told by the other across runs, which is why the ledger existed with a
// Record method and no caller.
//
// The adapter is deliberately the only place that assembles a Trial from a
// RecoveryEvidence, so the questions that need judgement live in one file:
//
//   - Which runs are evidence? A first-try verification is; a pass that
//     needed a rollback is not (it contradicts the fix's own claim), and a
//     failed verification is negative evidence. That mapping is decided by
//     crystallize.OutcomeOf, not re-derived here.
//   - Which tool class? It comes from the tool registry the fix dispatched
//     through, never from the action's name: an L2 soft write and an L3 hard
//     write can share a verb in different adapters, and a runbook's safety
//     level is set from the class.
//   - Which reach and window? They are a policy input. The ledger only saw a
//     human approve an action with no explicit radius, so the adapter stamps
//     the operator's configured default and says so.
package crystallizehook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/crystallize"
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/loop"
	"github.com/vincent-wuhan/opskeeper/core/ports"
)

// ToolSpecSource answers a tool's declared risk and class.
//
// It is ports.ToolCaller's LookupTool half, taken as its own interface
// because the hook never runs a tool — it only reads what the tool says
// about itself. A hook that could dispatch would be a second path to
// execution independent of the approval gate, which is the one thing the
// platform does not have twice.
type ToolSpecSource interface {
	LookupTool(name string) (ports.ToolSpec, bool)
}

// Config is the operator's policy for what a crystallised action may carry.
//
// The zero value is not usable: without a radius the emitted declaration
// would be refused by the ledger's own validation, so Default's values are
// required rather than optional.
type Config struct {
	// ToolClass maps a tool's declared risk level to the class stamped on
	// the trial. A tool the registry does not know is refused, not guessed.
	ToolClass func(riskLevel string) (domain.ToolClass, bool)

	// BlastRadius is the reach an operator is willing to grant a pattern
	// proven on this deployment. It is policy rather than evidence: the
	// loop's approval carries a target, not a radius.
	BlastRadius domain.BlastRadius

	// TTL is how long a crystallised action may stay live. The ledger
	// caps it further; this is the operator's own ceiling.
	TTL time.Duration

	// Policy is the ledger's promotion rule. Zero values take the
	// crystallise package's defaults (three clean runs).
	Policy crystallize.Policy
}

// Learner is the loop.RecoveryCrystallizer that owns the ledger.
//
// It is safe for concurrent use: the ledger counts streaks across
// incidents, and two recovered phases can finish at the same time.
type Learner struct {
	tools  ToolSpecSource
	ledger *crystallize.Ledger
	cfg    Config
	log    *slog.Logger

	mu       sync.Mutex
	lastErr  error
	recorded int
}

// New constructs the learner. tools is required — without a tool registry a
// trial cannot carry the class the emitted declaration is graded on, and a
// guessed class is how a runbook is written at the wrong safety level.
func New(tools ToolSpecSource, cfg Config, log *slog.Logger) (*Learner, error) {
	if tools == nil {
		return nil, errors.New("crystallizehook: a tool registry is required: a trial's class comes from the tool, not from its name")
	}
	if cfg.ToolClass == nil {
		return nil, errors.New("crystallizehook: Config.ToolClass is required: the host must state how a risk level becomes a class")
	}
	if !cfg.BlastRadius.Valid() || cfg.BlastRadius == domain.RadiusNone {
		return nil, fmt.Errorf("crystallizehook: Config.BlastRadius %q is not a reach a human can grant", cfg.BlastRadius)
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("crystallizehook: Config.TTL must be positive")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Learner{
		tools:  tools,
		ledger: crystallize.NewLedger(cfg.Policy),
		cfg:    cfg,
		log:    log,
	}, nil
}

// Ledger exposes the underlying ledger for a console that renders promoted
// patterns and their drafts. Nil until New has run.
func (l *Learner) Ledger() *crystallize.Ledger { return l.ledger }

// LastError returns the most recent refusal, for a health surface.
func (l *Learner) LastError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}

// Recorded reports how many trials this learner has accepted. It exists so a
// test can assert the ledger was reached without reaching into it.
func (l *Learner) Recorded() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recorded
}

// Learn implements loop.RecoveryCrystallizer.
//
// It returns an error for a run it declined to record, and the caller is
// expected to treat that as advisory — the recovery already verified, and a
// learning failure must not un-verify it. The reason is stored for the
// health surface either way.
func (l *Learner) Learn(_ context.Context, ev loop.RecoveryEvidence) error {
	trial, ok, why := l.trialOf(ev)
	if !ok {
		return l.note(why)
	}
	rep, err := l.ledger.Record(trial)
	if err != nil {
		return l.note(fmt.Errorf("crystallize: record trial for %s: %w", ev.Target, err))
	}
	l.mu.Lock()
	l.recorded++
	l.lastErr = nil
	l.mu.Unlock()
	l.log.Info("crystallize: recovery recorded",
		slog.String("incident", ev.IncidentID),
		slog.String("target", ev.Target),
		slog.String("tool", ev.Tool),
		slog.String("verdict", string(rep.Verdict)),
		slog.String("reason", rep.Reason))
	return nil
}

func (l *Learner) note(err error) error {
	if err == nil {
		return nil
	}
	l.mu.Lock()
	l.lastErr = err
	l.mu.Unlock()
	l.log.Warn("crystallize: recovery not recorded", slog.Any("err", err))
	return err
}

// trialOf assembles the ledger's input, or explains why this run is not one.
//
// The refusal messages are the point of the function: a run the platform
// cannot learn from is a finding about the wiring, and stating which field
// was missing is what makes it fixable rather than mysterious.
func (l *Learner) trialOf(ev loop.RecoveryEvidence) (crystallize.Trial, bool, error) {
	if _, ok := crystallize.OutcomeOf(ev.Verified); !ok {
		return crystallize.Trial{}, false, fmt.Errorf("crystallize: %s verified nothing this run can be read from", ev.IncidentID)
	}
	if len(ev.Argv) == 0 {
		// The action reached its change without an exec (a SQL statement, an
		// API call), or the adapter did not report the vector. Either way
		// there is no program a node could re-run, and inventing one would
		// promote a different action than the one that worked.
		return crystallize.Trial{}, false, fmt.Errorf("crystallize: %s ran %s with no literal argv; an action with nothing to re-run is not a runbook", ev.IncidentID, ev.Tool)
	}
	spec, ok := l.tools.LookupTool(ev.Tool)
	if !ok {
		return crystallize.Trial{}, false, fmt.Errorf("crystallize: %s names tool %q, which is not registered, so its class cannot be read", ev.IncidentID, ev.Tool)
	}
	class, ok := l.cfg.ToolClass(spec.RiskLevel)
	if !ok {
		return crystallize.Trial{}, false, fmt.Errorf("crystallize: tool %q declares risk %q, which this host cannot map to a class", ev.Tool, spec.RiskLevel)
	}
	rem := loop.RemediationOption{Action: ev.Tool, Target: ev.Target}
	trial, ok := crystallize.TrialOf(ev.At, ev.IncidentID, rootCauseFor(ev), ev.Verified, rem, crystallize.Execution{
		Tool:        ev.Tool,
		Class:       class,
		Argv:        ev.Argv,
		Trigger:     ev.Trigger,
		BlastRadius: l.cfg.BlastRadius,
		TTL:         l.cfg.TTL,
	})
	if !ok {
		return crystallize.Trial{}, false, fmt.Errorf("crystallize: %s recovery is not usable evidence (target=%q tool=%q argv=%d)", ev.IncidentID, ev.Target, ev.Tool, len(ev.Argv))
	}
	return trial, true, nil
}

// rootCauseFor rebuilds the minimal contract TrialOf reads a fault kind from.
func rootCauseFor(ev loop.RecoveryEvidence) *loop.RootCauseJSON {
	if ev.FaultKind == "" {
		return nil
	}
	return &loop.RootCauseJSON{RootCauseObject: &loop.RootCauseObject{Kind: ev.FaultKind}}
}
