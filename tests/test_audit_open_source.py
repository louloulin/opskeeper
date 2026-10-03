"""Tests for the open-source release gate.

The gate had no tests, which is how two defects in it survived: it stopped at
the first violation, and it scanned the developer's working tree rather than
the commit a release is cut from. Both are invisible to a person reading a
red log -- a fail-fast gate reports one finding, and a tree scan fails on a
machine for a file that a release would never contain.

These tests build a throwaway repository, put material in it, and read the
gate's verdict, because the only honest way to test a gate is to feed it
something it should reject.
"""
from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path

import pytest

AUDITOR_REL = Path("scripts/audit_open_source.py")
REPO_ROOT = Path(__file__).resolve().parents[1]


def load_auditor(root: Path):
    """A fresh copy of the auditor, pointed at `root`.

    The module resolves its own root at import time and caches the tracked
    file set, so each case needs its own instance rather than a patched
    global.
    """
    spec = importlib.util.spec_from_file_location("audit_open_source", REPO_ROOT / AUDITOR_REL)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.ROOT = root
    module.AUDITOR = AUDITOR_REL
    module.tracked_files.cache_clear()
    module.VIOLATIONS.clear()
    return module


def git(root: Path, *args: str) -> None:
    subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)


def scaffold(root: Path, *, commit: bool = True) -> None:
    """A repository the auditor can accept apart from what a case adds.

    The required files are real because check_required_files insists, and
    ACKNOWLEDGMENTS is real because check_acknowledgments insists on three
    strings in it. A test that failed on the scaffolding instead of on the
    material it planted would be worthless.
    """
    (root / "scripts").mkdir(parents=True, exist_ok=True)
    (root / AUDITOR_REL).write_text("# stand-in for the auditor\n", encoding="utf-8")
    (root / "LICENSE").write_text("Apache-2.0\n", encoding="utf-8")
    (root / "NOTICE.md").write_text("notice\n", encoding="utf-8")
    (root / "TRADEMARK.md").write_text("trademark\n", encoding="utf-8")
    (root / "RELEASE_VERSION.json").write_text(
        '{"repository": "https://github.com/vincent-wuhan/opskeeper", "license": "Apache-2.0"}\n',
        encoding="utf-8",
    )
    (root / "README.md").write_text("readme\n", encoding="utf-8")
    acknowledgments = root / "docs" / "ACKNOWLEDGMENTS.md"
    acknowledgments.parent.mkdir(parents=True, exist_ok=True)
    acknowledgments.write_text(
        "GoAI AgentTeams\nAgentTeams Dashboard\nOnGrid\nnot claims of code derivation\n",
        encoding="utf-8",
    )
    (root / "docs" / "OPEN_SOURCE_GATE.md").write_text("gate\n", encoding="utf-8")
    git(root, "init", "-q")
    # Identity first: a commit with none fails, and it would fail for a
    # reason that has nothing to do with the gate under test.
    git(root, "config", "user.email", "gate@example.invalid")
    git(root, "config", "user.name", "gate")
    if commit:
        git(root, "add", "-A")
        git(root, "commit", "-qm", "scaffold", "--no-gpg-sign")


def run(module) -> tuple[int, str, str]:
    import io
    import contextlib

    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = module.main()
    return code, out.getvalue(), err.getvalue()


def test_a_clean_repository_passes(tmp_path: Path) -> None:
    scaffold(tmp_path)
    code, out, err = run(load_auditor(tmp_path))
    assert code == 0, err
    assert "open-source gate passed" in out
    # The scope is part of the answer: a reader has to know whether the gate
    # judged the commit or the machine, because the two can differ.
    assert "the tracked tree" in out


def test_a_tracked_private_path_is_rejected(tmp_path: Path) -> None:
    scaffold(tmp_path)
    private = tmp_path / "docs" / "superpowers" / "plans" / "plan.md"
    private.parent.mkdir(parents=True)
    private.write_text("plan\n", encoding="utf-8")
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "add", "--no-gpg-sign")

    code, _, err = run(load_auditor(tmp_path))
    assert code == 1
    assert "docs/superpowers/plans/plan.md" in err


def test_an_untracked_private_path_is_not_a_release_violation(tmp_path: Path) -> None:
    """The defect this encodes.

    A private note in a developer's working directory is not in the commit a
    release is cut from. Failing the release gate on it means the gate
    answers differently on two machines for the same commit, and it is the
    same untracked-state dependency decision 164 removed from the test suite.
    """
    scaffold(tmp_path)
    private = tmp_path / "docs" / "superpowers" / "notes.md"
    private.parent.mkdir(parents=True)
    private.write_text("notes\n", encoding="utf-8")

    code, _, err = run(load_auditor(tmp_path))
    assert code == 0, err


