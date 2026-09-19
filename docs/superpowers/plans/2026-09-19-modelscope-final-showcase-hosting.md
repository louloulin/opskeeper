---
change: modelscope-final-showcase-hosting
design-doc: docs/superpowers/specs/2026-09-19-modelscope-final-showcase-hosting-design.md
base-ref: 96731ba3cb0c93a3989f3acd445ef7e3f62a3528
---

# ModelScope Final Showcase Hosting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a public-safe, manifest-driven ModelScope submission package and lightweight Gradio showcase while keeping GitHub and the five OpsKeeper roadshow entries authoritative.

**Architecture:** Local deliverables live under `deliverables/modelscope/`. A JSON manifest records source, mirror, assets, entries, visibility, and verification state; a standard-library Python validator enforces integrity and public-safety boundaries. A Gradio app renders approved content and media without privileged runtime dependencies. Online GitHub, ModelScope, website, and roadshow operations are explicit readback-gated manual stages.

**Tech Stack:** Python 3 standard library for checks, Gradio 5.x for Creative Space, existing Next.js site for optional ecosystem linking, Markdown/JSON for auditable submission records.

**Spec:** `openspec/changes/modelscope-final-showcase-hosting/` and `docs/superpowers/specs/2026-09-19-modelscope-final-showcase-hosting-design.md`

## Global Constraints

- Current workspace `/Users/yueming/Workspace/Goai/opskeeper-main-2a94e6df` is the sole decision source; do not update ModelScope state in the old workspace.
- GitHub is the source-code authority; ModelScope is a showcase and mirror.
- Do not create a model repository because OpsKeeper has no model weights.
- Creative Space must not embed, proxy, reverse-proxy, accept, store, or forward credentials for OpsKeeper, Matrix, AgentTeams, or the roadshow console.
- Privileged entry links must open in new tabs with `rel="noopener noreferrer"`.
- Public assets are allowlisted and digested; unlisted files block validation.
- Exclude tokens, DSNs, passwords, private endpoints, internal project-management data, and unmasked incident data.
- Do not claim PolarDB HA, active-session cloning, or autonomous production repair.
- Required failing URLs block public switching; optional entries may be hidden with evidence fallback.
- Timestamps stored in UTC and presented with `Asia/Shanghai` when shown to humans.
- Do not overwrite or commit unrelated existing website work while implementing this package.

---

### Task 1: Submission manifest and deterministic validator

**Files:**
- Create: `deliverables/modelscope/submission.json`
- Create: `deliverables/modelscope/checks/validate.py`
- Modify: `openspec/changes/modelscope-final-showcase-hosting/tasks.md`

**Interfaces:**
- Consumes: the five authoritative URLs from the OpenSpec change.
- Produces: `deliverables/modelscope/submission.json` with `schema_version`, `source`, `entries`, `submissions`, `assets`, `verification`, and `rollback`.
- Produces: `python3 deliverables/modelscope/checks/validate.py [--links] [--public]` returning exit code `0` only when valid.

- [ ] **Step 1: Create the initial draft manifest**

Use explicit draft state and unknown fields rather than inventing release or ModelScope values:

```json
{
  "schema_version": 1,
  "state": {
    "package_status": "draft",
    "visibility": "private",
    "generated_at_utc": null,
    "operator": "yueming"
  },
  "source": {
    "github_repository": "https://github.com/vincent-wuhan/opskeeper",
    "default_branch": "main",
    "authoritative_commit": null,
    "release_tag": null,
    "teamharness_version": null,
    "modelscope_code_url": null,
    "mirror_status": "not_created"
  },
  "entries": [
    {
      "id": "official_website",
      "url": "https://opskeeper.yueming.xin/home",
      "role": "Product official website",
      "required_for_public": true,
      "auth_expected": false,
      "health": "unknown"
    },
    {
      "id": "opskeeper_service",
      "url": "https://opskeeper.yueming.xin",
      "role": "OpsKeeper service",
      "required_for_public": true,
      "auth_expected": true,
      "health": "unknown"
    },
    {
      "id": "agentteams_rooms",
      "url": "https://rooms.yueming.xin",
      "role": "AgentTeams Element rooms",
      "required_for_public": true,
      "auth_expected": true,
      "health": "unknown"
    },
    {
      "id": "agentteams_dashboard",
      "url": "https://teams.yueming.xin",
      "role": "AgentTeams Dashboard",
      "required_for_public": true,
      "auth_expected": true,
      "health": "unknown"
    },
    {
      "id": "roadshow_console",
      "url": "https://home.yueming.xin",
      "role": "Full-flow roadshow console",
      "required_for_public": true,
      "auth_expected": true,
      "health": "unknown"
    }
  ],
  "submissions": {
    "creative_space": {"url": null, "status": "not_created", "visibility": "private"},
    "code_repository": {"url": null, "status": "not_created", "visibility": "private"},
    "project_practice": {"url": null, "status": "not_created", "visibility": "private"},
    "model_repository": {"created": false, "reason": "OpsKeeper publishes no model weights"}
  },
  "assets": [],
  "verification": {
    "private_readback": null,
    "public_readback": null,
    "safety_scan": null,
    "current_result": "pending"
  },
  "rollback": {
    "trigger": "Any required public link, media, code view, or safety check fails",
    "actions": [
      "Switch all three ModelScope submissions back to private",
      "Remove or hide the website ModelScope entry",
      "Record failure time UTC and observed symptoms in submission.json"
    ]
  }
}
```

