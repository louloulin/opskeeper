# OpsKeeper Architecture (Pi Agent Fleet · okp Phase 1)

**Status**: in progress · **Version**: 1.12 · **Last updated**: 2026-09-11
**Plan reference**: [`plan1.0.md`](./plans/plan1.0.md) §5.5 + 附录 D；[`plan1.1.md`](./plans/plan1.1.md) §5.1 F-1 / F-2
**Branch**: `docs/plan1.0-pi-agent-fleet` · **Commits**: local working tree includes F-1 fleet read model + F-2 cluster incident aggregation

This document is the durable architecture reference for the Pi agent fleet
work (okp Phase 1). It complements `plan1.0.md` — the plan tracks
**what** and **why**; this doc tracks **how** the implemented pieces fit
together.

Diagrams use [mermaid](https://mermaid.js.org/) (GitHub-native). Every
diagram has been hand-validated against the implemented paths in
`internal/edgeagent/` and the F-1 fleet read model at the revision listed above.

---

## 1 · One-paragraph summary

opskeeper-edge (Go 1.25) on a target host supervises a Pi-coding-agent
sidecar (TypeScript, `@earendil-works/pi v0.85.1`) bound to loopback.
Pi calls opskeeper-edge's HTTP tool layer (`127.0.0.1:9101`,
`enforceLoopback`) for host actions. Each tool call passes through
`cmdpolicy.Sandbox` (read-only by default; write paths require an
`X-Opskeeper-Pi-Approval-Token` whose lifetime is governed by
`cmdpolicy.ApprovalCache`) and is appended to `audit.Chain` (HMAC-SHA256,
append-only, local-only today; tunnel uplink in P-5). Edge runs in its
own process group so Pi + skill subprocesses die together when the
supervisor's `Stop` fires.

---

## 2 · Component map

| Layer | Package / Path | Role | Tests |
|---|---|---|---|
| Sidecar | `vendor/pi/` (submodule, `v0.85.1`) | Pi-coding-agent runtime | pin check |
| Process lifecycle | `internal/edgeagent/pisupervisor/` | spawn / health / backoff / crash-loop / Stop | 18 |
| HTTP tool API | `internal/edgeagent/server/` | `127.0.0.1:9101` + `enforceLoopback` | 25 |
| Shell policy | `internal/edgeagent/cmdpolicy/` | `DefaultPiCapable()` + `ApprovalCache` | 13+ |
| Audit | `internal/edgeagent/audit/` | HMAC chain + Verify | 16 |
| Real tools | `internal/edgeagent/host_files/` | `find_large_files` / `du_summary` / `stat_file` | 12+ |
| Real tools | `internal/edgeagent/restart_service/` | systemctl allow-list (Mocked=true today) | – |
| Pi prompt | `pi-skills/*/SKILL.md` + `AGENTS.md` | system prompt / skills | YAML ✓ |
| Hooks | `dist/hooks/hooks.yaml` | pi-yaml-hooks deny-list | yamllint ✓ |
| Upgrade | `scripts/sync-pi.sh` + `.github/workflows/sync-pi.yml` | `git pull --rebase` + verify | shellcheck ✓ |

---

## 3 · Topology — cloud ↔ edge ↔ Pi

```mermaid
flowchart LR
    subgraph Cloud["云端 opskeeper-manager (124.221.146.145:13001)"]
        C_Reviewer["reviewer worker<br/>ReAct + eino"]
        C_AuditStore[("audit DB<br/>postmortem 归档")]
        C_Web["Web UI<br/>Phase 2"]
        C_Reviewer --> C_AuditStore
    end

    subgraph Edge["目标机 opskeeper-edge 进程"]
        direction TB
        E_Sup["pisupervisor.Supervisor<br/>spawn + 退避重启 + crash-loop<br/>+ process group kill"]
        E_Pi["Pi sidecar<br/>vendor/pi v0.85.1<br/>--mode http --bind 127.0.0.1<br/>--port 19000"]
        E_HTTP["server.Server<br/>127.0.0.1:9101<br/>enforceLoopback 拒绝非 loopback"]
        E_Cmd["cmdpolicy.Sandbox<br/>DefaultPiCapable<br/>+ ApprovalCache"]
        E_Audit["audit.Chain<br/>HMAC-SHA256<br/>append-only"]
        E_Tools["host_files + restart_service<br/>(真实实现)"]

        E_Sup -- "fork/exec<br/>Setpgid=true" --> E_Pi
        E_Pi -- "HTTP POST<br/>loopback" --> E_HTTP
        E_HTTP --> E_Cmd
        E_HTTP --> E_Audit
        E_Cmd -. "调用" .-> E_Tools
    end

    E_Audit == "tunnel.v1.pi_audit<br/>(P-5 待云端联调)" ==> C_AuditStore
    E_HTTP == "POST /v1/edge/propose" ==> C_Reviewer
    C_Reviewer == "tunnel.v1.pi_approval_grant" ==> E_Cmd

    classDef blocked stroke-dasharray: 5 5
    class C_Reviewer,C_AuditStore blocked
```

