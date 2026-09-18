---
comet_change: repair-preview-readback
role: technical-design
canonical_spec: openspec
---

# Repair Preview Readback Technical Design

## Architecture

Manager remains the authority for repair-preview facts. A new repair-preview domain owns run and candidate persistence, fixed-workload metadata, decision calculation, compact readback, and archive aggregation. The execution runner is isolated from request handling: it receives a versioned run request, creates disposable preview branches, executes the baseline and candidates, validates outputs, and submits a completed result to Manager. The live demo never triggers the full replay; it reads a run that was executed in advance and bound to the selected incident.

TeamHarness remains read-only. It consumes the existing Manager archive proxy and renders either the compact approval-gate summary or the full post-closure comparison. It does not write preview facts, derive approval authority, or cache a second source of truth.

## Domain Model

`RepairPreviewRun` is uniquely identified by `run_id` and bound to `tenant_id` plus `incident_id`. It stores:

- preview scope and branch naming prefix;
- deterministic seed fingerprint;
- versioned workload fingerprint and workload revision;
- isolation boundary text and controlled-load flag;
- execution status, start/end time, and error summary;
- bounded run artifact reference;
- created/updated timestamps.

`RepairPreviewCandidate` belongs to one run and stores:

- candidate ID and display name;
- candidate kind and sanitized change summary;
- branch identifier;
- result checksum;
- consistency status;
- latency average/median/p95 and sample count;
- throughput and errors;
- write impact and storage delta;
- business-probe outcome;
- final decision and bounded rejection reason.

Baseline is represented as a candidate with kind `baseline`. PASS requires complete comparison metrics, matching consistency policy, successful required business probes, and no prohibited write impact. A passing preview only makes a candidate eligible for HITL; it never records human approval.

## Runner and Data Flow

1. An operator requests a preview run for a selected incident and candidate set.
2. The runner resolves a versioned seed and workload manifest and computes their fingerprints.
3. It creates disposable preview-pg schemas or databases under a run-scoped prefix.
4. It applies the same seed to every branch, warms up each branch, and replays the same bounded workload.
5. For connection-pool exhaustion or lock-wait scenarios, the workload generates controlled saturation with finite clients and timeouts. The persisted boundary explicitly states that original active sessions are not copied.
6. The runner collects checksums, timing distributions, throughput, storage/write deltas, and business probes. Candidate A applies the bounded capacity adjustment; Candidate B applies the risky reset/session-clear action and must fail its defined safety predicate.
7. Manager validates and persists the completed run, appends compact incident events, and exposes only bounded sanitized summaries.

Raw SQL, parameters, credentials, production data, and unbounded logs remain outside API responses. Evidence references point to immutable run artifacts with access controlled by existing Manager authorization.

## API and Gate Integration

Manager exposes a compact, incident-bound summary for the approval-gate view. The summary contains baseline saturation metrics, Candidate A eligibility, Candidate B rejection, replay fingerprints, and the controlled-load boundary. It does not require the full table during the live presentation.

The incident archive response gains a bounded `repair_previews` array. Legacy incidents return an empty array and an explanatory plugin empty state; missing optional preview evidence does not reduce existing archive completeness.

All risky formal-change proposal paths call one shared preview validator. A proposal can enter HITL only when it references a PASS candidate from the same tenant and incident, with complete metrics and a current workload fingerprint. Candidate B is persisted as rejected by preview and cannot enter HITL. Existing human approval and execution flows remain unchanged after eligibility passes.

## Failure Handling

- Runner failure marks only that run failed and never changes the incident's operational state.
- Duplicate submissions use an idempotency key and return the stored result.
- Stale seed/workload fingerprints prevent a candidate from entering HITL.
- Tenant mismatch returns not found without revealing another tenant's run.
- Archive aggregation limits runs and candidates per incident and truncates or omits non-essential text.
- Preview branch cleanup is best effort after result persistence; run-scoped naming allows an operator to identify and remove leftovers.

## Testing Strategy

- Go unit tests validate domain rules, PASS/FAIL decisions, metric completeness, idempotency, stale fingerprints, and rejection reasons.
- Repository tests cover tenant isolation, incident lookup, duplicate handling, and legacy archive compatibility.
- Gate tests exercise every risky proposal entry path and prove Candidate B cannot bypass the shared validator.
- Runner integration tests use disposable PostgreSQL branches to verify seed determinism, checksum equality/divergence, latency collection, write impact, controlled saturation, cleanup, and timeout behavior.
- Dashboard tests cover response normalization, compact decision card rendering, full archive table rendering, legacy empty state, and sanitization-friendly field handling.
- Public E2E validates the complete sequence: controlled saturation, compact A/B decision card, Candidate A HITL approval, recovery verification, incident closure, and full Archive readback.

## Rollout

Deploy backward-compatible storage first, then the runner and Manager API, then TeamHarness. Execute and bind the real final-demo run before enabling the gate for the selected demo flow. Rollback can remove the plugin version and leave Manager extensions unused; legacy archive and approval behavior remain operational.
