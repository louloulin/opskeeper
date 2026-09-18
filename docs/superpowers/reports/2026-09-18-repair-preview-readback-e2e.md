# Repair Preview Readback E2E — 2026-09-18

## Scope and safety boundary

- Repository: `opskeeper-repair-preview-readback` at base `299ad493`.
- Public target: Aliyun `8.160.172.235`.
- Builds were performed only on the local Mac. Aliyun received artifacts and performed load/restart/apply operations only.
- AgentTeams and Dashboard host source were not modified.
- Manager remains the authority for preview evidence, HITL state, recovery execution, and incident archive; TeamHarness is a read-only projection plus routing.
- `PASS` means preview eligibility only. It does not mean human approval and does not mutate the production pool.
- Candidate B never entered HITL and never mutated the public pool.
- Preview boundary: controlled fixed-workload reconstruction in disposable `preview-pg`; original active sessions are not copied.
- Sensitive values, bearer tokens, gateway keys, JWT secrets, database DSNs, and passwords are intentionally omitted.

## Release readback

| Component | Result |
| --- | --- |
| Manager image | `opskeeper:repair-preview-299ad493-1.0.65` |
| Manager status | running; started `2026-09-18T07:14:41Z`; no restart loop |
| Dashboard root manifest | TeamHarness `1.0.65` |
| Dashboard `dist/plugin.json` | TeamHarness `1.0.65` |
| Dashboard entry | `dist/main-1.0.65.js`; HTTP 200, 110,749 bytes |
| AgentTeams Manager plugin | JSON and Python constants both `1.0.65` |
| All six workers | alerter/investigator/reviewer/repairer/verifier/reporter manifests all `1.0.65` |
| `preview-pg` | healthy |
| Disk after deployment | 66% used |

The compact projection strings were present in the deployed dashboard bundle: “修复预演审批门禁”, “PASS 仅代表预演资格通过”, “Candidate A”, and “Candidate B”.

## Real controlled preview

- Tenant: `goai-demo`
- Incident: `opskeeper-final-2a94e6df-20260917-144442`
- Run canonical ID: `1831d65f-f134-4e12-ad6e-d6d7fa6d1572`
- Stored run label: `1831D65F-F134-4E12-AD6E-D6D7FA6D1572`
- Status: `finished`
- Workload fingerprint: `sha256:db905b8f98c631212336b736f92d80b2a3040a75a44554687cfc782d39c31cc4`
- Seed fingerprint: `sha256:seed-v1`
- Seed revision: `seed-v1`
- Boundary: controlled fixed-workload reconstruction in disposable `preview-pg`; original active sessions are not copied.

| Role | Candidate ID | Stored UUID | Action | Consistency | Business probe | Decision |
| --- | --- | --- | --- | --- | --- | --- |
| Baseline | `baseline` | `18b470fb-55d6-593b-93c1-01ffc43cce0c` | `baseline` | PASS | PASS | `PASS` |
| A | `candidate-a` | `056bc7db-d491-5596-9e6a-8e21d24fbae2` | `resize_pool` | PASS | PASS | `PASS` |
| B | `candidate-b` | `0c2aecad-368d-5a2c-809d-563c121bd3c7` | `reset_pool` | PASS | FAIL | `REJECTED_BY_PREVIEW` |

Observed metrics:

- Baseline: p50 approximately 2.31 ms, p95 approximately 61.16 ms, 539.04 TPS, zero errors.
- Candidate A: p50 approximately 0.39 ms, p95 approximately 161.04 ms, 505.40 TPS, zero errors.
- Candidate B rejection reason: `business probe failed`.

The first tunnel-based attempt failed because SSH RTT exceeded the 500 ms workload timeout and wrote no preview rows. A locally built Linux arm64 runner was then uploaded and executed next to the public databases; it completed the run successfully. No remote compilation occurred.

## Public E2E timeline (UTC)

1. `07:30:07` — created a fresh incident-owned saturated pool fixture `4d10596facee1c57db91b863b649fa06` at capacity 4, target capacity 8.
2. `07:30:28` — the controlled probe failed with `pool_exhausted` after 500 ms.
3. `07:32:41` — Candidate B carrying its real preview binding was rejected by `recovery.execute` before proposal reservation with: `repair preview candidate is not eligible`. The dummy proposal UUID therefore remained unused.
4. `07:34:22` — the AgentTeams HITL API created the sole real pending proposal for Candidate A.
5. Human approval used the authenticated admin API and a verified payload hash/matrix event binding. The sole real proposal ID is `e5696388-f57e-4f20-80a1-67a9470f0e57`; state became `approved`.
6. `07:34:39` — repairer executed Candidate A through signed MCP. Audit log `1312` records the exact preview binding. The pool resized to capacity 8 and its recovery probe succeeded in under 1 ms.
7. `07:35:34` — repairer appended `action.executed`.
8. `07:35:43` — verifier appended `recovery_signal.observed` after observing status `recovered`, capacity 8, active connections 0, and successful recovery probe.
9. `07:35:43` — reporter appended `incident.closed`.

## Archive and projection verification

The public compact summary API returned HTTP 200 and authoritative values:

- Passing candidate: `candidate-a`, action `resize_pool`, decision `PASS`.
- Rejected candidate: `candidate-b`, action `reset_pool`, decision `REJECTED_BY_PREVIEW`, reason `business probe failed`.
- Controlled load: `true`.
- Isolation boundary was unchanged.

After closure, the incident archive returned:

- Event count: 14.
- Missing required events: none.
- Evidence complete: `true`.
- Recovery observed: `true`.
- Closed: `true`.
- Repair preview runs retained: 1, with baseline plus A/B candidates.

Legacy compatibility check:

- Legacy incident: `INC-PG-POOL-001`.
- Archive HTTP result: success.
- Event count: 6.
- Closed: `true`.
- Repair preview count: 0, correctly rendered as legacy-missing rather than treated as an archive failure.

The `/preview/` deep link returned HTTP 200 after closure. It remains a secondary link; Manager Archive is the authoritative readback.

## Rollback and cleanup state

- Previous Manager container remains stopped as `opskeeper-rollback-repair-preview-20260918` with the prior runtime available.
- Dashboard TeamHarness backup: `opskeeper-teamharness.pre-1.0.65.20260918T071344Z`.
- Nginx backups exist for the compact-summary proxy route.
- The old selected pool state was backed up to `pool-fixture-selected-incident-before-20260918.tgz` before removing only that incident's stale fixture state.
- The active replacement pool is recovered and has no held connections.
- No production database was modified by the preview; disposable `preview-pg` executed all candidate work.

## Final local validation

All commands completed successfully on 2026-09-18:

```text
openspec validate repair-preview-readback --strict
go test ./...
pnpm --dir plugins/opskeeper-teamharness/dashboard test
./plugins/opskeeper-teamharness/scripts/build-package.sh
git diff --check
```

Dashboard tests: 40 passed, 0 failed. Package hashes:

- Plugin-manager tarball: `de6effce0d4cd628ffb434f2d6fd7018e733ae88bf4205dadfacec08a46bac62`
- Dashboard zip: `9bf4f2feea6f2e1d11bc43f1b4eb377ab6c2638282c69df4cd71f85d0c0cbe4e`
- QwenPaw zip: `3633f8d4f55fb15eb03888e954f06e009120ee5e730e2b4593f5bf60ef65cb32`
