---
comet_change: modelscope-final-showcase-hosting
role: technical-design
canonical_spec: openspec
---

# ModelScope Final Showcase Hosting — Technical Design

## Context

OpsKeeper needs a public-safe ModelScope submission that complements the authoritative GitHub repository and the five roadshow entries. The current workspace, `prepare-final-demo-main-flow`, is the sole source of truth for this change. The old `repair-preview-readback` workspace is historical input only.

The selected approach is a manifest-driven lightweight Gradio showcase. It presents OpsKeeper without hosting privileged operations, duplicating the control plane, or embedding authenticated consoles. GitHub remains the source-code authority; ModelScope is a showcase and code-mirror channel.

## Architecture

### Submission package

Create `deliverables/modelscope/` with a submission manifest, public-safe assets, and Creative Space application:

- `submission.json` records the package schema version, source revision, release tag, TeamHarness version, five authoritative URLs, submission URLs, visibility state, asset inventory, and verification history.
- `assets/` contains only approved screenshots, diagrams, and recorded media. Every asset has an adjacent inventory entry with source, purpose, authorization, SHA-256 digest, and public-safety status.
- `creative-space/` contains the Gradio application and localized presentation content. It has no runtime dependency on Manager, PostgreSQL, Matrix, AgentTeams, or the roadshow fixture.
- `checks/` contains deterministic local checks for manifest shape, asset integrity, link reachability, and public-safety scanning.

The manifest is the coordination record, not a second incident or capability fact source. Operational evidence values remain snapshots for publication and always identify their source report or recording.

### Creative Space application

The Gradio application renders a single-page Chinese showcase:

1. Project positioning and competition scenario.
2. Pain point, solution, and value wall.
3. OpsKeeper × AgentTeams architecture and plugin boundary.
4. Human approval, role separation, repair preview, and archive evidence summary.
5. Six external entry cards: GitHub, official website, OpsKeeper, AgentTeams Rooms, AgentTeams Dashboard, and roadshow console.
6. Media and screenshot evidence with fallback explanations.
7. Quick start and security boundary.

All privileged or authenticated entries open in new browser tabs with `rel="noopener noreferrer"`. The showcase does not embed, proxy, reverse-proxy, accept, store, or forward credentials for those systems. It has no administrative action and no server-side write API beyond rendering static content.

If ModelScope later permits a pure static submission without changing functionality, the same content and assets can be exported as static HTML. Gradio is chosen first for platform compatibility and predictable Creative Space startup.

### Entry and website integration

The five roadshow entries remain authoritative:

- `https://opskeeper.yueming.xin/home` — official website.
- `https://opskeeper.yueming.xin` — OpsKeeper service.
- `https://rooms.yueming.xin` — AgentTeams Element rooms.
- `https://teams.yueming.xin` — AgentTeams Dashboard.
- `https://home.yueming.xin` — full-flow roadshow console.

The official website adds a ModelScope ecosystem entry only after the Creative Space URL is known. The Creative Space links back to the website and GitHub. No route replaces the existing website `/zh/demo` page; ModelScope is an additional showcase and fallback surface.

The official website `/home` deployment and restoration of `home.yueming.xin` are prerequisites for publishing their respective entry cards. Until then, the cards are omitted and replaced with safe screenshots or recorded evidence, never a live failing URL.

### Source and mirror flow

1. Resolve and record local branch, GitHub default branch, public demo revision, TeamHarness version, and release tag.
2. Merge the validated final baseline into GitHub `main` and create the release/tag.
3. Import or synchronize the same revision to the ModelScope code repository.
4. Generate the submission manifest and asset digests from the authoritative workspace.
5. Save the Creative Space and project-practice submissions privately.
6. Re-run link, media, code, and field readback before switching public.

A submission is never marked aligned merely because assets were uploaded. The manifest must record readback values from GitHub and ModelScope. If the public demo runs a newer validated revision, the manifest records that divergence and leaves the main-merge task open.

## Data and Integrity Model

`submission.json` uses explicit top-level sections:

- `schema_version`: manifest contract version.
- `source`: GitHub repository, default branch, commit, tag, TeamHarness version, mirror URL, and mirror state.
- `entries`: authoritative external URLs, role, required status, auth expectation, and current health result.
- `submissions`: ModelScope code repository, Creative Space, and project-practice metadata.
- `assets`: publication inventory and digest metadata.
- `verification`: private check, public switch, final readback, timestamps, operator, and result.
- `rollback`: trigger conditions, private-switch action, entry-hiding action, and owner.

Timestamps use UTC with an explicit `Asia/Shanghai` presentation note in generated reports. Local checks regenerate digests and compare them before every visibility transition. A missing file, size mismatch, digest mismatch, unknown asset, or failed safety scan blocks publication.

## Public-Safety Controls

The safety review uses an allowlist, not a best-effort cleanup:

- Approved assets must be listed before packaging.
- Screenshots and recordings are reviewed for tokens, DSNs, passwords, private endpoints, internal identifiers, and unmasked incident data.
- Documents exclude private project-management links, internal task IDs, infrastructure topology, and customer-like data.
- Public text does not claim PolarDB HA, active-session cloning, or unimplemented autonomous production repair.
- Repair-preview claims are limited to disposable `preview-pg`, fixed controlled load, result consistency, latency/write-impact comparison, and HITL gating.

Automated scanning complements human review but does not replace it. The manifest records both scan output and human approval.

## Error Handling

- A required URL failure blocks public switching; an optional entry is hidden with an explanatory fallback.
- A Creative Space startup failure keeps the package private and uses the website/GitHub path while the issue is repaired.
- Missing media does not silently degrade the showcase; the build fails until replaced or explicitly removed from the manifest.
- A GitHub/ModelScope revision mismatch is visible in the manifest and submission report.
- Public exposure can be rolled back by returning the submissions to private visibility and hiding the website ModelScope entry.

## Testing and Verification

### Local checks

- Parse and validate `submission.json`.
- Verify every inventory file exists and its SHA-256 matches.
- Verify only allowlisted assets are packaged.
- Scan text and filenames for credential patterns and private-network references.
- Validate required and optional entry classification.
- Run the Creative Space application locally and smoke-test rendering.
- Run website typecheck and production build, including the `/home` base-path variant when that deployment is exercised.

### Browser checks

- Desktop and mobile rendering of the showcase.
- Images and video load and play.
- All external links open in new tabs.
- No link leaks a credential or private endpoint.
- Official website `/home` returns the website while the OpsKeeper root service remains reachable.
- `home.yueming.xin` reaches the full-flow console before its entry is published.

### Submission readback

- Private state: confirm required fields, code view, Creative Space startup, project-practice content, and media access.
- Public state: repeat the private checks, verify all required links, record the public-switch timestamp, and store rollback instructions.

## Implementation Boundaries

This change adds publication assets and checks. It does not modify OpsKeeper Manager, AgentTeams Controller, AgentTeams Dashboard, TeamHarness runtime behavior, Matrix, or the demo fixture. Website changes are limited to the ModelScope entry and existing `/home` deployment integration.

## Risks and Mitigations

- **Platform uncertainty**: Gradio is the compatibility-first runtime; content is structured for a static fallback.
- **Manual submission drift**: every readback updates `submission.json`; divergence cannot be hidden.
- **Sensitive evidence leakage**: allowlisting, digesting, automated scans, and human review are all required.
- **Roadshow availability**: failing required URLs block publication; optional entries degrade to recorded evidence.
- **Version confusion**: source, demo, release, and mirror revisions are recorded separately.