**Trust boundary**: only Pi on the same host can hit edge's HTTP layer
(`enforceLoopback` rejects everything else). Operators needing remote
access must front edge with SSH/Unix-socket — never relax the loopback
check.

---

## 4 · Fleet F-1 — host read-model architecture

F-1 adds a cloud-side, read-only fleet view without introducing a new fleet table. The use case joins existing `devices`, `edges`, and `edge_devices` records, while the HTTP layer maps the result into a deliberately narrow response DTO.

```mermaid
flowchart LR
    Client[Authenticated fleet client] --> Protected[Manager protected chi router]
    Protected --> HTTP[GET /v1/fleet/hosts]
    HTTP --> Auth{tenantctx present?}
    Auth -- no --> Unauthorized[401 unauthorized]
    Auth -- yes --> Filter[Parse status / role / since / limit / offset]
    Filter --> UC[Fleet Usecase.List]
    UC --> DeviceRepo[DeviceRepo.List]
    UC --> EdgeRepo[EdgeRepo.List]
    UC --> LinkRepo[EdgeDeviceRepo.ListEdgesForDevice]
    DeviceRepo --> Devices[(devices)]
    EdgeRepo --> Edges[(edges)]
    LinkRepo --> Junction[(edge_devices)]
    Devices --> Join[Join device + junction + edge]
    Edges --> Join
    Junction --> Join
    Join --> Page[Apply since filter and pagination]
    Page --> DTO[Safe hostItem DTO]
    DTO --> Response[items + total JSON]
```

The fleet endpoint is intentionally read-only: no route in this slice mutates a device, edge, or junction. `access_key_id` and `secret_key_hash` remain internal model fields and are never copied into `hostDevice` or `hostEdge`.

## 5 · Fleet F-1 — request and join flow

```mermaid
sequenceDiagram
    autonumber
    participant C as Authenticated client
    participant R as Protected router
    participant H as Fleet HTTP handler
    participant U as Fleet usecase
    participant D as Device repo
    participant E as Edge repo
    participant J as EdgeDevice repo
    participant S as Safe DTO mapper

    C->>R: GET /v1/fleet/hosts?status&role&since&limit&offset
    R->>H: dispatch protected request
    H->>H: tenantctx authentication
    alt missing tenant context
        H-->>C: 401 unauthorized
    else authenticated
        H->>H: parse RFC3339 since and non-negative pagination
        H->>U: List(Filter)
        U->>U: validate status and role
        U->>D: List(DeviceListFilter)
        U->>E: List(non-deleted edges)
        loop each selected device
            U->>J: ListEdgesForDevice(device.ID)
            J-->>U: edge_devices rows
            U->>U: resolve edge IDs from edge map
        end
        U->>U: apply since, offset, and limit
        U-->>H: []Host + total
        H->>S: copy approved fields only
        S-->>H: hostItem[] without credentials
        H-->>C: 200 {items, total}
    end
```

## 6 · Fleet F-1 — filter, pagination, and trust boundary

```mermaid
flowchart TB
    Query[HTTP query] --> Parse[Handler parser]
    Parse --> Status[status: online / offline]
    Parse --> Role[role: server / storage / network / database / unknown]
    Parse --> Since[since: RFC3339]
    Parse --> Paging[limit + offset]
    Status --> DeviceFilter[Device ListFilter.Online]
    Role --> RoleFilter[RolesAny or RolesUnknownOnly]
    Since --> LastSeen[Device.LastSeenAt comparison]
    DeviceFilter --> ReadModel[Fleet read model]
    RoleFilter --> ReadModel
    LastSeen --> ReadModel
    Paging --> ReadModel
    ReadModel --> Safe[DTO allow-list]
    Safe --> Public[Public fleet JSON]

    subgraph InternalTrust[Internal trust boundary]
        Secrets[Edge credentials<br/>access_key_id / secret_key_hash]
        Models[Device / Edge models]
        Secrets --> Models
        Models -. never copied .-> Safe
    end

    Public -->|no credentials| Consumer[Fleet UI or API consumer]
```

