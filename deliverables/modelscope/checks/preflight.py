#!/usr/bin/env python3
"""Record the source revision baseline and probe public entry routes."""

from __future__ import annotations

import json
import subprocess
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from zoneinfo import ZoneInfo


SCHEMA_VERSION = 1
USER_AGENT = "OpsKeeper-ModelScope-Preflight/1.0"
REQUEST_TIMEOUT_SECONDS = 15

REPO_ROOT = Path(__file__).resolve().parents[3]
SUBMISSION_PATH = REPO_ROOT / "deliverables" / "modelscope" / "submission.json"
REPORTS_DIR = REPO_ROOT / "deliverables" / "modelscope" / "reports"
JSON_REPORT_PATH = REPORTS_DIR / "baseline.json"
MARKDOWN_REPORT_PATH = REPORTS_DIR / "baseline.md"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def load_submission() -> dict:
    with SUBMISSION_PATH.open("r", encoding="utf-8") as submission_file:
        return json.load(submission_file)


def git_revision(reference: str, required: bool) -> str | None:
    completed = subprocess.run(
        ["git", "rev-parse", reference],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    if completed.returncode == 0:
        return completed.stdout.strip()
    if required:
        raise RuntimeError(f"unable to read git {reference}: {completed.stderr.strip()}")
    return None


def probe_once(url: str, method: str) -> dict:
    request = urllib.request.Request(
        url,
        method=method,
        headers={"User-Agent": USER_AGENT, "Accept": "*/*"},
    )
    opener = urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
            status = int(response.status)
            error = None
    except urllib.error.HTTPError as exc:
        status = int(exc.code)
        error = None
        exc.close()
    except Exception as exc:
        status = None
        error = f"{type(exc).__name__}: {exc}"

    if status is None:
        outcome = "fail"
        reachable = False
    elif 200 <= status < 400:
        outcome = "pass"
        reachable = True
    elif status in (401, 403):
        outcome = "auth_protected"
        reachable = True
    else:
        outcome = "fail"
        reachable = False

    return {
        "method": method,
        "status": status,
        "error": error,
        "outcome": outcome,
        "reachable": reachable,
    }


def probe_entry(entry: dict) -> dict:
    attempts = [probe_once(entry["url"], "HEAD")]
    if attempts[0]["outcome"] == "fail":
        attempts.append(probe_once(entry["url"], "GET"))

    final_attempt = attempts[-1]
    return {
        "id": entry["id"],
        "url": entry["url"],
        "role": entry["role"],
        "required_for_public": entry["required_for_public"],
        "auth_expected": entry["auth_expected"],
        "result": final_attempt["outcome"],
        "reachable": final_attempt["reachable"],
        "effective_method": final_attempt["method"],
        "attempts": attempts,
    }


def attempt_label(attempt: dict) -> str:
    status = str(attempt["status"]) if attempt["status"] is not None else "network error"
    detail = f", {attempt['error']}" if attempt["error"] else ""
    return f"{attempt['method']} {status}{detail}"


def result_label(result: str) -> str:
    return {
        "pass": "PASS",
        "auth_protected": "AUTH PROTECTED",
        "fail": "FAIL",
    }[result]


def markdown_escape(value: object) -> str:
    return str(value).replace("|", "\\|").replace("\n", " ")


def build_markdown(report: dict) -> str:
    generated = report["generated_at"]
    source = report["source"]
    summary = report["summary"]
    lines = [
        "# ModelScope Source and Route Baseline",
        "",
        f"- Generated (UTC): `{generated['utc']}`",
        f"- Generated (Asia/Shanghai): `{generated['asia_shanghai']}`",
        f"- GitHub repository: {source['github_repository']}",
        f"- Default branch: `{source['default_branch']}`",
        f"- Current HEAD: `{source['head_commit']}`",
    ]
    if source["origin_main_available"]:
        lines.append(f"- `origin/main`: `{source['origin_main']}`")
    else:
        lines.append("- `origin/main`: unavailable")

    lines.extend(
        [
            "",
            "## Route Preflight",
            "",
            "HEAD is used first. GET is the fallback when HEAD is unreachable or returns a failing status. Redirects are recorded as redirect statuses rather than followed.",
            "",
            "| Entry | Result | Reachable | Effective method | Probe | Route |",
            "|---|---|---:|---|---|---|",
        ]
    )
    for entry in report["entries"]:
        lines.append(
            "| {id} | {result} | {reachable} | {method} | {probe} | {url} |".format(
                id=markdown_escape(entry["id"]),
                result=result_label(entry["result"]),
                reachable="yes" if entry["reachable"] else "no",
                method=entry["effective_method"],
                probe=markdown_escape("; ".join(attempt_label(item) for item in entry["attempts"])),
                url=markdown_escape(entry["url"]),
            )
        )

    result = "PASS" if summary["required_public_unreachable"] == 0 else "FAIL"
    lines.extend(
        [
            "",
            "## Summary",
            "",
            f"- Total entries: {summary['total']}",
            f"- Pass: {summary['pass']}",
            f"- Auth-protected and reachable: {summary['auth_protected']}",
            f"- Fail: {summary['fail']}",
            f"- Required public entries unreachable: {summary['required_public_unreachable']}",
            f"- Preflight result: {result}",
            f"- Exit code: {summary['exit_code']}",
            "",
            "Auth-protected (`401` or `403`) routes are considered reachable. HTTP `2xx` and `3xx` statuses pass. All other HTTP statuses and network errors fail.",
            "",
        ]
    )
    return "\n".join(lines)


def write_reports(report: dict) -> None:
    REPORTS_DIR.mkdir(parents=True, exist_ok=True)
    JSON_REPORT_PATH.write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    MARKDOWN_REPORT_PATH.write_text(build_markdown(report), encoding="utf-8")


def main() -> int:
    submission = load_submission()
    origin_main = git_revision("origin/main", required=False)
    now_utc = datetime.now(timezone.utc)
    now_shanghai = now_utc.astimezone(ZoneInfo("Asia/Shanghai"))
    entries = [probe_entry(entry) for entry in submission["entries"]]
    result_counts = {
        "pass": sum(entry["result"] == "pass" for entry in entries),
        "auth_protected": sum(
            entry["result"] == "auth_protected" for entry in entries
        ),
        "fail": sum(entry["result"] == "fail" for entry in entries),
    }
    required_public_unreachable = sum(
        entry["required_for_public"] and not entry["reachable"] for entry in entries
    )
    exit_code = 1 if required_public_unreachable else 0
    report = {
        "schema_version": SCHEMA_VERSION,
        "generated_at": {
            "utc": now_utc.isoformat(timespec="seconds").replace("+00:00", "Z"),
            "asia_shanghai": now_shanghai.isoformat(timespec="seconds"),
        },
        "source": {
            "github_repository": submission["source"]["github_repository"],
            "default_branch": submission["source"]["default_branch"],
            "head_commit": git_revision("HEAD", required=True),
            "origin_main": origin_main,
            "origin_main_available": origin_main is not None,
        },
        "probe": {
            "timeout_seconds": REQUEST_TIMEOUT_SECONDS,
            "user_agent": USER_AGENT,
            "strategy": "HEAD with GET fallback",
            "redirects_followed": False,
        },
        "entries": entries,
        "summary": {
            "total": len(entries),
            **result_counts,
            "required_public_unreachable": required_public_unreachable,
            "exit_code": exit_code,
        },
    }
    write_reports(report)

    for entry in entries:
        print(
            f"{entry['id']}: {result_label(entry['result'])} "
            f"({'; '.join(attempt_label(attempt) for attempt in entry['attempts'])})"
        )
    print(
        f"Preflight result: {'FAIL' if exit_code else 'PASS'} "
        f"({required_public_unreachable} required public entries unreachable)"
    )
    print(f"Reports written: {JSON_REPORT_PATH} and {MARKDOWN_REPORT_PATH}")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
