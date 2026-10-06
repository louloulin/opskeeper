#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import re
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
METADATA_PATHS = {
    "CHANGELOG.md",
    "RELEASE_VERSION.json",
    "VERSION",
    "Makefile",
    "scripts/audit_open_source.py",
    "scripts/check_release_version.py",
    "scripts/deterministic_archive.py",
    "scripts/fetch_onnxruntime.sh",
    "docs/OPEN_SOURCE_GATE.md",
    "docs/PROVENANCE.md",
    "NOTICE.md",
    ".github/workflows/release.yml",
    ".github/workflows/audit-open-source.yml",
}
ALLOWED_RELEASE_DELTA_PREFIXES = {
    "docs/",
    "scripts/",
    "testdata/",
    "tests/",
}


def run(*arguments: str, root: Path = ROOT) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, cwd=root, text=True, capture_output=True, check=False)


class Findings:
    """Every drifted fact, not the first one.

    This gate runs at signing time, which is the worst possible moment for
    fail-fast: the person reading it is holding a release branch, and the
    previous shape made them rediscover the same drift one run at a time --
    fix one, push, read the log, find the next. The open-source gate had this
    exact defect (see tests/test_audit_open_source.py), and here the number of
    findings is not a small constant: a tree that has moved since the manifest
    was signed drifts in the plugin versions, in both source-tree hashes and
    in the release boundary at once, and only the first of those was ever
    printed.

    So a check records what it expected and what it found, and every finding
    is printed together.
    """

    def __init__(self) -> None:
        self.items: list[str] = []

    def require(self, condition: bool, message: str, *, expected: object = None, actual: object = None) -> bool:
        if condition:
            return True
        if expected is None and actual is None:
            self.items.append(message)
        else:
            self.items.append(f"{message} (expected {expected!r}, found {actual!r})")
        return False

    def __len__(self) -> int:
        return len(self.items)


def yaml_metadata_version(content: str) -> str:
    """The version declared inside plugin.yaml's metadata block.

    Raises ValueError rather than exiting: the caller collects findings, so a
    file that cannot be parsed is one finding among the others instead of the
    one that hides them.
    """
    metadata = re.search(r"^metadata:\n(?:[ \t]+.*\n)+", content, re.MULTILINE)
    if metadata is None:
        raise ValueError("TeamHarness plugin metadata block is missing")
    match = re.search(r"^[ \t]+version:[ \t]+['\"]?([^'\"\s#]+)", metadata.group(0), re.MULTILINE)
    if match is None:
        raise ValueError("TeamHarness plugin metadata version is missing")
    return match.group(1)


def commit_exists(commit: str, root: Path = ROOT) -> bool:
    return run("git", "rev-parse", "--verify", f"{commit}^{{commit}}", root=root).returncode == 0


def allowed_release_delta(path: str) -> bool:
    return (
        path in METADATA_PATHS
        or path.startswith(tuple(ALLOWED_RELEASE_DELTA_PREFIXES))
        or path in {"README.md", "README_ZH.md"}
        or path.endswith("_test.go")
    )