def test_every_violation_is_reported_not_just_the_first(tmp_path: Path) -> None:
    scaffold(tmp_path)
    (tmp_path / "docs" / "superpowers").mkdir(parents=True)
    (tmp_path / "docs" / "superpowers" / "a.md").write_text("a\n", encoding="utf-8")
    (tmp_path / "docs" / "deliverables").mkdir(parents=True)
    (tmp_path / "docs" / "deliverables" / "b.md").write_text("b\n", encoding="utf-8")
    leaky = tmp_path / "leaky.md"
    leaky.write_text("token ghp_" + "a" * 30 + "\n", encoding="utf-8")
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "add", "--no-gpg-sign")

    code, _, err = run(load_auditor(tmp_path))
    assert code == 1
    # Three independent violations. A fail-fast gate names one of them and
    # sends the reader back to run it again.
    assert "docs/superpowers/a.md" in err
    assert "docs/deliverables/b.md" in err
    assert "leaky.md" in err
    assert "violation(s)" in err


def test_the_decoy_sentinel_is_exempt_but_a_real_key_is_not(tmp_path: Path) -> None:
    """The exemption has to be narrower than the pattern.

    A test that proves no API key reaches a node has to name the key it
    refuses to pass on. Exempting that one value is defensible; exempting
    every credential-shaped string in every test file would delete the gate.
    """
    scaffold(tmp_path)
    (tmp_path / "decoy.md").write_text(
        "the sentinel is sk-decoy-openai-must-not-reach-a-node\n", encoding="utf-8"
    )
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "decoy", "--no-gpg-sign")
    code, _, err = run(load_auditor(tmp_path))
    assert code == 0, err

    (tmp_path / "tests").mkdir()
    (tmp_path / "tests" / "leak_test.go").write_text(
        'key := "sk-' + "b" * 40 + '"\n', encoding="utf-8"
    )
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "leak", "--no-gpg-sign")
    code, _, err = run(load_auditor(tmp_path))
    assert code == 1
    assert "leak_test.go" in err


def test_a_test_may_name_what_it_is_testing(tmp_path: Path) -> None:
    """The OnGrid rule and the home-path rule exempt test files, nothing else.

    A test that asserts the OnGrid acknowledgment boundary exists has to
    contain the word OnGrid; without the exemption the gate reports the test
    for asserting the gate, which is a way to make people delete the test.
    """
    scaffold(tmp_path)
    (tmp_path / "test_boundary.py").write_text(
        'assert "OnGrid" in open("docs/ACKNOWLEDGMENTS.md").read()\n', encoding="utf-8"
    )
    (tmp_path / "production.md").write_text("built by OnGrid\n", encoding="utf-8")
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "add", "--no-gpg-sign")

    code, _, err = run(load_auditor(tmp_path))
    assert code == 1
    assert "production.md" in err
    assert "test_boundary.py" not in err


def test_the_gates_own_test_may_hold_what_the_gate_looks_for(tmp_path: Path) -> None:
    """The one file that has to contain the literals: the gate's own test.

    It is identified by sharing the auditor's stem, and both spellings count.
    The first version of this exemption matched only the Go spelling
    (`audit_open_source_test`) and therefore missed the Python one
    (`test_audit_open_source`) -- so the gate went on reporting the very file
    that was exempt from it.
    """
    scaffold(tmp_path)
    (tmp_path / "test_audit_open_source.py").write_text(
        'LEAK = "louloulin"\nIP = "8.160.172.235"\n', encoding="utf-8"
    )
    (tmp_path / "audit_open_source_test.py").write_text(
        'LEAK = "louloulin"\n', encoding="utf-8"
    )
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "add", "--no-gpg-sign")
    code, _, err = run(load_auditor(tmp_path))
    assert code == 0, err


def test_an_unrelated_test_file_is_not_exempt(tmp_path: Path) -> None:
    """The exemption covers the gate's own test, not tests in general.

    If it covered every test file, a real key committed beside a unit test
    would ship, and the decoy exemption's whole reason for being narrow would
    be gone.
    """
    scaffold(tmp_path)
    (tmp_path / "test_payment.py").write_text('key = "louloulin"\n', encoding="utf-8")
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "add", "--no-gpg-sign")
    code, _, err = run(load_auditor(tmp_path))
    assert code == 1
    assert "test_payment.py" in err


def test_every_ongrid_allowlist_entry_records_why(tmp_path: Path) -> None:
    """An allowlist entry with no reason is a failure someone silenced.

    The same rule the ledger applies to its own exemption tables applies here:
    the next person to add a path must write down why, or the list stops being
    evidence and becomes a habit.
    """
    module = load_auditor(tmp_path)
    for path, why in module.ONGRID_ALLOWLIST.items():
        assert why.strip(), f"{path} is on the OnGrid allowlist with no reason recorded"