Security invariants for this slice:

- the protected router and `tenantctx` check run before the service call;
- status and role are validated by the use case rather than treated as free-form SQL fragments;
- pagination is applied after the read-model join, and `total` describes the filtered pre-page set;
- the public DTO is an allow-list, so adding a sensitive field to the internal `Edge` model does not expose it accidentally;
- F-1 performs no writes and does not alter the existing edge trust boundary.

---

## 7 · Fleet F-2 — cluster-wide incident aggregation architecture

The alert pipeline already deduplicates **per host**: one
`(scope, device, rule)` triple maps to exactly one `alert_incidents` row
keyed by `dedupe_key`. That keeps a single host's flap from spamming the
board, but it also means a fault that hits N hosts — a bad deploy, a
shared storage backend, a network partition — surfaces as N unrelated
incidents, each with its own RCA. F-2 adds the missing layer: a
deterministic, LLM-free read model that folds per-host incidents into one
cluster-wide incident ("wave").

```mermaid
flowchart LR
    Client[Authenticated fleet client] --> Protected[Manager protected chi router]
    Protected --> HTTP[GET /v1/fleet/cluster-incidents]
    HTTP --> Auth{tenantctx present?}
    Auth -- no --> Unauthorized[401 unauthorized]
    Auth -- yes --> Wired{ClusterService wired?}
    Wired -- no --> NotWired[501 not-wired-yet]
    Wired -- yes --> Parse[Parse status / severity / since / window / min_hosts / limit / offset]
    Parse --> Validate[Validate status / severity / window / min_hosts]
    Validate -- invalid --> Bad[400 invalid]
    Validate -- ok --> UC[ClusterUsecase.List]

    UC --> IncRepo[IncidentRepo.ListIncidents<br/>newest-first, capped at 2000 rows]
    UC --> DevRepo[DeviceRepo.List<br/>best-effort host facts]
    IncRepo --> Incidents[(alert_incidents)]
    DevRepo --> Devices[(devices)]

    Incidents --> Classify[classifyAnomaly<br/>rule key to root-cause class]
    Incidents --> Dimension[signalDimension<br/>host-identity labels stripped]
    Classify --> Bucket[Bucket by class + dimension]
    Dimension --> Bucket
    Bucket --> Wave[Split each bucket into waves<br/>by sliding first-firing window]
    Wave --> Rollup[Per-host rollup<br/>join device facts, worst severity/status wins]
    Devices --> Rollup
    Rollup --> Floor[Drop waves below MinHosts]
    Floor --> Sort[Sort most-severe-first]
    Sort --> Page[Paginate AFTER grouping]
    Page --> DTO[clusterIncidentItem DTO]
    DTO --> Response[items + pre-pagination total]
```

F-2 is read-only: it consumes `alert_incidents` + `devices` and writes
nothing. The grouping decision is deterministic — the same incident rows
always produce the same groups in the same order — so the API is safe to
cache and the tests assert exact output. Semantic "same root cause"
ranking beyond this stays with the existing LLM semantic-dedup layer in
`internal/manager/biz/alert`, which this read model consumes rather than
duplicates.

## 8 · Fleet F-2 — grouping rule, ranking, and trust boundary

