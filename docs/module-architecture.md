# OpsKeeper 2.0 module architecture

Status: **Phases A, B and C landed. Phase D's B1 batch (read-only toolset) landed.**
Phase C has started: the node agent's process contract and its supervisor
exist and are tested; the tunnel methods and the control-plane fleet are not
written yet. `internal/pkg/llm` still runs on eino — see "What the plan got
wrong about eino" below.

This document describes the module graph, why it is shaped this way, and the
rules CI enforces. It is the reference for anyone adding a module or a
plugin.

## The problem this shape solves

OpsKeeper is migrating its AI runtime from `cloudwego/eino` to
[PiG](https://github.com/MichaelKinsy/PiG), a Go port of the Pi coding agent.

Two facts about PiG drive the whole design:

1. **PiG is pre-stable 0.x.** Its API will move. So does its dependency
   graph, which currently pulls the AWS SDK, Google API client, and a dozen
   provider libraries.
2. **OpsKeeper ships plugins.** A plugin author must be able to compile
   against OpsKeeper without inheriting either.

Together these mean one rule: *PiG must be reachable from exactly one place
in the repository.* If a plugin can import PiG, a PiG upgrade breaks every
plugin, and the plugin ecosystem dies on first contact with upstream churn.

## The graph

```
                          core
              (contracts, no dependencies)
                 ^          ^          ^
                 |          |          |
               pig       manager     edge        harness
          (PiG adapter)  (control)   (node)     (evaluation)
                 ^          ^          ^
                 |          |          |
                 +----------+----------+
                            |
                           sdk
                 (third-party plugin surface)
```

The arrows are the only permitted dependency directions. Every module except
`core` may import `core`. Only `pig` may import PiG. Only `cmd` may import
`pig`.

| Module | Path | Responsibility | May import |
|---|---|---|---|
| `core` | `core/` | Domain vocabulary, port interfaces, wire DTOs | stdlib only |
| `pig` | `core/pig/` | The PiG adapter | `core`, PiG |
| `manager` | (split pending) | Control plane | `core`, `pig`, `sdk` |
| `edge` | (split pending) | Node plane | `core`, `pig` |
| `harness` | (split pending) | Evaluation | `core` |
| `sdk` | `sdk/` | Third-party plugin surface | `core`, `yaml.v3` |

`manager`, `edge`, and `harness` are still the pre-split packages inside the
root module. Splitting them is mechanical work that has not been done yet;
the boundaries above are already enforced for the three new modules, and
`.go-arch-lint.yml` carries the intra-module rules for the rest.

## What lives in core

Three packages, and the split is deliberate:

- **`core/domain`** — the vocabulary: `ToolClass`, `SafetyLevel`,
  `BlastRadius`, `ProviderID`, `Scopes`, `PluginManifest`. This is what a
  plugin author reads.
- **`core/ports`** — the interfaces. `Chat`, `Agent`, `Tool`, `AuditSink`,
  `ApprovalGate`. PiG types stop here; nothing above this line sees them.
- **`core/wire`** — the transport DTOs. `wire.StreamEvent` is the SSE frame
  the web console already parses, so an adapter translating a PiG event into
  a console frame is a field copy, not a translation.

`core` has **zero external dependencies** — verified in CI. A contract that
reaches into infrastructure forces every plugin to resolve that
infrastructure.

## Fail-closed rules

The safety vocabulary is written so that a mistake reduces privilege rather
than granting it. Each of these is covered by a test that asserts the
failure mode, not just the happy path:

| Rule | Why |
|---|---|
| `ToolClass("")` ranks as destructive | An unclassified tool must not be trusted to be read-only. |
| An unrecognised `SafetyLevel` ranks as L3 | A typo in a manifest must not lower a plugin's privileges. |
| An unrecognised `BlastRadius` outranks `cluster` | `"cluser"` must not pass as `cluster`. |
| `Classify()` with no arguments returns `unknown` | An empty capability declaration is not a read-only grant. |
| `spec.audit.mutates: true` is a load error | Audit-ledger writes are host-only and are not delegable. |
| A capability above the declared level is refused, not clamped | A clamped manifest is ambiguous; a refused one is a lie. |
| A read-only plugin may not carry a blast radius | A mutation scope in the record of a package that cannot mutate. |
| A zero-value `Admission` refuses everything | A host that forgot to populate its policy must deny, not allow. |
| `Admit` checks `required.Satisfies(granted)` | The reverse check would admit a plugin whose scopes were never granted. |

## Enforcement

Two checks, because they catch different things:

- **`.go-arch-lint.yml`** (`make arch-lint`) — the intra-module BC rules that
  predate 2.0, plus the new module direction.
- **`scripts/modulecheck`** (`make module-check`) — the rule neither the Go
  toolchain nor arch-lint can express: *only `core/pig` may import
  `github.com/MichaelKinsy/PiG`*. It walks every module's import graph and
  is itself unit-tested against deliberately broken fixtures, because a
  checker that cannot fail is worse than no checker.

`make module-test` builds and tests all three new modules.

## Plugin governance

A plugin ships two manifests side by side:

- The **Pi package manifest**, which stays Pi-compatible and is what the
  agent runtime actually mounts.
- **`pig-ops.yaml`**, which adds only the governance fields Pi has no
  vocabulary for: where the plugin may run, how dangerous it is, and what
  it needs.

```yaml
apiVersion: opskeeper.io/v1
kind: Plugin
metadata: {name: opskeeper-sre-readonly, version: 0.1.0}
spec:
  targets: [edge]              # where it may run
  safety_level: L1             # L0 read-only … L3 external mutation
  capabilities: [read]         # declared ceiling, not a wish list
  required_scopes: [host.read] # the only credentials the host will inject
  audit: {emits: true, mutates: false}
  approval: {required: false}
  install: {strategy: rolling, min_edge_version: 0.7.0}
```

The host reads it through `internal/pkg/pluginmanifest`, which wraps the
`sdk` module. Every shipped plugin under `plugins/pig-ops/` is validated by
`TestShippedPluginsAreValid`, so a bad manifest fails the build rather than a
production install.

### The three host-enforced invariants

No plugin can widen its own authority, regardless of what its manifest says:

1. **Credentials** are injected per `required_scopes`. A plugin cannot reach
   a credential it did not declare.
2. **Audit** entries are derived by the host from its own gate events. A
   plugin can neither forge nor suppress a record, and has no write path.
3. **Approval** is the host's alone. A plugin can *request*; only a
   `DecisionProvider` in the control plane can *grant*, and a grant is bound
   to a digest of the exact proposed call. `blast_radius` is assessed by the
   host, not declared by the plugin.

## Where the agent runtime plugs in

`core/ports.Agent` is the seam. Its implementation is the kernel in
`core/pig/pigagent`, which drives PiG's `agent.Agent`. A host injects the
host services through `AgentDeps` — tools, audit sink, approval gate, model,
budget — so the same kernel serves the control plane, a background
investigator, and a per-node `pig` process with different policies and no
code changes.

Landed so far: `pigmodel` (settings → PiG providers and models, with
per-request credential injection so an admin edit lands on the next call
without a restart), and `pigagent` in full — the tool adapter, the SSE event
mapper, the run state that enforces host policy, and the kernel that drives
PiG's `agent.Agent`. Removing eino and reimplementing `internal/pkg/llm` on
`pigmodel` is the rest of this phase.

### Four defects the test suite found

The kernel's tests drive a real PiG agent loop through PiG's faux provider,
so the hook ordering they depend on is PiG's own. Writing them surfaced four
defects that reading the code had not:

1. **The stream function bypassed the resolved provider.** `ai.StreamSimple`
   does not use `model.Provider`; it rebuilds a provider from
   `ProviderMeta` through a fixed switch over OpenAI, Anthropic, Google,
   Bedrock and Mistral. Any other provider — including the self-hosted
   gateway that is the normal ops deployment — silently produced an empty
   stream, and the per-request credential closure attached by the registry
   was discarded along the way. The stream function now calls the bound
   provider directly, which is also what Pi does with a nil `StreamFn`.

2. **A policy refusal was reported as a tool failure.** PiG settles a
   blocked call as an "immediate" outcome: it never runs, so it never
   reaches `AfterToolCall`. The kernel therefore reported a refusal as
   `tool_error` and audited it as `tool_failed`, and — worse — wrote **no
   ledger row at all** for the one event an incident review most needs.
   `runState` now records refusals as they are made and settles them when
   the end event arrives, which is the only point where every terminal call
   is visible.

3. **An absent tool bag was a nil dereference.** A host running a turn with
   no tools — a summariser, a classifier, a worker whose profile grants
   nothing — crashed the kernel instead of running a tool-free turn.

4. **The `done` frame's usage was always zero.** The mapper owned the
   accumulator but nothing ever fed it, so the console had to sum assistant
   frames for a cost after all. `foldUsage` now publishes the running total
   into the mapper as the turn proceeds.

A fifth was cosmetic: `ErrAlreadyRunic`.

The refusal path also turned out to speak two vocabularies. An explicit
operator denial reached the console as `deny` and every other refusal as
`denied`, so a console branching on the field would have needed three
spellings. `ApprovalFrame.Decision` is now the closed `grant`/`deny` pair and
the cause travels in `Note`.

## The node agent: a process, not a library

A node's AI agent is a separate `pig --mode rpc` process, not a goroutine in
the edge agent. That separation is the design, not an implementation detail.
The agent and the plugins it loads are the highest-privilege, highest-churn
code on a host: a plugin can segfault, leak, or wedge, and a shell tool it
exposes can do damage with nobody watching. Confining that to one process
means a bad plugin upgrade takes down a process that restarts in seconds,
rather than the telemetry pipeline, the tunnel, and the upgrade path an
operator would need in order to roll it back.

Three pieces, in dependency order:

| Layer | Where | What it owns |
|---|---|---|
| `ports.AgentProcess` | `core/ports/agentprocess.go` | the contract: start/stop, prompt/steer/abort, state, events |
| `pigrpc.Client` | `core/pig/pigrpc/` | the only place that knows a node agent is a `pig --mode rpc` subprocess |
| `pigsupervisor.Supervisor` | `core/edge/pigsupervisor/` | spawn, restart, backoff, crash-loop protection, health |

The port exists so `edge` never imports PiG. That is the same rule that made
`core/pig` the sole PiG importer, applied one level down: a PiG upgrade has
to land in an adapter, not in a file that a node binary compiles.

### Two states the supervisor must not confuse

The supervisor's second job is knowing the difference between an agent that
is healthy, one that is down, and one that is failing too fast to be worth
restarting. Those are three different pages. Collapsing them into "up or
down" is how a node ends up in a respawn loop that looks healthy in a
dashboard while burning a core and flooding a log.

So the supervisor **gives up**, and does so in a specific way. After
`MaxCrashAttempts` crashes inside `CrashWindow` it stops respawning and
reports `Degraded` — and it *stays up and keeps answering health*. A
supervisor that exits takes its health endpoint with it, and a node that has
vanished from the fleet is a worse outcome than one that is visibly broken
and awaiting a human.

A manual `Restart` deliberately does **not** reset that budget. The escape
hatch must not become the loop: a node an operator is hand-restarting every
minute should still trip the limit.

### What the supervisor's tests found

Writing the lifecycle tests surfaced four defects that reading the code had
not. All four are the same shape: state the operator relies on was published
one step out of order, or not at all.

- **`Stop()` on a supervisor that was never started blocked forever.** The
  shutdown path that stops a supervisor it failed to start — which is
  exactly the path taken when the agent binary is missing — hung instead of
  returning. The done channel now starts closed, and `Stop` reads it under
  the lock that `Start` writes it under.
- **`Start` after `Stop` reported the wrong error.** It said "already
  started" for a supervisor that no longer existed, sending an operator
  looking for a second Start call that never happened. `stopped` is now
  checked first.
- **A subscription did not survive a restart, despite the doc comment
  saying it did.** `OnEvent` bound the listener to the current process and
  returned; the next crash left the console attached to a process that no
  longer existed, with no error anywhere. Subscriptions are now held by the
  supervisor and rebound on every launch. The same fix exposed a second
  hole — a listener registered before the first `Start` and cancelled
  before it was bound stayed bound — which is why each subscription carries
  its own process binding rather than a single shared one.
- **`degraded` was published before the reason for it.** A fleet poll
  landing in that window saw a crash-looping node with nothing to say why,
  which is the one state an operator cannot act on. Both are now written in
  the same critical section.

`Process()` had the same problem in the other direction: it answered from
the `running` flag, which the restart loop clears only once it *notices* the
exit, so in the window between a process dying and the loop reacting it
handed out a handle that failed every command. It now consults the process's
own exit channel, which does not have that window.

## The tunnel: what crosses, and in what vocabulary

A node's agent and the console have never met, and the tunnel is where that
stops being true. Seven methods, split by direction:

| Method | Direction | Carries |
|---|---|---|
| `agent.prompt` | manager → node | one turn |
| `agent.steer` | manager → node | a message for the turn already running |
| `agent.abort` | manager → node | stop this turn, keep the conversation |
| `agent.state` | manager → node | what the agent is doing |
| `agent.set_model` | manager → node | a model pin, applied before the first turn |
| `agent.health` | manager → node | what the supervisor sees |
| `agent.event` | node → manager | one stream frame |

**Events are translated on the node, not in the control plane.** The node
receives `turn_start` / `message_update` / `tool_execution_end` and emits
`assistant_start` / `assistant_delta` / `tool_end` — the frames the console
has always parsed — before the frame crosses the tunnel. That is what keeps
"前端零改动" true, and it is why the knowledge lives in `core/pig/pigwire`:
when PiG renames something, one file in one module changes.

A frame that cannot be placed still crosses, with a nil console frame,
rather than being dropped at the edge. A gap the console can see and count
is recoverable; a turn that stops mid-sentence with no marker is not.

Because one node's agent multiplexes several conversations over one event
stream, the translation state is per conversation: `pigwire.Set` holds one
translator per session id, and idle conversations are reaped. A shared
counter would interleave two operators' turns into one sequence and make the
console's gap detection fire on nearly every frame.

### The two halves that meet at the tunnel

| Side | Type | Role |
|---|---|---|
| node | `edgebiz.AgentBridge` (`internal/edgeagent/biz/agent_rpc.go`) | serves `agent.*`, relays frames |
| manager | `nodefleet.Fleet` (`internal/manager/biz/nodefleet/`) | routes a frame to the conversation that named it |
| manager | `nodeagent.Service` (`internal/manager/biz/nodeagent/`) | binds a console's SSE stream to a conversation |

The fleet holds a `ports.AgentProcess` per conversation and cannot tell it
is four network hops away — the same interface the node's own supervisor
implements. That is deliberate: the routing, the session bookkeeping, and the
fan-out are written once against a contract, not twice against two
transports.

The service above it owns the one thing the fleet deliberately does not: the
join from a conversation id the console was handed to the stream it is
watching on. It is also where the two console-facing rules live:

- **A turn is refused when no console is watching.** Sending blind is how
  "the button did nothing" reports start: the agent works for minutes and
  the operator has no way to see any of it.
- **A stream ends on a finished turn, after a grace period.** The terminal
  frame and the tool frames it follows cross the tunnel out of order, so
  closing on the terminal frame alone truncates a turn in the middle of its
  own summary. A follow-up sent inside the grace window revives the stream,
  because the operator saw a finished turn and immediately asked another
  question.

### Refusal is not transport failure

A node that answers "this node runs no agent" has answered. A `dial.Call`
error means the two are not talking. The console renders them differently
and an operator debugging a fleet acts on them differently, so
`nodefleet.RemoteError` is a distinct type: `Retryable() == false`, with the
node's own code and message carried verbatim. "agent crashed 5 times in
10m0s: plugin manifest not found" is a page somebody can act on;
"agent unavailable" is a state to be stared at.

## The node agent's package set

The agent discovers its plugins from the project config in its working
directory, not from a command-line flag. The node therefore writes that
file itself, after admitting every configured package against its
`pig-ops.yaml` through the `sdk` module's admission rules.

- **Admission is a gate, and it is all-or-nothing.** A package that fails
  validation means the agent does not start, rather than starting with the
  subset that happened to parse. A node with a silently reduced tool set
  answers questions it can no longer see the answer to.
- **A package that does not target the edge is refused**, even though it
  validates. Its manifest was reviewed for the control plane; running it on
  a host anyway would read as reviewed where nobody looked.
- **The settings file is replaced, never merged.** A stale entry would keep
  a removed plugin loaded for ever, and a hand-added key could point the
  agent at something nobody reviewed. It is written to a temporary name and
  renamed, so a node killed mid-write leaves either the old set or the new
  one — never a truncated file that starts an agent with no plugins and no
  explanation.
- **It is `0640`.** It names the code that runs with the node's privileges.

The node derives the config directory name (`.pi` or `.pig`) from the same
environment variable the agent uses, because two sides disagreeing about
where config lives produces a node that boots with no plugins while its own
settings say otherwise.

## What the plan got wrong about eino

The plan states that removing eino leaves `loop`, `judge`, `chat_to_query`
and `alertdraft` with "zero caller changes". That is not true of this
codebase, and the difference is worth recording rather than discovering
mid-rewrite:

- **34 files, ~20K lines** import eino or go-openai.
- eino is not a swappable client at the edge. It runs *through* the chat
  runtime: `schema` (21 files), `components/model` (19), `callbacks` (15),
  `components/tool` (11), `compose` (6), and `flow/agent/react` (1).

So the removal is a rewrite of the ReAct graph and its callback chain, not a
provider swap. The `ports.Chat` interface is in place and the kernel behind
it is tested, which is the part that makes the rewrite tractable — but it is
a phase-scale change, and it is not a prerequisite for anything in Phase C.
The node agent does not use the manager's chat runtime, so the two can land
in either order.

## Development

`go.work` covers the root module plus `core`, `core/pig`, `core/edge`, and
`sdk`, and
pins PiG to a local checkout. Each `go.mod` also carries a relative `replace`
so a bare module directory still builds in CI jobs that disable workspaces.
Releases replace those with tagged versions.

## Known issue

`internal/skill/builtin` has three failing tests
(`TestTruncateOrSpill_OverLimit`, `_FileSuffix`, `_FallsBackToTempDir`).
This is **pre-existing** — it reproduces on a clean checkout without any
2.0 change — and is tracked separately. It is not caused by the Go 1.26
bump.


## The node plane's two sockets

`core/edge` holds three node-plane components, and the boundary between them
is the reason the plugin ecosystem is safe rather than merely tidy:

| Component | Path | Question it answers | Reached from |
|---|---|---|---|
| `policygate` | `core/edge/policygate/` | May this call run? | the gate socket |
| `gatesocket` | `core/edge/gatesocket/` | Carries that question to the gate | the courier extension |
| `toolbroker` | `core/edge/toolbroker/` | Run it | the toolset extension |

The gate is reached through an extension inside the agent process, so a
package that replaced the courier would silence it. The broker is host code,
reached only by tool name, and it re-checks the same registry before it
dispatches. Two checks reading one registry is the whole of the defence in
depth here: a tool has to survive a check the agent could have suppressed.

`cmd/opskeeper-edge` is the only place that knows all three exist, which is
why `internal/edgeagent` may not import `core/edge` and vice versa. The same
rule keeps a PiG upgrade confined to `core/pig` and the composition root.

## Where a tool's implementation lives

Not in the plugin. The read-only SRE toolset
(`core/pig/extensions/opskeeper-sre-readonly/`) declares eighteen tools and
implements none of them:

- The thirteen `host_*` probes are the node's own, and `internal/skill/builtin`
  already implements them with permission classes, spill handling and tests.
- The five control-plane queries read the manager's graph and rule table. A
  subprocess on the node has no legitimate path to either, so the node asks
  over `tunnel.MethodAgentTool` and the manager answers on the same
  authenticated edge session.

So the agent process holds no operational code. That is what makes "isolated
subprocess" a containment boundary rather than a naming convention, and it
is why adding a read-only capability is a manifest line plus a skill, not a
new extension.
