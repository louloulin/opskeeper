# OpsKeeper PiG plugins (`plugins/pig-ops/`)

Each subdirectory is one plugin package. A plugin ships **two manifests**
side by side, and both are read on install:

| File | Owner | Purpose |
|---|---|---|
| the Pi package manifest | Pi / PiG | What the agent runtime actually mounts — extensions, skills, prompts, agents, MCP servers, hooks |
| `pig-ops.yaml` | OpsKeeper | Governance Pi has no vocabulary for: where it may run, how dangerous it is, what it needs |

Keeping the package manifest Pi-compatible is deliberate. OpsKeeper does not
invent a plugin format; it adopts Pi's, so a plugin written against
[pi.dev](https://pi.dev) runs here, and extensions authored in Go, Rust,
Python, or TypeScript all work. `pig-ops.yaml` sits alongside as a policy
sidecar rather than a fork of the package manifest.

## Layout

```
opskeeper-sre-readonly/
├── pig-ops.yaml              # governance sidecar (required)
├── skills/
│   ├── diagnose-readonly/SKILL.md
│   └── opskeeper-*/SKILL.md   # the seven worker personas
├── extensions/
│   └── opskeeper-gate/        # the gate courier
├── agents/                   # optional; persona definitions
├── prompts/                  # optional
└── mcp/                      # optional; MCP server definitions
```

`extensions/`, `agents/`, `prompts/`, and `mcp/` are all optional. A
plugin that ships only skills is valid.

## The governance sidecar

```yaml
apiVersion: opskeeper.io/v1
kind: Plugin
metadata: {name: ..., version: ..., vendor: ..., homepage: ...}
spec:
  targets: [edge, manager]     # where it may run
  safety_level: L0|L1|L2|L3    # L0 read-only … L3 external mutation
  capabilities: [read, write, destructive]   # the package's ceiling
  tools:                      # the host's allow-list, built from this
    - {name: host_lsof, class: read}
  required_scopes: [host.read] # the only credentials the host will inject
  audit: {emits: true, mutates: false}
  approval: {required: true, max_blast_radius: pod|single-ns|namespace|cluster}
  install: {strategy: rolling|pin, min_edge_version: X}
```

### Safety levels

| Level | Meaning | Approval |
|---|---|---|
| `L0` | read-only observation | none |
| `L1` | read-only plus local evidence collection | none |
| `L2` | mutates OpsKeeper-managed state | single approval |
| `L3` | mutates external systems | scoped approval with a blast radius |

### `spec.tools` is the allow-list

This is the field that makes enforcement possible at all. The agent
discovers a package's tools at run time, from the package itself — so a
manifest that did not list them would leave the host deciding permission
*after* the code was already running.

Instead the list is declared, validated, and turned into the node's
allow-list at boot. The rules:

- A tool's class may not exceed `spec.capabilities`, which may not exceed
  `safety_level`. A tool above the review's ceiling is a **load error**.
- Two packages claiming one tool name is a **node boot error**. Taking
  whichever loaded last would make a tool's effective permission depend on
  install order.
- A tool the agent produces that is **not on this list is refused** on
  every turn. A package cannot become more capable by shipping an undeclared
  tool.

The list is therefore the review surface: a tool added here is a tool
somebody agreed this package may run. That is why `opskeeper-sre-readonly`
keeps it sorted — a review is a diff between versions more often than it is
a reading of one file.

### What the host will not let a plugin do

No manifest field widens a plugin's authority, because three things are
enforced outside the plugin entirely:

1. **Credentials** are injected per `required_scopes`. A plugin cannot
   reach a credential it did not declare.
2. **Audit** entries are derived by the host from its own gate events. A
   plugin has no write path to the ledger; `spec.audit.mutates: true` is a
   load error, not a warning.
3. **Approval** is the host's alone. A plugin may request; only the control
   plane grants, and a grant is bound to a digest of the exact proposed
   call, so a re-planned call needs a new approval. `max_blast_radius` is
   a ceiling the host clamps against the node's own policy — it cannot
   widen it.

### Validation

Every plugin in this directory is validated by
`internal/pkg/pluginmanifest/TestShippedPluginsAreValid`, so a manifest that
violates a rule fails the build rather than a production install. The rules
are enforced in the `sdk` module, which is also what third-party plugin
authors depend on — one implementation, not two.

Start from `opskeeper-sre-readonly/`, the read-only L1 baseline that every
node runs: eighteen read-only tools, the seven worker personas, and the
courier that makes every one of their tool calls ask the host first.

## The courier

`extensions/opskeeper-gate/` is the one extension here that is not a
capability. It registers a `tool_call` handler and asks the node's host
whether each call may run, blocking the ones it may not.

It is part of this package rather than of the edge binary because the agent
is a **separate process**: nothing inside it can be trusted to decide
whether one of its own tools may run. The tool allow-list, the approval
queue, and the audit ledger all live in the edge, and the courier is the
only thing that connects the two — over a unix socket, one line of JSON
each way.

Its canonical source is `core/pig/extensions/opskeeper-gate/`. The copy
here is deliberate: the agent builds extensions from source on the node, and
a node has no access to an unpublished OpsKeeper module.
`TestThePackagedCourierMatchesTheCanonicalSource` keeps the two identical,
so a node always runs the courier that was reviewed.

The courier fails closed on everything — no socket, an unreachable socket,
a truncated answer, a verdict it does not recognise. It holds no allow-list,
no queue, and no credentials: a socket path and a line protocol.

## The toolset

`extensions/opskeeper-sre-readonly/` is the other extension, and it is the
one that makes the eighteen `spec.tools` entries real.

It contains **no implementations**. Each entry in its table is a name, a
description and a JSON Schema; when the model calls one, the extension
carries the call across a unix socket to the node's host and returns what
the host said. That is the whole of it.

This is a deliberate shape, not a shortcut:

- **The agent process runs with the node's privileges.** Every tool that
  touches a node is exactly the kind of code that should not be
  reimplemented inside it. Routing means the agent process holds no
  operational capability the host did not hand it.
- **Half the toolset could not live there anyway.** `get_topology` and
  `query_alert_rules` read the manager's graph and its rule table. A
  subprocess on the node has no path to either, and inventing one would be
  a second, unaudited route into the control plane. The node asks, and the
  manager answers on the same authenticated tunnel every other RPC uses.
- **The host re-checks the allow-list before it dispatches.** The gate
  already refused the call on the way in, but the gate is reached through
  an extension in the agent process — a package that replaced the courier
  would silence that check. The broker is host code, reached only by name,
  and it consults the same registry. A tool has to survive being permitted
  by a check the agent could have suppressed.

The descriptions and schemas are copied from the host implementations rather
than rewritten. A tool whose schema the model sees and whose schema the
executor parses are two different objects, and a hand-edited copy of one is
a lie the model finds out about during an incident.
`TestEveryHostToolTheProfileDeclaresHasAnExecutorOnThisNode` and the
manager-side contract test are what keep the copy honest.

Its canonical source is `core/pig/extensions/opskeeper-sre-readonly/`, and
`TestThePackagedToolsetMatchesTheCanonicalSource` keeps the two identical.