```mermaid
flowchart TB
    Row[alert_incidents row] --> Norm[normalizeRuleKey<br/>lowercase, '/' '-' ':' ' ' to '.']
    Norm --> Exact{Prefix table hit?<br/>key == prefix or prefix + '.'}
    Exact -- yes --> ClassA[Root-cause class<br/>e.g. host.cpu_saturation]
    Exact -- no --> Kw{Keyword substring hit?<br/>unseparated Alertmanager names}
    Kw -- yes --> ClassA
    Kw -- no --> ClassB[rule.&lt;normalized key&gt;<br/>same rule still aggregates]

    Row --> Labels[Parse LabelsJSON]
    Labels --> Strip[Drop host-identity labels<br/>host / instance / device_id / edge / opskeeper_source]
    Strip --> Sig[Signal dimension<br/>sorted key=value fingerprint]

    ClassA --> Group[Group key = class + dimension]
    ClassB --> Group
    Sig --> Group
    Group --> Window[Sliding window on FirstFiredAt<br/>default 10m, max 24h]
    Window --> HostFloor{Distinct hosts >= min_hosts?<br/>default 2, max 100}
    HostFloor -- no --> Drop[Not cluster-wide, omitted]
    HostFloor -- yes --> Keep[Cluster incident]
    Keep --> Rank[severity rank: critical 3 &gt; warning 2 &gt; info 1<br/>status rank: open 4 &gt; ack 3 &gt; silenced 2 &gt; resolved 1]
    Rank --> Out[Sorted, paginated response]

    subgraph Trust[Internal trust boundary]
        Secrets[Edge credentials<br/>access_key_id / secret_key_hash]
        Raw[Device / Edge / Incident models]
        Secrets --> Raw
        Raw -. never copied .-> Out
    end
```

**Grouping rule.** Two incidents join the same wave when they share
(1) the same anomaly class derived from the rule key, (2) the same signal
dimension with host-identity labels stripped, and (3) a bounded
first-firing window (sliding, default 10 minutes). A recurrence outside
the window becomes a separate wave, so the view never merges an unrelated
recurrence into a stale group. A group needs at least `min_hosts`
(default 2) distinct hosts to be considered cluster-wide; an incident
with no `device_id` still counts as a member but never as a host.

**Two rule-key families reach `alert_incidents`**, and the classifier
covers both:

| Family | Example key | How it matches |
|---|---|---|
| Built-in seed rules (`seed_rules.go`) | `cpu_high`, `disk_full_warning`, `scrape_down` | exact prefix-table entry |
| Harness / custom `<domain>/<case>` keys | `host/cpu-spike`, `k8s/pod-oom` | prefix table after separator normalization |
| Alertmanager alert names forwarded by webhook | `HostHighCpuLoad`, `KubePodOOMKilled` | substring keyword stage (no separator exists to match a prefix) |

A key that matches nothing keeps its own class (`rule.<normalized>`), so
"same rule" still aggregates across hosts even when the rule is unknown.

**Security invariants for this slice** mirror F-1: the protected router
and `tenantctx` check run before the service call; an unwired
`ClusterService` answers `501`, not `500`; filter values are validated by
the use case rather than passed through; pagination applies after
grouping and `total` describes the pre-page grouped set; and the public
DTO is an allow-list, so the response carries incident ids, titles,
severities and timestamps — never credentials.

---

## 9 · Sequence — single tool call (read path)

```mermaid
sequenceDiagram
    autonumber
    participant L as LLM
    participant P as Pi runtime
    participant H as edge handler
    participant CP as cmdpolicy.Sandbox
    participant HF as host_files.RunStatOne
    participant AU as audit.Chain

    L->>P: "查 /var/log/syslog 权限"
    P->>H: POST /v1/edge/tools/host_files/stat_file
    H->>HF: RunStatOne(ctx, sandbox, path)
    HF->>HF: sandbox.ValidatePath<br/>(denylist + allowlist + symlink)
    HF-->>H: StatFileResultEntry
    H->>AU: Append("tool.call.host_files.stat_file", payload, "")
    AU-->>H: Event{Sequence, Hash}
    H-->>P: 200 hostFilesReadResponse
    P-->>L: tool result
```

---

## 10 · Sequence — write path (approval gate)

```mermaid
sequenceDiagram
    autonumber
    participant L as LLM
    participant P as Pi runtime
    participant H as edge handler
    participant CP as cmdpolicy.Sandbox
    participant AC as cmdpolicy.ApprovalCache
    participant RS as restart_service.SandboxConfig
    participant Sys as systemctl
    participant AU as audit.Chain

    Note over P,AC: 前置：云端 reviewer 已通过 P-5 tunnel<br/>下发 ApprovalToken 到 AC

    L->>P: "重启 nginx"
    P->>H: POST /v1/edge/tools/host_restart_service<br/>{service: "nginx", proposal_id: "p-1"}<br/>X-Opskeeper-Pi-Approval-Token: tk-1
    H->>AC: Consume(tk-1, "p-1", "restart_service", edgeID)
    alt token missing / expired / mismatch
        AC-->>H: ErrApprovalMissing / ErrApprovalExpired / ErrProposalMismatch / ErrKindMismatch / ErrEdgeMismatch
        H-->>P: 401/403
    else consumed (single-use)
        AC-->>H: nil
    end
    H->>RS: Restart("nginx")
    alt Mocked=true (今天)
        RS-->>H: {Allowed: true, Mocked: true}
    else Mocked=false (P-5 后)
        RS->>Sys: exec.CommandContext("systemctl", "restart", "nginx")
        Sys-->>RS: stdout/stderr
    end
    H->>AU: Append("tool.call.host_restart_service", payload, "p-1")
    H-->>P: 200 restartResponse{AuditSeq}
```

