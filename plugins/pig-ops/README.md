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
│   └── diagnose-readonly/
│       └── SKILL.md          # frontmatter: name + description
├── extensions/               # optional; Go / Rust / Python / TypeScript
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
  capabilities: [read, write, destructive]
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
node runs.
