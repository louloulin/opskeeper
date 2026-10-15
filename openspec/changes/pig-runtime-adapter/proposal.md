## Why

OpsKeeper needs access to Pi's growing extension, skill, prompt, and package ecosystem, but replacing the existing agent engine would discard the product's differentiating incident, approval, audit, and controlled-execution control plane. A governed PiG Runtime Adapter lets OpsKeeper adopt Pi/PiG capabilities incrementally without weakening safety boundaries.

## What Changes

- Add an `AgentRuntimeAdapter` seam so OpsKeeper ChatRuntime, AgentTeams/TeamHarness, and PiG remain interchangeable runtimes instead of hard-coded execution engines.
- Add a PiG Runtime Adapter that can launch a governed PiG/Piglet runtime, expose read-only diagnostic capabilities, stream lifecycle events, cancel work, and report health.
- Normalize PiG extension capabilities into OpsKeeper's read/write/destructive tool classes and enforce capability allowlists before tools are visible to an agent.
- Route every PiG tool call through the OpsKeeper Governed Tool Gateway; PiG extensions receive no direct production credentials, host access, or unapproved write path.
- Define a staged Pi/PiG adoption path: ecosystem inventory, non-executable resources, read-only runtime, governed executable extensions, and gradual role migration.
- Represent Pi/PiG packages and extensions as immutable releases with source identity, digest, compatibility, capability manifest, and verification state; reuse the planned runtime-management plugin model rather than building a second plugin store.
- Add operational readiness gates for isolation, heartbeat, cancellation, audit completeness, rollback, and read-only behavior before any role migrates.
- Preserve the current ChatRuntime and AgentTeams integrations as first-class paths; PiG is opt-in and does not replace them in this change.

## Capabilities

### New Capabilities

- `pig-runtime-adapter`: Governed lifecycle, capability, tool-call, event, health, and cancellation contract for running PiG as an OpsKeeper agent runtime.
- `pig-ecosystem-adoption`: Staged ingestion, verification, governance, rollout, and migration path for Pi/PiG packages, Piglets, extensions, skills, prompts, and agents.

### Modified Capabilities

- None. The repository currently has no archived main specifications; this change does not alter the separate active `runtime-management-gap` proposal.

## Impact

- Add agent-runtime abstraction and PiG adapter code under the Manager AIOPS/runtime area.
- Reuse the existing chatruntime policy model and MCP/tool contracts; no existing ChatRuntime behavior is removed.
- Extend runtime health, event, and audit models only where needed for PiG observations and tool calls.
- Add Pi/PiG package inventory and compatibility validation without implementing an enterprise marketplace in this change.
- Potentially reuse the runtime-management plugin release, task claim, credential broker, and event timeline once that change lands; until then this change implements a self-contained read-only first slice.
- Affect deployment configuration with explicit opt-in settings for a pinned PiG executable or Piglet artifact; no ambient PiG discovery in production.