---

## 11 · State machine — pisupervisor.Supervisor

```mermaid
stateDiagram-v2
    [*] --> New: New(Config)
    New --> Starting: Start(ctx)
    Starting --> Running: spawn OK<br/>first 2xx probe
    Starting --> Restarting: spawn fail /<br/>install fail /<br/>auto-upgrade fail
    Running --> Unhealthy: probe 连续 ≥ 3 失败
    Running --> Restarting: child 异常退出
    Unhealthy --> Restarting: killProcessGroup(-PID)
    Restarting --> CrashLoop: window 内 ≥ 5 次<br/>(isCrashLoop 剪枝)
    Restarting --> Starting: sleepBackoff(ctx)<br/>exponential capped 100ms→1s<br/>(computeBackoff)
    CrashLoop --> [*]: doneCh close
    Running --> Stopped: Stop(timeout)<br/>close(stopCh)
    Restarting --> Stopped: stopCh 触发
    Stopped --> [*]
```

**Key invariants**

- `runLoop` puts `child.Wait()` in a side goroutine so `stopCh` /
  `ctx.Done()` can interrupt a stuck child. Stop's deadline timer falls
  back to `killProcessGroup(-PID, SIGKILL)` if the child hasn't drained.
- `Setpgid` on spawn ensures `kill -PID` takes down Pi's entire
  subprocess tree (LLM stream, skill subprocesses).
- `Upgrade()` refuses without `TagLock` — defense against upstream
  tag-flipping.
- `installExtraPackages` is pre-flight; failure flips state to
  `CrashLoop` (fail-closed).

---

## 12 · Module dependency graph

```mermaid
flowchart TB
    cmd_main["cmd/opskeeper-edge/main.go<br/>(TODO)"]
    biz_agent["internal/edgeagent/biz/agent.go<br/>(TODO)"]
    pisupervisor["internal/edgeagent/pisupervisor/"]
    server["internal/edgeagent/server/"]
    cmdpolicy["internal/edgeagent/cmdpolicy/"]
    audit["internal/edgeagent/audit/"]
    host_files["internal/edgeagent/host_files/"]
    restart_service["internal/edgeagent/restart_service/"]
    biz_config["internal/edgeagent/biz/config.go<br/>(TODO)"]
    tunnel["internal/pkg/tunnel/"]
    vendor_pi["vendor/pi/ (submodule)"]

    cmd_main --> biz_agent
    cmd_main --> biz_config
    biz_agent --> pisupervisor
    biz_agent --> server
    biz_config --> pisupervisor
    pisupervisor --> vendor_pi
    server --> cmdpolicy
    server --> audit
    server --> host_files
    server --> restart_service
    cmdpolicy -. "ApprovalCache<br/>来自云端" .-> tunnel
    audit -. "P-5 上行" .-> tunnel

    classDef todo stroke-dasharray: 3 3,opacity:0.7
    class cmd_main,biz_agent,biz_config todo
```

Solid arrows = implemented dependencies. Dashed arrows = planned (P-5
tunnel RPC + the `biz/agent.go` wiring that hasn't landed yet — it's
the only missing glue between the four implemented stacks and
`cmd/opskeeper-edge/main.go`).

---

## 13 · Audit chain data model

```mermaid
classDiagram
    class Event {
        +uint64 Sequence
        +time.Time Timestamp
        +string Kind
        +string EdgeID
        +string ProposalID
        +json.RawMessage Payload
        +string PrevHash
        +string Hash
    }
    class Chain {
        -sync.Mutex mu
        -[]Event events
        -[]byte key
        -string edgeID
        +Append(kind, payload, proposalID) Event,error
        +Snapshot() []Event
        +Last() Event,bool
        +Len() int
        +Verify() error
    }
    Chain "1" --> "*" Event : append-only
    Event --> Event : prev_hash linkage
```

