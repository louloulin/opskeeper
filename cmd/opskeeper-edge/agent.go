package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/edge/pigsupervisor"
	"github.com/vincent-wuhan/opskeeper/core/edge/policygate"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigrpc"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigwire"
	"github.com/vincent-wuhan/opskeeper/core/ports"

	edgebiz "github.com/vincent-wuhan/opskeeper/internal/edgeagent/biz"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/tunnel"
)

// nodeAgentConfig is how a node is told which agent to run and what it may
// load.
//
// Everything is env-driven because a node is provisioned by install.sh on
// hosts OpsKeeper has never seen, and the manager reaches them only through
// the tunnel. The defaults point at a read-only profile in a plugin bundle
// the operator has to place deliberately: an agent that finds no bundle
// starts with no tools at all rather than with whatever happens to be in the
// working directory.
type nodeAgentConfig struct {
	// Binary is the agent executable. Empty uses "pig" off PATH.
	Binary string
	// Cwd is the agent's working directory and, because the agent
	// discovers its extensions and skills relative to where it was
	// launched, the package root of the plugin bundle it is allowed to
	// load. This is the single most security-relevant setting on the
	// node: it decides which code runs with the node's privileges.
	Cwd string
	// Packages are the plugin bundles the agent may load, as absolute
	// directories. Each is admitted against its governance manifest before
	// any of them is written into the agent's settings; one that fails is
	// a boot error, not a warning.
	Packages []string
	// Provider and Model pin the agent's model. Empty lets the manager
	// pin per conversation.
	Provider string
	Model    string
	// MaxCrashAttempts bounds crash-looping. Zero restarts forever, which
	// is right for a node whose agent is known-good and wrong for one
	// that is simply broken.
	MaxCrashAttempts int
	// RestartBackoff is the first restart delay.
	RestartBackoff time.Duration
}

// defaultAgentPackageDir is the read-only profile every node starts with.
//
// It is a default, not a constant that always applies: an operator who
// points OPSKEEPER_EDGE_AGENT_PACKAGES somewhere else gets exactly what
// they asked for. What is not negotiable is that nothing outside the
// configured list is ever loaded.
const defaultAgentPackageDir = "/var/lib/opskeeper-edge/agent/packages"

// loadNodeAgentConfig reads the node agent's settings from the environment.
func loadNodeAgentConfig() nodeAgentConfig {
	packages := splitList(os.Getenv("OPSKEEPER_EDGE_AGENT_PACKAGES"))
	if len(packages) == 0 {
		packages = []string{defaultAgentPackageDir}
	}
	return nodeAgentConfig{
		Binary:           envOr("OPSKEEPER_EDGE_AGENT_BIN", "pig"),
		Cwd:              envOr("OPSKEEPER_EDGE_AGENT_DIR", "/var/lib/opskeeper-edge/agent"),
		Packages:         packages,
		Provider:         os.Getenv("OPSKEEPER_EDGE_AGENT_PROVIDER"),
		Model:            os.Getenv("OPSKEEPER_EDGE_AGENT_MODEL"),
		MaxCrashAttempts: 5,
		RestartBackoff:   pigsupervisor.DefaultRestartBackoff,
	}
}

