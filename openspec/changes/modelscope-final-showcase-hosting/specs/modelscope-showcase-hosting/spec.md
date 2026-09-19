## ADDED Requirements

### Requirement: Maintain a coordinated ModelScope submission package
The project SHALL maintain a ModelScope submission package that identifies the authoritative source revision, release tag, showcase assets, submission URLs, visibility state, and verification result.

#### Scenario: Submission identity is auditable
- **WHEN** a reviewer inspects the ModelScope package manifest
- **THEN** it identifies the code repository, Creative Space, and project-practice submissions
- **AND** records the GitHub commit, release tag, TeamHarness version, public demo URLs, and visibility state
- **AND** does not identify a model repository because OpsKeeper publishes no model weights

#### Scenario: Source and showcase diverge
- **WHEN** the public demo runs a newer validated baseline than the default GitHub branch
- **THEN** the package records the exact demo commit and pending main-merge status
- **AND** the main-merge task remains open until the authoritative repository reaches that baseline

### Requirement: Keep showcase assets public-safe
The ModelScope showcase SHALL include only assets that are cleared for public disclosure and SHALL exclude runtime credentials, private topology, administrative tokens, unmasked sensitive incident data, and internal project-management records.

#### Scenario: Assets are selected for publication
- **WHEN** screenshots, diagrams, documents, or videos are added to the showcase
- **THEN** the package records their source, purpose, and digest
- **AND** a review confirms they contain no credential, secret, private endpoint, or sensitive customer data

#### Scenario: Claims must match implementation
- **WHEN** the showcase describes repair preview or reliability capabilities
- **THEN** it states that candidates run in disposable `preview-pg` branches with controlled fixed-load replay
- **AND** it does not claim PolarDB HA or copying of the original instance's active sessions

### Requirement: Provide a lightweight reachable showcase
The project SHALL provide a Creative Space showcase that loads without privileged OpsKeeper runtime dependencies and routes visitors to the authoritative website, repository, and public interactive demos.

#### Scenario: Visitor opens the Creative Space
- **WHEN** the showcase loads in a browser
- **THEN** it presents the project positioning, scenario, architecture, safety boundary, evidence summary, and media assets
- **AND** provides links to GitHub, the official website, OpsKeeper, AgentTeams Dashboard, and AgentTeams Element
- **AND** does not request or accept an OpsKeeper administrative credential

#### Scenario: Visitor opens a privileged roadshow entry
- **WHEN** a visitor selects OpsKeeper, AgentTeams Rooms, AgentTeams Dashboard, or the full-flow console from the Creative Space
- **THEN** the selected entry opens in a new browser tab at its authoritative URL
- **AND** existing authentication and route protection remain in force
- **AND** the Creative Space neither embeds, proxies, nor stores credentials

#### Scenario: Public interactive demo is unavailable
- **WHEN** an external interactive demo cannot be reached
- **THEN** the visitor can still understand the end-to-end workflow from the showcase text, diagrams, screenshots, and recorded evidence

### Requirement: Verify visibility transitions and links
The submission process SHALL verify the package while private, then re-verify required public links and media before roadshow exposure.

#### Scenario: Private submission is checked
- **WHEN** the GOAI submission is saved before public exposure
- **THEN** the recorded fields, code revision, media, and submission links pass a private-state readback
- **AND** any failure is tracked before the package becomes public

#### Scenario: Package is switched public
- **WHEN** the package is made public before the roadshow
- **THEN** the Creative Space starts, code is viewable, media plays, and all external demo links return successfully
- **AND** the verification timestamp and rollback instruction are recorded

#### Scenario: Legacy home domain is unavailable
- **WHEN** `home.yueming.xin` returns an error or cannot host the full-flow roadshow console
- **THEN** the package either restores the console or omits the entry from the public showcase until it is available
- **AND** the ModelScope showcase never exposes the failing URL as a required roadshow entry

#### Scenario: Official website path is unavailable
- **WHEN** `https://opskeeper.yueming.xin/home` does not return the official website
- **THEN** the package either deploys the website under `/home` without breaking the OpsKeeper root service or omits the website entry until it is available
- **AND** a 404 response is never presented as the official website entry