- [ ] **Step 2: Implement the validator**

Implement exact validation rules, not approximate warnings:

```python
#!/usr/bin/env python3
import argparse
import hashlib
import json
import re
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MANIFEST_PATH = ROOT / "submission.json"
ASSET_ROOT = ROOT / "assets"
REQUIRED_TOP_LEVEL = {
    "schema_version", "state", "source", "entries", "submissions",
    "assets", "verification", "rollback",
}
REQUIRED_ENTRY_IDS = {
    "official_website", "opskeeper_service", "agentteams_rooms",
    "agentteams_dashboard", "roadshow_console",
}
BLOCKED_PATTERNS = [
    re.compile(r"(?i)(api[_-]?key|secret|password|passwd|token)"),
    re.compile(r"\b(postgres(?:ql)?):[^/\s]+:[^@\s]+@"),
    re.compile(r"\b(10|172\.(?:1[6-9]|2\d|3[01]))\.\d+\.\d+\b"),
    re.compile(r"\b192\.168\.\d+\.\d+\b"),
]

def fail(errors, message):
    errors.append(message)

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--links", action="store_true")
    parser.add_argument("--public", action="store_true")
    args = parser.parse_args()
    errors = []
    try:
        manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    except Exception as exc:
        print(f"manifest parse failed: {exc}", file=sys.stderr)
        return 1

    missing = REQUIRED_TOP_LEVEL - manifest.keys()
    if missing:
        fail(errors, f"missing top-level fields: {sorted(missing)}")
    if manifest.get("schema_version") != 1:
        fail(errors, "schema_version must be 1")
    if manifest.get("submissions", {}).get("model_repository", {}).get("created"):
        fail(errors, "model repository must not be created")

    entries = manifest.get("entries", [])
    entry_ids = {entry.get("id") for entry in entries}
    if entry_ids != REQUIRED_ENTRY_IDS:
        fail(errors, f"entry ids differ: {sorted(entry_ids)}")
    for entry in entries:
        if not entry.get("url", "").startswith("https://"):
            fail(errors, f"entry {entry.get('id')} is not HTTPS")

    listed = set()
    for asset in manifest.get("assets", []):
        relative = asset.get("path", "")
        path = (ASSET_ROOT / relative).resolve()
        if not str(path).startswith(str(ASSET_ROOT.resolve())):
            fail(errors, f"asset escapes root: {relative}")
            continue
        if not path.is_file():
            fail(errors, f"asset missing: {relative}")
            continue
        listed.add(relative)
        actual = digest(path)
        if asset.get("sha256") != actual:
            fail(errors, f"asset digest mismatch: {relative}")
        if asset.get("public_safe") is not True or asset.get("approval") != "human":
            fail(errors, f"asset not human-approved: {relative}")
        for label, value in (("title", asset.get("title", "")), ("source", asset.get("source", ""))):
            if any(pattern.search(str(value)) for pattern in BLOCKED_PATTERNS):
                fail(errors, f"unsafe asset {label}: {relative}")

    for path in ASSET_ROOT.rglob("*"):
        if path.is_file():
            relative = path.relative_to(ASSET_ROOT).as_posix()
            if relative not in listed:
                fail(errors, f"unlisted asset: {relative}")

    if args.public:
        if manifest.get("state", {}).get("visibility") != "public":
            fail(errors, "public validation requires visibility=public")
        if any(entry.get("required_for_public") and entry.get("health") != "pass" for entry in entries):
            fail(errors, "required public entry has not passed health readback")
        if manifest.get("verification", {}).get("current_result") != "pass":
            fail(errors, "manifest current_result must be pass before public")

    if args.links:
        for entry in entries:
            if not args.public and not entry.get("required_for_public"):
                continue
            request = urllib.request.Request(entry["url"], method="HEAD", headers={"User-Agent": "OpsKeeper-ModelScope-Preflight/1.0"})
            try:
                with urllib.request.urlopen(request, timeout=15) as response:
                    status = response.status
            except Exception:
                try:
                    request = urllib.request.Request(entry["url"], headers={"User-Agent": "OpsKeeper-ModelScope-Preflight/1.0"})
                    with urllib.request.urlopen(request, timeout=15) as response:
                        status = response.status
                except Exception as exc:
                    fail(errors, f"link failed {entry['id']}: {exc}")
                    continue
            if status < 200 or status >= 400:
                fail(errors, f"link failed {entry['id']}: HTTP {status}")

    if errors:
        print("\n".join(f"FAIL: {error}" for error in errors))
        return 1
    print("PASS: ModelScope submission manifest and assets")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 3: Run the validator**

Run: `python3 deliverables/modelscope/checks/validate.py`

Expected: `PASS: ModelScope submission manifest and assets`.

- [ ] **Step 4: Commit**

```bash
git add deliverables/modelscope/submission.json deliverables/modelscope/checks/validate.py openspec/changes/modelscope-final-showcase-hosting/tasks.md
git commit -m "chore(modelscope): add submission manifest validator"
```

### Task 2: Curate the initial public-safe asset set

**Files:**
- Create: `deliverables/modelscope/assets/evidence/README.md`
- Copy: approved existing public images from `docs/assets/`
- Modify: `deliverables/modelscope/submission.json`

**Interfaces:**
- Consumes: existing public repository images that are already intended for documentation.
- Produces: eight allowlisted files with stable relative paths and SHA-256 values in `assets`.

- [ ] **Step 1: Select only already-public evidence**

Use these eight existing assets:

```bash
mkdir -p deliverables/modelscope/assets/evidence
cp docs/assets/demo.gif deliverables/modelscope/assets/evidence/opskeeper-overview.gif
cp docs/assets/readme-tour/user-20260707-monitor.png deliverables/modelscope/assets/evidence/monitor.png
cp docs/assets/readme-tour/user-20260707-rca-session.png deliverables/modelscope/assets/evidence/rca-session.png
cp docs/assets/readme-tour/user-20260707-workflow-editor.png deliverables/modelscope/assets/evidence/workflow-editor.png
cp docs/assets/readme-tour/user-20260707-topology-map.png deliverables/modelscope/assets/evidence/topology-map.png
cp docs/assets/readme-tour/user-20260707-artifacts-pages.png deliverables/modelscope/assets/evidence/artifacts.png
cp docs/assets/readme-tour/user-20260707-agent-write-gate.png deliverables/modelscope/assets/evidence/write-gate.png
cp docs/assets/readme-tour/user-20260707-knowledge-vault.png deliverables/modelscope/assets/evidence/knowledge-vault.png
```

Each inventory entry must use:

```json
{
  "id": "monitor",
  "title": "OpsKeeper monitoring view",
  "path": "evidence/monitor.png",
  "type": "image/png",
  "source": "docs/assets/readme-tour/user-20260707-monitor.png",
  "purpose": "Show monitoring and alert context",
  "authorization": "Apache-2.0 project documentation asset",
  "public_safe": true,
  "approval": "human",
}
```

- [ ] **Step 2: Generate digests and update all eight entries**

Run this exact inventory synchronization script so digests are derived from files rather than typed manually:

```bash
python3 - <<'PY'
import hashlib
import json
from pathlib import Path