// startNodeAgent brings up this node's PiG agent and returns the bridge that
// serves the manager's agent.* commands against it.
//
// It is a composition point, not a layer: this file is the one place that
// knows both the PiG adapter and the node supervisor exist. Neither
// core/edge nor internal/edgeagent may import the other, so a PiG upgrade
// changes this file and core/pig and nothing else on the node plane.
//
// A node agent that fails to start is not fatal. The edge still collects
// metrics, still serves its own skill RPCs, and still answers agent.state
// and agent.health with an explanation. An edge that refuses to boot
// because the AI is down would take the telemetry with it.
func startNodeAgent(ctx context.Context, client tunnel.Client, cfg nodeAgentConfig, version string, log *slog.Logger) (bridge *edgebiz.AgentBridge, stop func(), err error) {
	// Admit the packages before anything starts. A package that fails
	// validation is a boot error the operator has to fix, not a warning
	// that scrolls past: starting the agent without it would produce a
	// node that answers confidently and cannot see half the host.
	admitted, err := admitPackages(cfg.Packages)
	if err != nil {
		return nil, nil, fmt.Errorf("edge agent packages: %w", err)
	}
	settingsPath, err := writeAgentSettings(cfg.Cwd, packageRoots(admitted))
	if err != nil {
		return nil, nil, err
	}
	log.Info("node agent package set installed",
		slog.Int("packages", len(admitted)),
		slog.String("settings", settingsPath))

	// The allow-list is built from the same manifests that were just
	// admitted, and is built before the agent starts. Nothing reaches the
	// gate later than this: a tool the host did not bind at boot is
	// refused on every turn, so a package that ships an undeclared tool
	// cannot become reachable by being clever about it at run time.
	registry, err := policygate.RegistryFromManifests(manifestsOf(admitted))
	if err != nil {
		// Two packages claiming one tool is a packaging problem the
		// operator has to resolve, and resolving it by picking a winner
		// would make the effective permission of a tool depend on install
		// order. Refusing the node is the honest outcome.
		return nil, nil, fmt.Errorf("edge agent tool allow-list: %w", err)
	}
	log.Info("node agent tool allow-list built",
		slog.Int("tools", registry.Len()),
		slog.Any("names", registry.Names()))

	args := []string{"--mode", "rpc"}
	// Every process is a new one: a restart is a new agent, not a resume.
	// The agent holds no transcript across its own death, and pretending
	// otherwise would drop the turn's output without saying so.
	factory := func() ports.AgentProcess {
		return pigrpc.New(pigrpc.Options{
			Binary:   cfg.Binary,
			Cwd:      cfg.Cwd,
			Args:     args,
			Provider: cfg.Provider,
			Model:    cfg.Model,
			Version:  version,
		})
	}
	sup, err := pigsupervisor.New(pigsupervisor.Config{
		Factory:          factory,
		Log:              log,
		MaxCrashAttempts: cfg.MaxCrashAttempts,
		RestartBackoff:   cfg.RestartBackoff,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("edge agent supervisor: %w", err)
	}
	// The supervisor's own loop ends with ctx, but the agent process it
	// supervises is a child of this binary and does not. Without an
	// explicit stop, an edge shutting down for an upgrade would leave a
	// `pig` holding the node's credentials with nobody to supervise it.
	stop = func() {
		if err := sup.Stop(); err != nil {
			log.Warn("edge agent did not stop cleanly", slog.Any("err", err))
		}
	}

	// One translator per conversation, held by the node. The agent
	// multiplexes conversations over a single process, and a shared
	// counter would interleave two operators' turns into one sequence.
	translators := pigwire.NewSet(nil, 0)

	if err := sup.Start(ctx); err != nil {
		// Report it and keep the supervisor. It is already in the
		// crash-loop policy, so a binary that is missing now may be
		// present after the next package push, and a node that gave up on
		// its agent at boot would need the edge restarted to pick it up.
		log.Error("edge agent did not start; the node will keep retrying under the crash policy",
			slog.String("binary", cfg.Binary),
			slog.String("dir", cfg.Cwd),
			slog.Any("err", err))
	}

	bridge, err = edgebiz.NewAgentBridge(edgebiz.AgentBridgeOptions{
		Source:    sup,
		Client:    client,
		Log:       log,
		Translate: translators.Translate,
	})
	if err != nil {
		stop()
		return nil, nil, err
	}

	// The gate is the node's only path to a tool call, and the two objects
	// reference each other: the gate relays approval frames through the
	// bridge, and the bridge applies the operator's answers to the gate.
	// The bridge is built first and given the gate afterwards so the
	// reference is explicit rather than a closure over a variable that is
	// written later.
	gate, err := policygate.New(policygate.Options{
		Policy:  registry.Policy(roleCeiling("")),
		ByActor: func(actor string) policygate.Policy { return registry.Policy(roleCeiling(actor)) },
		Emit:    bridge.EmitApproval,
	})
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("edge agent approval gate: %w", err)
	}
	bridge.SetDecider(gate)
	// The console learns that the agent restarted through agent.state and
	// agent.health, not through a resumed sequence. Carrying the old
	// counters across the swap would make a fresh process answer a
	// conversation the operator believes is further along than it is.
	sup.OnRestart(translators.ForgetAll)
	return bridge, stop, nil
}