**Triple verification** (per `audit.Chain.Verify`):

1. Dense sequence — `Sequence[i] == i+1`
2. `PrevHash[i] == Hash[i-1]`
3. Recompute digest under key; compare to `Hash[i]`

Genesis prev_hash = 32 zero bytes. `MinKeyBytes=32` enforced.

---

## 14 · cmdpolicy class taxonomy

```mermaid
classDiagram
    class Policy {
        +PathAllowlist []string
        +NetworkHostAllowlist []string
        +ReadCommands []CommandMatcher
        +WriteCommands []CommandMatcher
    }
    class CommandMatcher {
        +string Bin
        +[]string Args
        +Class Class
    }
    class Class {
        <<enum>>
        ReadFS
        ReadSystem
        Mixed
        Network
        Denied
    }
    class ApprovalToken {
        +string TokenID
        +string ProposalID
        +string Kind
        +string EdgeID
        +time.Time GrantedAt
        +time.Time ExpiresAt
    }
    class ApprovalCache {
        +Put(token)
        +Consume(tokenID, proposalID, kind, edgeID) error
        +EvictExpired()
    }
    Policy "1" --> "*" CommandMatcher
    CommandMatcher --> Class
    ApprovalCache "1" --> "*" ApprovalToken
```

**DefaultPiCapable** (`internal/edgeagent/cmdpolicy/policy_pi.go`):
`DefaultReadOnly ∪ {kill -l, pgrep, pidof, pkill --list}`, with
`PathAllowlist` widened for systemd/journal/os-release and
`NetworkHostAllowlist` restricted to `127.0.0.0/8 + ::1/128`.

---

## 15 · Deployment view (target host)

```mermaid
flowchart TB
    subgraph Host["目标机 (待 P-2 e2e)"]
        direction TB
        systemd["systemd unit:<br/>opskeeper-edge.service"]
        edge_bin["opskeeper-edge binary<br/>/usr/local/bin/"]
        pi_bin["pi binary<br/>/usr/local/bin/pi<br/>(node 22.19+ runtime)"]
        skills["/var/lib/opskeeper/skills/<br/>(rendered from pi-skills/)"]
        env["/etc/opskeeper/edge.env<br/>(OPSKEEPER_PI_*)"]

        systemd --> edge_bin
        edge_bin --> pi_bin
        edge_bin --> skills
        edge_bin -. "读取" .-> env
    end

    cloud["云端 opskeeper-manager<br/>8.160.172.235:13001"]

    edge_bin == "tunnel.v1.*<br/>(geminio via frontier)" ==> cloud
```

**State of the world today**: code is committed; the systemd unit, edge
binary installation, and `edge.env` are P-2 e2e work that requires a
target host (currently a documented blocker).

---

## 16 · Status matrix (plan §5.5 + plan1.1 F-1 / F-2)

| State | Count | Items |
|---|---:|---|
| ✅ verified | 9 | P-1, P-7, P-8, P-11, P-12, G-2 (partial), G-5, plan1.1 F-1, plan1.1 F-2 |
| 🟡 partial | 6 | P-2, P-3, P-4, P-6, G-3, plus the remaining partial baseline items |
| ⛔ blocked | 32 | plan1.0 unfinished items plus F-3/A-1/A-2/A-3/S-1/S-2/S-3 |

Across plan1.0 and plan1.1 this is **19.1% verified, 12.8% partial, and 68.1% blocked** (9/47, 6/47, and 32/47). These are work-item states, not production-readiness or code-coverage percentages.

The blocked work is primarily constrained by:

1. **Target host** for real edge deployment + systemd unit
2. **LLM API key** to feed Pi (`OPSKEEPER_PI_LLM_*`)
3. **Cloud admin credentials** for reviewer worker + audit DB
4. **GitHub push credential** to ship commits off devbox

---

## 17 · What's verified end-to-end today