def main(root: Path = ROOT) -> int:
    findings = Findings()
    require = findings.require

    manifest = json.loads((root / "RELEASE_VERSION.json").read_text(encoding="utf-8"))
    root_version = (root / "VERSION").read_text(encoding="utf-8").strip()
    plugin_yaml = (root / "plugins/opskeeper-teamharness/plugin.yaml").read_text(encoding="utf-8")
    plugin_json = json.loads(
        (root / "plugins/opskeeper-teamharness/dashboard/public/plugin.json").read_text(encoding="utf-8")
    )
    installer_json = json.loads(
        (root / "plugins/agentteams-plugin-installer/dashboard/public/plugin.json").read_text(encoding="utf-8")
    )

    expected_version = "2026.09.14-rc4"
    expected_tag = "v2026.09.14-rc4"
    require(manifest["version"] == expected_version, "manifest version drifted", expected=expected_version, actual=manifest["version"])
    require(manifest["release_tag"] == expected_tag, "manifest release tag drifted", expected=expected_tag, actual=manifest["release_tag"])
    require(manifest["release_candidate"] is True, "manifest release candidate flag drifted", expected=True, actual=manifest["release_candidate"])
    expected_baseline = "release/20260922@d2920895363edca87e34ee00fcb33eb1c6090723"
    require(
        manifest["release_baseline_ref"] == expected_baseline,
        "manifest main baseline drifted",
        expected=expected_baseline,
        actual=manifest["release_baseline_ref"],
    )
    require(
        manifest["release_branch_head_at_signing"] == manifest["backend_commit"],
        "manifest signing base drifted",
        expected=manifest["backend_commit"],
        actual=manifest["release_branch_head_at_signing"],
    )
    require(root_version == expected_tag, "VERSION drifted from the release tag", expected=expected_tag, actual=root_version)
    changelog = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    require(f"## {expected_version} — 2026-09-14" in changelog, "release changelog entry is missing")
    require(manifest["backend_commit"] in changelog, "release changelog backend binding is missing")
    require(manifest["teamharness_version"] in changelog, "release changelog plugin binding is missing")
    require(re.fullmatch(r"[0-9a-f]{40}", manifest["backend_commit"]) is not None, "backend commit is invalid", actual=manifest["backend_commit"])
    require(manifest["repository"] == "https://github.com/vincent-wuhan/opskeeper", "manifest repository drifted", expected="https://github.com/vincent-wuhan/opskeeper", actual=manifest["repository"])
    require(manifest["license"] == "Apache-2.0", "manifest license drifted", expected="Apache-2.0", actual=manifest["license"])

    try:
        harness_version = yaml_metadata_version(plugin_yaml)
    except ValueError as error:
        require(False, str(error))
        harness_version = None
    if harness_version is not None:
        require(harness_version == manifest["teamharness_version"], "plugin.yaml version drifted", expected=manifest["teamharness_version"], actual=harness_version)
    require(plugin_json["version"] == manifest["teamharness_version"], "dashboard plugin version drifted", expected=manifest["teamharness_version"], actual=plugin_json["version"])
    require(installer_json["version"] == manifest["installer_version"], "installer plugin version drifted", expected=manifest["installer_version"], actual=installer_json["version"])
    require(
        plugin_json["entry"]["dashboard"] == f"dist/main-{harness_version}.js",
        "dashboard entry drifted",
        expected=f"dist/main-{harness_version}.js",
        actual=plugin_json["entry"]["dashboard"],
    )

    web_tree = run("git", "rev-parse", "HEAD:web", root=root).stdout.strip()
    harness_tree = run("git", "rev-parse", "HEAD:plugins/opskeeper-teamharness", root=root).stdout.strip()
    require(web_tree == manifest["web_hash"], "web source tree hash drifted", expected=manifest["web_hash"], actual=web_tree)
    require(manifest.get("teamharness_source_tree") == harness_tree, "TeamHarness source tree hash drifted", expected=harness_tree, actual=manifest.get("teamharness_source_tree"))

    backend_commit = manifest["backend_commit"]
    if commit_exists(backend_commit, root=root):
        ancestry = run("git", "merge-base", "--is-ancestor", backend_commit, "HEAD", root=root).returncode == 0
        require(ancestry, "release commit is not descended from backend_commit", actual=backend_commit)
        changed = run("git", "diff", "--name-only", backend_commit, "HEAD", root=root).stdout.splitlines()
        outside = sorted(path for path in changed if not allowed_release_delta(path))
        require(
            not outside,
            "release commit contains changes outside its source boundary",
            expected="only release metadata, docs, tests and _test.go may differ",
            actual=f"{len(outside)} of {len(changed)} paths, first: {outside[0] if outside else '-'}",
        )
        if outside:
            for path in outside[:20]:
                print(f"  outside source boundary: {path}")
            if len(outside) > 20:
                print(f"  ... and {len(outside) - 20} more")
    elif os.environ.get("OPSKEEPER_REQUIRE_FULL_HISTORY") == "1":
        raise SystemExit("release version check failed: full backend history is required")

    if findings:
        print(f"release version check failed: {len(findings)} finding(s)")
        for item in findings.items:
            print(f"  - {item}")
        return 1

    print("release version check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
