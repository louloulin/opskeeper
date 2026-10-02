package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/edge/autonomy"
	"github.com/vincent-wuhan/opskeeper/core/edge/cmdpolicy"
	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"
	"github.com/vincent-wuhan/opskeeper/core/floor/skill/builtin"
)

// The node's self-heal capability, assembled.
//
// It is built here rather than inside the autonomy package because this is
// the one file that knows all four of its inputs: which packages were
// admitted, what the heartbeat has to say about the control plane, what the
// collector last scraped, and which sandbox the node already runs its shell
// commands under. A component that knew all four would be the node plane in
// one package, which is the shape this composition root exists to avoid.
//
// Nothing here runs unless a package actually asked for autonomy. A node
// whose installed manifests have no autonomy block gets no registry, no
// spool, no pump and no runner, and the tool it exposes refuses. That is
// the state of every package that ships today, and it is the reason adding
// this file does not change what any existing node can do.

// autonomySpoolFile is where the node keeps the decisions it made on its
// own, under its own working directory rather than somewhere global: it
// belongs to this installation, and an installation that is deleted takes
// its evidence with it.
const autonomySpoolFile = "autonomy-audit.jsonl"

// autonomyObservations is what the stack needs from the node's agent: the
// control plane's reachability, and this host's last reading of a metric.
//
// It is an interface because the alternative is a test that has to stand up a
// tunnel and a clock to answer two questions. *edgebiz.Agent satisfies it,
// and the seam is the honest shape: these are observations, and an
// observation is something you can have recorded rather than dialled.
type autonomyObservations interface {
	// LinkReach reports whether the control plane is answering, and when it
	// stopped.
	LinkReach() (online bool, offlineSince time.Time)
	// MetricValue returns this host's most recent reading of a metric.
	MetricValue(name string) (value float64, ok bool)
}

// autonomyLink reads the control plane's reachability from the agent.
//
// The agent watches it through the heartbeat, which is the only witness that
// actually proves the manager is there: a socket that has not failed yet
// shows that the network stack accepted a write.
type autonomyLink struct{ agent autonomyObservations }

func (l autonomyLink) Reach() autonomy.Reach {
	online, since := l.agent.LinkReach()
	return autonomy.Reach{Online: online, OfflineSince: since}
}

// autonomyRunner runs a declared action through the node's own sandbox.
//
// The sandbox is the same one the bash tool uses, which is the point: an
// autonomy action is not a privileged path, it is an ordinary command that
// happens to have been signed in advance. If it needed its own executor
// with its own allow-list, the two lists would drift, and the day they did
// the signed argv would be running under a rule nobody reviewed.
type autonomyRunner struct{ sandbox *cmdpolicy.Sandbox }

func (r autonomyRunner) RunArgv(ctx context.Context, argv []string) (autonomy.Outcome, error) {
	res, err := r.sandbox.ExecArgv(ctx, argv)
	if err != nil {
		return autonomy.Outcome{ExitCode: -1}, err
	}
	if !res.Allowed {
		// The sandbox refused. That is a refusal of the *node's policy*,
		// not a failure of the action, and it must not look like a run:
		// the audit row has to say the command never started.
		return autonomy.Outcome{ExitCode: -1, Stderr: res.Reason}, nil
	}
	return autonomy.Outcome{
		ExitCode:  res.ExitCode,
		Stdout:    res.Stdout,
		Stderr:    res.Stderr,
		Truncated: res.Truncated,
	}, nil
}

// autonomyTool adapts the arbiter to the shape the host tool declares.
type autonomyTool struct {
	arbiter *autonomy.Arbiter
	runner  autonomy.Runner
}

func (t autonomyTool) PerformAutonomy(ctx context.Context, action, target, window string) (builtin.AutonomyOutcome, error) {
	claim := autonomy.Claim{
		Action: action,
		Target: target,
		Window: window,
		// The trigger is filled in from the declaration by the arbiter
		// itself when the caller names none, because the caller is not the
		// party that knows it: the manifest is, and a claim that supplied
		// its own would be checked against itself.
	}
	res, err := t.arbiter.Perform(ctx, claim, t.runner)
	if err != nil {
		return builtin.AutonomyOutcome{Verdict: autonomy.Refuse.String(), Reason: err.Error()}, nil
	}
	return builtin.AutonomyOutcome{
		Verdict:  res.Decision.Verdict.String(),
		Reason:   res.Decision.Reason,
		Ran:      res.Ran,
		ExitCode: res.Outcome.ExitCode,
		Stdout:   res.Outcome.Stdout,
		Stderr:   res.Outcome.Stderr,
	}, nil
}