| Path | Verified by | Status |
|---|---|---|
| `vendor/pi` pinned to `v0.85.1` | `git describe` | ✅ |
| 4 SKILL.md frontmatter | YAML parse | ✅ |
| `scripts/render-pi-system-md.py` idempotency | render→check→drift detect | ✅ |
| `dist/hooks/hooks.yaml` syntax | yamllint | ✅ |
| `scripts/sync-pi.sh` shellcheck | shellcheck 0 errors | ✅ |
| `.github/workflows/sync-pi.yml` syntax | yamllint 0 errors | ✅ |
| `internal/edgeagent/cmdpolicy` tests | `go test` 13+ cases | ✅ |
| `internal/edgeagent/audit` tests | `go test` 16 cases | ✅ |
| `internal/edgeagent/pisupervisor` tests | `go test` 18 cases | ✅ |
| `internal/edgeagent/server` tests | `go test` 25 cases | ✅ |
| `internal/edgeagent/host_files` tests | `go test` 12 cases | ✅ |
| `GET /v1/fleet/hosts` read model and handler | targeted `go test -mod=mod` + `go vet -mod=mod` | ✅ |
| `GET /v1/fleet/cluster-incidents` grouping usecase | targeted `go test -mod=mod` 13 cases + `go vet -mod=mod` | ✅ |
| `GET /v1/fleet/cluster-incidents` HTTP contract | targeted `go test -mod=mod` 6 cases (auth 401 / unwired 501 / filter parse / 400 / error map / empty array) | ✅ |

---

## 18 · What's NOT verified (and what blocks it)

| Path | Blocker |
|---|---|
| Real Pi binary spawning + systemd unit | Target host |
| Edge HTTP listening on `127.0.0.1:9101` | Target host (test-only via httptest) |
| Real `systemctl restart nginx` shell-out | Target host |
| `biz/agent.go` startup wiring | Target host + LLM key |
| `tunnel.v1.pi_audit` / `pi_approval_grant` uplink | Cloud admin credentials |
| Web UI Pi tab | Cloud admin + frontend env |
| Harness cases `internal/harness/cases/pi/*` | devbox-internal but deferred to P-10 |
| Full `cmd/opskeeper` binary build | devbox has `CGO_ENABLED=0` and no `gcc`. `internal/pkg/embedding` imports `github.com/anush008/fastembed-go`, which imports `github.com/yalue/onnxruntime_go` — a cgo-only package with no files under that constraint, so anything linking the embedding chain (including `data/alert/store` and therefore `cmd/opskeeper`) fails to link. Both fleet packages build, vet and test clean with `-mod=mod`. |
| F-2 grouping against a live `alert_incidents` table | Needs cloud admin credentials + a populated incident history |

---

## 19 · Open questions for the operator

1. Which host should we deploy edge to first? (Need SSH + systemd +
   network reachability to `8.160.172.235:13001`.)
2. LLM provider + API key? (Plan §6.4 expects `OPSKEEPER_PI_LLM_*`.)
3. GitHub push: PAT / SSH key / gitcode mirror?
4. Cloud admin user for the reviewer worker (NOT the `Lumosai2026` —
   that password was rotated already).

---

## Appendix · Repo layout

```
opskeeper/
├── cmd/opskeeper-edge/main.go         (TODO — wired in v1.11)
├── docs/
│   ├── architecture.md                ← this file
│   └── plans/plan1.0.md               ← master plan
├── dist/hooks/hooks.yaml              (P-12 pi-yaml-hooks)
├── pi-skills/                         (P-7 + P-8)
│   ├── opskeeper-restart-service/SKILL.md
│   ├── opskeeper-host-files/SKILL.md
│   ├── opskeeper-diagnostics/SKILL.md
│   ├── opskeeper-incident-report/SKILL.md
│   └── opskeeper-monitor/AGENTS.md
├── scripts/
│   ├── sync-pi.sh                     (G-5 upgrade)
│   └── render-pi-system-md.py         (P-8)
└── internal/
    ├── edgeagent/
    │   ├── audit/         chain.go        (P-6)
    │   ├── biz/           agent.go        (TODO)
    │   ├── cmdpolicy/     policy_pi.go    (P-4)
    │   │                  approval.go
    │   ├── host_files/    handlers.go     (P-3 helpers)
    │   ├── pisupervisor/  supervisor.go   (P-2)
    │   │                  health.go
    │   └── server/        server.go + 6   (P-3)
    │                      handler files
    └── manager/
        ├── biz/fleet/     usecase.go      (F-1 read model)
        │                  repo.go
        │                  cluster.go      (F-2 cluster aggregation)
        ├── server/fleet/  http.go         (/v1/fleet/hosts + cluster-incidents)
        └── data/alert/store/              (F-2 source: alert_incidents)
```
