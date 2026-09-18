# Repair Preview Readback Tasks

## 1. Contracts and Storage

- [ ] 1.1 Define preview run, candidate, workload, branch, metric, and decision JSON contracts with tenant/incident bindings and validation.
- [ ] 1.2 Add backward-compatible Manager storage and repository methods for append/list/read-by-incident.
- [ ] 1.3 Add compact `repair_preview` timeline events and archive aggregation without making legacy incidents incomplete.

## 2. Controlled Evaluation Runner

- [ ] 2.1 Add a deterministic preview-pg seed and fixed workload definition with stable seed/workload fingerprints.
- [ ] 2.2 Implement isolated per-candidate branch execution, baseline replay, checksum comparison, latency/TPS sampling, write/storage impact, and business-probe checks.
- [ ] 2.3 Add the final PG pool scenario: Candidate A bounded capacity adjustment; Candidate B risky reset/session-clear rejection.
- [ ] 2.4 Add focused runner tests for PASS, FAIL, consistency divergence, write loss, retries, and boundary metadata.

## 3. Manager Gate and Archive API

- [ ] 3.1 Block risky formal-change proposals unless they reference a PASS preview candidate with complete metrics.
- [ ] 3.2 Record rejected candidates without allowing them into HITL and preserve the rejection reason.
- [ ] 3.3 Extend the incident archive response with sanitized, bounded `repair_previews` data and tenant-isolated tests.

## 4. TeamHarness Archive Projection

- [ ] 4.1 Normalize preview run/candidate wrappers and legacy-missing responses.
- [ ] 4.2 Render workload fingerprint, isolation boundary, baseline/candidate comparison, consistency, latency, throughput, write impact, and decisions.
- [ ] 4.3 Keep the standalone preview page as a secondary link and add responsive/readability tests.

## 5. Final Demo and Deployment

- [ ] 5.1 Generate a real two-candidate run bound to the selected public incident ID before the live rehearsal.
- [ ] 5.2 Run the main-flow E2E: controlled saturation → incident-bound preview readback → Candidate A PASS and HITL approval → recovery verification; Candidate B FAIL and stays rejected.
- [ ] 5.3 Keep the live demo compact: show baseline/A/B decision evidence at the approval gate, then show the full comparison in Archive after closure; do not wait for the complete preview run during the audience-facing flow unless a replay is explicitly requested for Q&A.
- [ ] 5.4 Build locally, deploy Manager and TeamHarness to Aliyun, and verify public archive/API/plugin version readback.
- [ ] 5.5 Update final-demo script and evidence materials with the controlled-load boundary wording.
