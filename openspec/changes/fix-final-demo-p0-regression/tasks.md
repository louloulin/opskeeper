## 1. Pool Metrics Fix

- [ ] 1.1 Add `pool_manifest_id` labels to aggregate pool-fixture Prometheus series.
- [ ] 1.2 Add a versioned, manifest-agnostic public-demo pool metrics proxy script.
- [ ] 1.3 Add Go tests proving multi-fixture metrics are distinct and dynamically exposed.

## 2. AgentTeams Isolation Fix

- [ ] 2.1 Make Manager identity reject explicit Worker/standalone roles and isolate Manager continuation state.
- [ ] 2.2 Register Manager, Worker, and Team prompt sections only for their valid runtime roles.
- [ ] 2.3 Add outbound reply sanitization for thinking blocks and literal thinking spans.
- [ ] 2.4 Add configurable outbound burst limits and admin incident STOP circuit breaking.
- [ ] 2.5 Add Python tests for role identity, prompt gating, continuation isolation, sanitization, rate limits, and STOP.

## 3. Verification and Packaging

- [ ] 3.1 Run focused pool-fixture Go tests.
- [ ] 3.2 Run TeamHarness Python unit and package validation tests.
- [ ] 3.3 Update release version metadata and build local release artifacts on macOS.
- [ ] 3.4 Obtain deployment approval, deploy the verified artifacts, and run a fresh-manifest public E2E.
