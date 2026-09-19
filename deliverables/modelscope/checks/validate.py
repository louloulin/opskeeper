#!/usr/bin/env python3

import argparse
import hashlib
import json
import re
import sys
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MANIFEST_PATH = ROOT / "submission.json"
ASSET_ROOT = ROOT / "assets"
REQUIRED_TOP_LEVEL = {
    "schema_version",
    "state",
    "source",
    "entries",
    "submissions",
    "assets",
    "verification",
    "rollback",
}
REQUIRED_ENTRY_IDS = {
    "official_website",
    "opskeeper_service",
    "agentteams_rooms",
    "agentteams_dashboard",
    "roadshow_console",
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
        for label, value in (
            ("title", asset.get("title", "")),
            ("source", asset.get("source", "")),
        ):
            if any(pattern.search(str(value)) for pattern in BLOCKED_PATTERNS):
                fail(errors, f"unsafe asset {label}: {relative}")

    if ASSET_ROOT.exists():
        for path in ASSET_ROOT.rglob("*"):
            if path.is_file():
                relative = path.relative_to(ASSET_ROOT).as_posix()
                if relative not in listed:
                    fail(errors, f"unlisted asset: {relative}")

    if args.public:
        if manifest.get("state", {}).get("visibility") != "public":
            fail(errors, "public validation requires visibility=public")
        if any(
            entry.get("required_for_public") and entry.get("health") != "pass"
            for entry in entries
        ):
            fail(errors, "required public entry has not passed health readback")
        if manifest.get("verification", {}).get("current_result") != "pass":
            fail(errors, "manifest current_result must be pass before public")

    if args.links:
        for entry in entries:
            if not args.public and not entry.get("required_for_public"):
                continue
            request = urllib.request.Request(
                entry["url"],
                method="HEAD",
                headers={"User-Agent": "OpsKeeper-ModelScope-Preflight/1.0"},
            )
            try:
                with urllib.request.urlopen(request, timeout=15) as response:
                    status = response.status
            except Exception:
                try:
                    request = urllib.request.Request(
                        entry["url"],
                        headers={"User-Agent": "OpsKeeper-ModelScope-Preflight/1.0"},
                    )
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