// autonomyStack is what a node with autonomy installed holds.
type autonomyStack struct {
	registry *autonomy.Registry
	arbiter  *autonomy.Arbiter
	spool    *autonomy.Spool
}

// buildAutonomy assembles the stack, or returns nil when no installed
// package asked for autonomy.
//
// A nil stack is not an error and not a warning. It is the shape of every
// fleet running today's packages, and treating it as a problem would put a
// line in every node's boot log about a capability nobody asked for.
func buildAutonomy(
	ctx context.Context,
	admitted []pluginmanifest.Plugin,
	obs autonomyObservations,
	runner autonomy.Runner,
	cwd string,
	log *slog.Logger,
) (*autonomyStack, error) {
	registry, err := autonomy.NewRegistry(manifestsOf(admitted), time.Now())
	if err != nil {
		// Two packages declaring one action name is refused rather than
		// resolved, for the same reason the tool allow-list is: an action
		// name that resolves differently by install order is a name whose
		// argv nobody read.
		return nil, err
	}
	if registry.Empty() {
		return nil, nil
	}

	spool, err := autonomy.OpenSpool(filepath.Join(cwd, autonomySpoolFile), 0)
	if err != nil {
		// The stack is optional but not optional-once-declared. A package
		// that asked for autonomy on a node that cannot record what it
		// does is a node acting without a record, and the plan's phrase for
		// that is exactly what must not happen.
		return nil, fmt.Errorf("autonomy audit spool: %w", err)
	}

	arbiter, err := autonomy.New(autonomy.Options{
		Registry: registry,
		Link:     autonomyLink{agent: obs},
		Audit:    spool,
		Detector: autonomy.ValueDetector{Value: obs.MetricValue},
		Log:      log,
	})
	if err != nil {
		spool.Close()
		return nil, err
	}
	// The replay pump is NOT started here, and that is a decision rather
	// than an omission.
	//
	// The manager side has no method for it yet: there is no route to send
	// a batch of autonomy rows over, and a pump whose every drain fails
	// would spend the rest of the node's life logging that at five-second
	// intervals. The rows stay on disk, which loses nothing and is
	// visible: the health line below reports how many are waiting.
	//
	// When the route lands, this is where the pump is built and started —
	// autonomy.NewPump already exists, is rate-limited, acks all-or-nothing
	// and is tested against all four cases the plan names. The only piece
	// missing is the transport, and building a sender that cannot send is
	// how a node ends up with a replay loop that has never worked.
	log.Info("node autonomy audit spool is local only until the control plane grows a replay route",
		slog.String("path", spool.Path()))

	builtin.SetAutonomyRunner(autonomyTool{arbiter: arbiter, runner: runner})
	log.Info("node autonomy is installed",
		slog.Int("actions", len(registry.Actions())),
		slog.Duration("offline_after", registry.OfflineAfter()),
		slog.String("spool", spool.Path()))
	return &autonomyStack{registry: registry, arbiter: arbiter, spool: spool}, nil
}

// autonomyHealth is the shape a node's health page renders, so the numbers
// exist even before anything reads them.
type autonomyHealth struct {
	Actions   []string `json:"actions"`
	Deferred  uint64   `json:"deferred"`
	Run       uint64   `json:"run"`
	Refused   uint64   `json:"refused"`
	Replays   uint64   `json:"replays"`
	SpoolPath string   `json:"spool_path,omitempty"`
	Spooled   int      `json:"spooled,omitempty"`
}

func (s *autonomyStack) Health() autonomyHealth {
	if s == nil {
		return autonomyHealth{}
	}
	h := autonomyHealth{SpoolPath: s.spool.Path()}
	stats := s.arbiter.Snapshot()
	h.Deferred, h.Run, h.Refused, h.Replays = stats.Deferred, stats.Run, stats.Refused, stats.Replays
	for _, a := range s.registry.Actions() {
		h.Actions = append(h.Actions, a.Name)
	}
	if n, err := s.spool.Len(); err == nil {
		h.Spooled = n
	}
	return h
}
