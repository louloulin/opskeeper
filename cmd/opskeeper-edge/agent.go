package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/edge/agentmodel"
	"github.com/vincent-wuhan/opskeeper/core/edge/agentprofile"
	"github.com/vincent-wuhan/opskeeper/core/edge/gatesocket"
	"github.com/vincent-wuhan/opskeeper/core/edge/pigsupervisor"
	"github.com/vincent-wuhan/opskeeper/core/edge/policygate"
	"github.com/vincent-wuhan/opskeeper/core/edge/toolbroker"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigrpc"
	"github.com/vincent-wuhan/opskeeper/core/pig/pigwire"
	"github.com/vincent-wuhan/opskeeper/core/ports"
	"github.com/vincent-wuhan/opskeeper/core/wire"

	edgebiz "github.com/vincent-wuhan/opskeeper/core/edge/biz"
	"github.com/vincent-wuhan/opskeeper/core/floor/tunnel"
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
		Cwd:              envOr(agentmodel.WorkingDirEnv, agentmodel.DefaultWorkingDir),
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
// knows both the PiG adapter and the node supervisor exist. The supervisor
// is handed a client, not a PiG type, so the node module never names PiG
// and a PiG upgrade changes this file and core/pig and nothing else on the
// node plane.
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
	trust := loadTrustStore()
	// The boot bundle is reviewed against the same rule the runtime path
	// uses, including the version check: a node whose own bundle declares
	// a min_edge_version it does not meet should say so at boot — loudly,
	// once — rather than confidently serving a package it cannot host.
	policy, err := nodeBootPolicy(version)
	if err != nil {
		return nil, nil, fmt.Errorf("edge agent policy: %w", err)
	}
	admitted, err := admitPackages(cfg.Packages, trust, policy)
	if err != nil {
		return nil, nil, fmt.Errorf("edge agent packages: %w", err)
	}
	if trust.LoadError() != nil {
		// A trust store that was configured and could not be read is
		// already a hard error from admitPackages. Reaching here means
		// there is no configured store, which is worth saying once per
		// boot: this node is running packages nobody signed.
		log.Warn("node agent trust store is not configured; packages are not being signature-checked",
			slog.String("set", "OPSKEEPER_EDGE_TRUST_STORE"))
	}
	settingsPath, err := writeAgentSettings(cfg.Cwd, packageRoots(admitted))
	if err != nil {
		return nil, nil, err
	}

	// The profile, written next to the package list and for the same
	// reason: it is the other half of what this agent may do, and it is
	// written before the process starts so the two can never describe
	// different nodes. A failure here stops the node. The profile is not
	// a convenience - a node that booted without it would hand the model
	// a shell on a production host, and the gate would then refuse every
	// call to it, which is a safe but unreadable place to be during an
	// incident.
	profilePath, err := agentprofile.Write(filepath.Join(cfg.Cwd, agentConfigDirName()))
	if err != nil {
		return nil, nil, fmt.Errorf("edge agent profile: %w", err)
	}
	log.Info("node agent package set installed",
		slog.Int("packages", len(admitted)),
		slog.String("settings", settingsPath),
		slog.String("profile", profilePath))

	// The model endpoint, resolved before anything is started.
	//
	// This is a separate step from the package set above and it fails
	// differently on purpose. A package that does not review is a boot
	// error. A model endpoint that is half-configured is also a boot error,
	// for a reason that has nothing to do with review: without it the node
	// starts, loads its plugins, authenticates to the tunnel and then
	// answers every question with no model behind it. The symptom is a node
	// that reports healthy, and the cause is visible in exactly one place —
	// a boot log nobody reads after the rollout is finished.
	//
	// A node with no endpoint at all is not an error. That is a deployment
	// that has not been given a model yet, and the agent's own configuration
	// scope is left entirely alone so an operator who provisioned one by
	// hand keeps it. See agentmodel.go for why the default scope is not
	// something to rely on.
	modelCfg, modelConfigured, err := agentmodel.ConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("edge agent model configuration: %w", err)
	}
	// socketPath is read by the factory when it spawns the agent, which is
	// after the socket exists. A closure over it rather than a value,
	// because the socket cannot be created until the gate is.
	var socketPath string

	// toolSocketPath is read the same late way: the broker cannot exist
	// until the invoker and the authoriser do, and those cannot be built
	// until the registry is.
	var toolSocketPath string

	// The agent's whole environment, assembled here and completed below,
	// then read by the factory when it spawns.
	//
	// A Go map holds copies, not references, so the two socket entries are
	// written again where the paths are actually assigned rather than being
	// captured now with their empty values. That redundancy is deliberate
	// and it is the whole reason the factory takes a map instead of being
	// handed each value: "what the agent process is told" is then one
	// object, and every key in it was written by code that had the value in
	// hand. An agent told an empty gate socket path fails to load its
	// plugins, and it does so by refusing every tool call — a node that
	// looks configured and answers nothing.
	agentEnv := map[string]string{
		wire.GateSocketEnv: socketPath,
		wire.ToolSocketEnv: toolSocketPath,
	}
	if modelConfigured {
		modelsPath, err := agentmodel.Write(modelCfg)
		if err != nil {
			return nil, nil, err
		}
		for key, value := range modelCfg.AgentEnvVars() {
			agentEnv[key] = value
		}
		// The endpoint and the model are safe to log; the token is not, and
		// it is not in models.json either — see the agentmodel package.
		log.Info("node agent model endpoint installed",
			slog.String("models", modelsPath),
			slog.String("provider", agentmodel.ProviderID),
			slog.String("base_url", modelCfg.BaseURL),
			slog.String("model", modelCfg.Model))
	} else {
		log.Info("node agent has no model endpoint configured; the agent will use whatever "+
			"provider its own configuration scope resolves",
			slog.String("set", agentmodel.BaseURLEnv))
	}

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

	// The gate, the socket, the bridge and the supervisor reference each
	// other, and the cycle is broken in one place rather than spread across
	// the file: the supervisor is built first with a factory that reads the
	// socket path at the moment it spawns, the bridge is built next, then
	// the gate, then the socket, and only then is the supervisor started.
	// Every reference below is to something already constructed, and the
	// one forward reference is a variable the factory reads late.

	// --mode rpc is the headless protocol this node speaks. --piglet points
	// at the profile written above, and it is not optional: without it the
	// agent starts with PiG's stock built-ins, which include a shell, and
	// the model on a production node would be offered it. The order is the
	// agent's own; both flags are read before the session starts, so
	// neither has to precede the other.
	args := []string{"--mode", "rpc", "--piglet", profilePath}
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
			// Two sockets, and they are the only things the agent is told:
			// not the role, not the session, not what is permitted. An
			// agent handed those would be handed the ability to assert
			// them, and an assertion is not a lookup.
			//
			// The gate socket is how a tool call inside the agent reaches
			// the host that is allowed to say no. The tool socket is how
			// the call, once permitted, reaches the host that actually
			// performs it — because the agent process holds no
			// implementation of any of them.
			//
			// The model endpoint and its credential are in here too, and
			// for the same reason: the agent is told where to send a
			// request, never how to answer one. It holds no provider key
			// of its own and no way to reach one that was not named here.
			Env: agentEnv,
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

	// The gate is the node's only path to a tool call. It relays approval
	// frames through the bridge and the bridge applies the operator's
	// answers back to it, so the bridge is built first and given the gate
	// afterwards.
	//
	// The base policy is read-only and every real decision goes through
	// ByActor, so the only way a mutating call runs is by a role the
	// manager authenticated and the node resolved. A caller the node
	// cannot place in that ladder gets the base policy, which is the
	// bottom of it.
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

	// The socket is the enforcement point. It is created before the agent
	// is started so there is no window in which a running agent has no way
	// to ask, and it is the last thing torn down so a tool call in flight
	// during shutdown still gets an answer.
	socket, err := gatesocket.Listen(gatesocket.Options{
		Admit: gate,
		Actor: bridge.ActorFor,
		Log:   log,
	})
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("edge agent gate socket: %w", err)
	}
	socketPath = socket.Path()
	agentEnv[wire.GateSocketEnv] = socketPath
	priorStop := stop
	stop = func() {
		if err := socket.Close(); err != nil {
			log.Warn("edge agent gate socket did not close cleanly", slog.Any("err", err))
		}
		priorStop()
	}

	// The broker is the other half of the same arrangement, and it is
	// built after the gate because it authorises against the same registry
	// with the same role ladder. It re-checks rather than trusting the
	// gate's answer because the gate is reached through an extension in
	// the agent process: a package that replaced that extension would
	// silence the check, and this is the one it cannot silence.
	broker, err := toolbroker.Listen(toolbroker.Options{
		// The gate is passed in so the broker can demand the receipt for
		// any call that needed a human. Without it a package that replaced
		// the courier would find its mutating tools running unapproved.
		Authorize: toolAuthorizer(registry, gate),
		Invoke:    &agentToolInvoker{client: client, log: log},
		Actor:     bridge.ActorFor,
		Log:       log,
	})
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("edge agent tool broker: %w", err)
	}
	toolSocketPath = broker.Path()
	agentEnv[wire.ToolSocketEnv] = toolSocketPath
	priorStop = stop
	stop = func() {
		if err := broker.Close(); err != nil {
			log.Warn("edge agent tool broker did not close cleanly", slog.Any("err", err))
		}
		priorStop()
	}

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

	// The console learns that the agent restarted through agent.state and
	// agent.health, not through a resumed sequence. Carrying the old
	// counters across the swap would make a fresh process answer a
	// conversation the operator believes is further along than it is.
	sup.OnRestart(translators.ForgetAll)
	return bridge, stop, nil
}