root = Path('deliverables/modelscope')
manifest_path = root / 'submission.json'
assets = [
    ('opskeeper-overview', 'OpsKeeper overview animation', 'evidence/opskeeper-overview.gif', 'image/gif', 'docs/assets/demo.gif', 'Show the end-to-end product journey'),
    ('monitor', 'OpsKeeper monitoring view', 'evidence/monitor.png', 'image/png', 'docs/assets/readme-tour/user-20260707-monitor.png', 'Show monitoring and alert context'),
    ('rca-session', 'Root-cause analysis session', 'evidence/rca-session.png', 'image/png', 'docs/assets/readme-tour/user-20260707-rca-session.png', 'Show diagnosis evidence'),
    ('workflow-editor', 'Workflow editor', 'evidence/workflow-editor.png', 'image/png', 'docs/assets/readme-tour/user-20260707-workflow-editor.png', 'Show operational workflow composition'),
    ('topology-map', 'Topology map', 'evidence/topology-map.png', 'image/png', 'docs/assets/readme-tour/user-20260707-topology-map.png', 'Show system relationships'),
    ('artifacts', 'Artifact pages', 'evidence/artifacts.png', 'image/png', 'docs/assets/readme-tour/user-20260707-artifacts-pages.png', 'Show execution artifacts'),
    ('write-gate', 'Agent write gate', 'evidence/write-gate.png', 'image/png', 'docs/assets/readme-tour/user-20260707-agent-write-gate.png', 'Show human approval boundary'),
    ('knowledge-vault', 'Knowledge vault', 'evidence/knowledge-vault.png', 'image/png', 'docs/assets/readme-tour/user-20260707-knowledge-vault.png', 'Show operational knowledge retention'),
]
manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
manifest['assets'] = []
for asset_id, title, relative, media_type, source, purpose in assets:
    path = root / 'assets' / relative
    manifest['assets'].append({
        'id': asset_id,
        'title': title,
        'path': relative,
        'type': media_type,
        'source': source,
        'purpose': purpose,
        'authorization': 'Apache-2.0 project documentation asset',
        'public_safe': True,
        'approval': 'human',
        'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
    })
manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
PY
```

- [ ] **Step 3: Add the human safety statement**

Create `assets/evidence/README.md` with:

```markdown
# Public evidence assets

These files are copied only from already-public OpsKeeper documentation. They must not be edited to add private endpoints, credentials, customer-like data, or internal task identifiers. A human reviewer must re-confirm every file before public visibility changes.
```

- [ ] **Step 4: Validate asset integrity**

Run: `python3 deliverables/modelscope/checks/validate.py`

Expected: PASS, including rejection of any unlisted asset.

- [ ] **Step 5: Commit**

```bash
git add deliverables/modelscope
git commit -m "chore(modelscope): add public-safe evidence assets"
```

### Task 3: Creative Space Gradio showcase

**Files:**
- Create: `deliverables/modelscope/creative-space/app.py`
- Create: `deliverables/modelscope/creative-space/content/zh.md`
- Create: `deliverables/modelscope/creative-space/requirements.txt`
- Create: `deliverables/modelscope/creative-space/README.md`

**Interfaces:**
- Consumes: assets at `../assets/evidence/*`.
- Produces: a Gradio web app launched by `python app.py`; all external links use target `_blank` and `rel="noopener noreferrer"`.

- [ ] **Step 1: Add the runtime declaration**

`requirements.txt`:

```text
gradio==5.49.1
```

- [ ] **Step 2: Add Chinese showcase content**

Structure `content/zh.md` with these exact sections:

```markdown
# OpsKeeper · Agent 原生运维工作台

## 场景与挑战
告警风暴、多系统割裂和人工执行风险拖慢根因定位，事故证据也难沉淀复用。

## 解决方案与价值
OpsKeeper 以插件方式接入 AgentTeams，由 Manager 协调诊断、预演、修复与验证角色；正式恢复必须经过人工审批，操作对象、候选方案和执行凭据逐项核对。

## 安全边界
本创空间只做公开展示，不运行特权运维环境，不保存凭据，也不代理 OpsKeeper、Matrix 或 AgentTeams。所有在线入口均在新窗口打开并保留原有认证。

## 能力表述
修复预演在独立 preview-pg 中执行固定负载重放，对比结果一致性、查询延迟和写入影响。通过预演只代表可进入人工审批，不代表自动执行；失败候选会被拒绝。当前不声明 PolarDB HA，也不声明复制原实例活动会话。
```

Then add architecture, evidence, quick-start, and entry-card Markdown using the five authoritative URLs and GitHub.

- [ ] **Step 3: Implement the rendering app**

Use one file and no privileged client:

```python
from pathlib import Path

import gradio as gr

ROOT = Path(__file__).resolve().parent
CONTENT = (ROOT / "content" / "zh.md").read_text(encoding="utf-8")
ASSETS = ROOT.parent / "assets" / "evidence"

EXTERNAL_LINKS = """
<div class="entry-grid">
  <a href="https://github.com/vincent-wuhan/opskeeper" target="_blank" rel="noopener noreferrer">GitHub 源码</a>
  <a href="https://opskeeper.yueming.xin/home" target="_blank" rel="noopener noreferrer">产品官网</a>
  <a href="https://opskeeper.yueming.xin" target="_blank" rel="noopener noreferrer">OpsKeeper 服务</a>
  <a href="https://rooms.yueming.xin" target="_blank" rel="noopener noreferrer">AgentTeams Rooms</a>
  <a href="https://teams.yueming.xin" target="_blank" rel="noopener noreferrer">AgentTeams Dashboard</a>
  <a href="https://home.yueming.xin" target="_blank" rel="noopener noreferrer">路演全流程控制台</a>
</div>
"""

css = """
.entry-grid {display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:12px;margin:16px 0}
.entry-grid a {display:block;padding:14px;border:1px solid #d9dde6;border-radius:12px;color:#1652f0;text-decoration:none;font-weight:600}
.entry-grid a:hover {border-color:#1652f0;background:#f4f7ff}
"""

with gr.Blocks(title="OpsKeeper · Agent 原生运维工作台", theme=gr.themes.Base(), css=css) as demo:
    gr.Markdown(CONTENT)
    gr.HTML(EXTERNAL_LINKS)
    with gr.Tabs():
        with gr.Tab("总览"):
            gr.Image(str(ASSETS / "opskeeper-overview.gif"), label="OpsKeeper overview", show_label=False)
        with gr.Tab("监控与根因"):
            gr.Gallery([str(ASSETS / "monitor.png"), str(ASSETS / "rca-session.png")], label="Monitoring and RCA", columns=2)
        with gr.Tab("编排与拓扑"):
            gr.Gallery([str(ASSETS / "workflow-editor.png"), str(ASSETS / "topology-map.png")], label="Workflow and topology", columns=2)
        with gr.Tab("审批与档案"):
            gr.Gallery([str(ASSETS / "write-gate.png"), str(ASSETS / "artifacts.png"), str(ASSETS / "knowledge-vault.png")], label="Approval and archive", columns=2)

if __name__ == "__main__":
    demo.launch(server_name="0.0.0.0", server_port=7860, show_api=False)
```

- [ ] **Step 4: Add local launch instructions**

`README.md` must state:

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python app.py
```

- [ ] **Step 5: Validate syntax and dependency declaration**

Run: `python3 -m py_compile deliverables/modelscope/creative-space/app.py`

Expected: no output and exit code 0.

- [ ] **Step 6: Commit**

```bash
git add deliverables/modelscope/creative-space
git commit -m "feat(modelscope): add creative space showcase"
```

### Task 4: Source baseline and route preflight report

**Files:**
- Create: `deliverables/modelscope/checks/preflight.py`
- Create: `deliverables/modelscope/reports/baseline.md`

**Interfaces:**
- Consumes: `submission.json`, current git revision, optional remote refs.
- Produces: `reports/baseline.json` and human-readable `baseline.md` with UTC and Beijing time.

- [ ] **Step 1: Implement preflight**

The script must:

```python
1. Read submission.json.
2. Run git rev-parse HEAD and git rev-parse origin/main when available.
3. Probe each URL with HEAD and GET fallback.
4. Classify 2xx/3xx as pass, 401/403 as auth-protected but reachable, and other statuses as fail.
5. Write reports/baseline.json and reports/baseline.md.
6. Exit nonzero only when a required public entry is unreachable.
```

Use only Python standard library plus `subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT)` and `subprocess.run(["git", "rev-parse", "origin/main"], cwd=REPO_ROOT)`.

- [ ] **Step 2: Record current status without hiding failures**

Run: `python3 deliverables/modelscope/checks/preflight.py`

Expected at this stage: the command may fail because `/home` is 404 and `home.yueming.xin` is 502. The report must record those exact statuses.

- [ ] **Step 3: Commit**

```bash
git add deliverables/modelscope/checks/preflight.py deliverables/modelscope/reports
git commit -m "chore(modelscope): add baseline preflight"
```

### Task 5: Website ModelScope entry after Creative Space URL exists

**Files:**
- Modify: `site/lib/site.ts`
- Modify: `site/lib/site-zh.ts`
- Modify: the English and Chinese navigation/footer component that already renders ecosystem links

**Interfaces:**
- Consumes: final Creative Space URL from `submission.json`.
- Produces: `SITE.modelScopeUrl` and localized label; render only when the URL is non-empty.

- [ ] **Step 1: Gate on a real submission URL**

Do not add a fake URL. If `submissions.creative_space.url` is null, stop this task and leave it open.

- [ ] **Step 2: Add localized data**

```ts
Copy the exact `submissions.creative_space.url` value from `submission.json` into both localized site constants.
```

- [ ] **Step 3: Render a new-window link**

Use `target="_blank"` and `rel="noopener noreferrer"`; label it `ModelScope Showcase` in English and `魔搭创空间` in Chinese.

- [ ] **Step 4: Validate website**

Run:

```bash
cd site
pnpm typecheck
pnpm build
```

Expected: both pass.

- [ ] **Step 5: Commit**

```bash
git add site
git commit -m "feat(site): link ModelScope showcase"
```

### Task 6: Authoritative source, release, and ModelScope mirror

**Files:**
- Modify: `deliverables/modelscope/submission.json`
- Create: `deliverables/modelscope/reports/source-readback.md`

**Interfaces:**
- Consumes: validated local final baseline and GitHub remote.
- Produces: GitHub `main` commit, release tag, ModelScope code URL, and mirror readback.

- [ ] **Step 1: Confirm baseline**

Record local HEAD, GitHub `main`, public demo revision, TeamHarness version, and release tag. Do not infer values.

- [ ] **Step 2: Merge and release**

Only after final E2E passes, merge the validated branch to `main`, push, and create the agreed tag. Record exact commands and readback output in `source-readback.md`.

- [ ] **Step 3: Import or synchronize code**

Use ModelScope's supported repository import/sync flow from the same GitHub commit. Record the ModelScope commit readback.

- [ ] **Step 4: Commit submission records**

```bash
git add deliverables/modelscope
git commit -m "chore(modelscope): record source and mirror readback"
```

### Task 7: Private submission and public switch

**Files:**
- Modify: `deliverables/modelscope/submission.json`
- Create: `deliverables/modelscope/reports/private-readback.md`
- Create: `deliverables/modelscope/reports/public-readback.md`

**Interfaces:**
- Consumes: final Creative Space, code mirror, and project-practice URLs.
- Produces: auditable private/public visibility and link/media readback.

- [ ] **Step 1: Save all three submissions privately**

Submit Creative Space, code repository, and project-practice content. Do not create a model submission.

- [ ] **Step 2: Run private readback**

Run:

```bash
python3 deliverables/modelscope/checks/validate.py --links
python3 deliverables/modelscope/checks/preflight.py
```

Record fields, code visibility, media playback, external links, desktop/mobile layout, and current time in both UTC and Beijing time.

- [ ] **Step 3: Block or hide failed optional entries**

A required failed URL blocks public switch. `home.yueming.xin` may only be exposed after it returns the full-flow console.

- [ ] **Step 4: Switch public and repeat checks**

Run `validate.py --links --public`, record public readback, and verify rollback instructions remain accurate.

- [ ] **Step 5: Commit**

```bash
git add deliverables/modelscope
git commit -m "chore(modelscope): record submission readback"
```

### Task 8: Final task reconciliation

**Files:**
- Modify: `openspec/changes/modelscope-final-showcase-hosting/tasks.md`

**Interfaces:**
- Consumes: all implementation and readback artifacts.
- Produces: every completed OpenSpec task checked; unfinished manual/online tasks remain unchecked rather than being falsely marked complete.

- [ ] **Step 1: Map artifacts to tasks**

Check only tasks with concrete evidence. Keep online tasks open until their readback files exist.

- [ ] **Step 2: Re-run local checks**

```bash
python3 deliverables/modelscope/checks/validate.py
python3 -m py_compile deliverables/modelscope/creative-space/app.py
```

- [ ] **Step 3: Commit**

```bash
git add openspec/changes/modelscope-final-showcase-hosting/tasks.md
git commit -m "chore(modelscope): reconcile final showcase tasks"
```