def test_the_repository_ongrid_allowlist_points_at_files_that_exist() -> None:
    """An entry for a file that was renamed or deleted is a hole, not a policy."""
    real = load_auditor(REPO_ROOT)
    for path in real.ONGRID_ALLOWLIST:
        assert (REPO_ROOT / path).is_file(), f"{path} is on the OnGrid allowlist but does not exist"


def test_a_missing_required_file_still_stops_immediately(tmp_path: Path) -> None:
    """Some failures make the rest of the scan meaningless.

    A missing LICENSE is a release that cannot ship at all; continuing would
    produce a content report for a tree that is already disqualified.
    """
    scaffold(tmp_path)
    (tmp_path / "LICENSE").unlink()
    module = load_auditor(tmp_path)
    # fail() raises rather than accumulating, so it surfaces as SystemExit and
    # not as a return code. Asserting on the exception is what keeps the
    # distinction between "stops here" and "joins the list" from eroding.
    with pytest.raises(SystemExit) as raised:
        module.main()
    assert "missing required file: LICENSE" in str(raised.value)


@pytest.mark.parametrize("pattern_fragment", ["louloulin", "/Users/alice/", "8.160.172.235"])
def test_the_three_leaks_the_gate_exists_for(tmp_path: Path, pattern_fragment: str) -> None:
    scaffold(tmp_path)
    (tmp_path / "leak.md").write_text(pattern_fragment + "\n", encoding="utf-8")
    git(tmp_path, "add", "-A")
    git(tmp_path, "commit", "-qm", "leak", "--no-gpg-sign")
    code, _, err = run(load_auditor(tmp_path))
    assert code == 1
    assert "leak.md" in err


if __name__ == "__main__":
    sys.exit(pytest.main([__file__]))


# --- the repository's own private roots -------------------------------------
#
# This one exists because of a mistake made twice in a row, and both times the
# same signal said it had worked: `git rm --cached` plus `git status` plus a
# commit that reported thousands of deletions, while `git ls-tree -r HEAD`
# still listed the files. The pattern in .gitignore was written as
# `/docs/deliverables/` for a directory that lives at the repository root, so
# it never matched and `git add -A` put every one of them back.
#
# The lesson is not "be careful with gitignore". It is that a claim about what
# a release contains has to be checked with a command that reads the release.


def check_ignore(root: Path, path: str) -> str:
    result = subprocess.run(
        ["git", "check-ignore", "-v", "--", path], cwd=root, capture_output=True, text=True
    )
    return result.stdout.strip()


def test_the_private_roots_are_actually_ignored() -> None:
    """A .gitignore line that matches nothing is not a policy.

    `git status` on these paths shows them as untracked, which reads like
    "handled" and is the reason this failed twice: nothing checked that the
    pattern matched.
    """
    roots = ("deliverables/modelscope/ASSETS.md",
             "docs/superpowers/plans/example.md",
             "openspec/changes/example/.comet/handoff/design-context.md")
    for path in roots:
        matched = check_ignore(REPO_ROOT, path)
        assert matched, f".gitignore has no rule that matches {path}"
        assert REPO_ROOT.joinpath(path).exists() or True  # may be absent; the rule is what matters
        # The rule must be the one these paths are excluded by, and it must
        # come from this repository's own file rather than a global one.
        assert matched.startswith(".gitignore:"), f"{path} is excluded by {matched}, not by this repo"


def test_the_private_roots_are_not_in_any_commit() -> None:
    """The check that actually answers the question, and the one that was missing.

    `git ls-tree` reads the commit. Every other signal used in its place reads
    the index or the working directory, and those three disagreed.
    """
    for prefix in ("deliverables/", "docs/superpowers/"):
        listed = subprocess.run(
            ["git", "ls-tree", "-r", "HEAD", "--name-only"],
            cwd=REPO_ROOT, capture_output=True, text=True, check=True,
        ).stdout.splitlines()
        offenders = [p for p in listed if p.startswith(prefix)]
        assert not offenders, f"{len(offenders)} file(s) under {prefix} are in HEAD: {offenders[:3]}"
    listed = subprocess.run(
        ["git", "ls-tree", "-r", "HEAD", "--name-only"],
        cwd=REPO_ROOT, capture_output=True, text=True, check=True,
    ).stdout.splitlines()
    comet = [p for p in listed if "/.comet/" in p]
    assert not comet, f"{len(comet)} .comet file(s) are in HEAD: {comet[:3]}"
