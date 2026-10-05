#!/usr/bin/env bash
# Focused tests for deploy/install/state-dirs.sh and the two scripts that use it.
#
# Why this exists: the host data directories are the one thing in the install
# path where getting it wrong produces a stack that looks healthy and cannot
# write. A missing chown is not an error — `|| true` hides it, `docker compose
# up` then creates the directory as root, and the nonroot process fails later
# at "mkdir page dir: permission denied" or worse, silently forgets a
# federation membership and re-enrols every cluster. Nothing in the install
# output says so.
#
# These tests run entirely in a sandbox: no docker, no root, no network. chown
# is intercepted rather than performed, because the test needs to assert the
# ORDER of the three steps and only a fake can observe it — a real chown of a
# directory that does not exist fails, and the failure is exactly what the
# production code swallows.
#
# Run: bash scripts/test-install-state-dirs.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIRS="${REPO_ROOT}/deploy/install/state-dirs.sh"
INSTALL_SH="${REPO_ROOT}/deploy/install/install.sh"
UPGRADE_SH="${REPO_ROOT}/deploy/install/upgrade.sh"
PACKAGE_SH="${REPO_ROOT}/dist/package.sh"

WORK="$(mktemp -d -t statedirs-test-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'PASS: %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

# --- a sandbox where chown and chmod are observable -------------------------
#
# Both record every invocation to a log. chown additionally records whether
# the directory it was handed already existed: that single fact is the whole
# difference between an install that works and one that silently writes
# nothing, and it is invisible from the outside of a real chown.
build_sandbox() {
    mkdir -p "$WORK/bin"
    cat > "$WORK/bin/chown" <<'EOF'
#!/usr/bin/env bash
target="${@: -1}"
if [[ ! -d "$target" ]]; then
    printf 'MISSING %s\n' "$*" >> "$FAKE_CHOWN_LOG"
else
    printf 'OK %s\n' "$*" >> "$FAKE_CHOWN_LOG"
fi
exit 0
EOF
    cat > "$WORK/bin/chmod" <<'EOF'
#!/usr/bin/env bash
printf 'CHMOD %s\n' "$*" >> "$FAKE_CHMOD_LOG"
exit 0
EOF
    chmod +x "$WORK/bin/chown" "$WORK/bin/chmod"
    : > "$WORK/chown.log"
    : > "$WORK/chmod.log"
    export FAKE_CHOWN_LOG="$WORK/chown.log" FAKE_CHMOD_LOG="$WORK/chmod.log"
}

# --- 1. the file parses ------------------------------------------------------
if bash -n "$STATE_DIRS" 2>"$WORK/syntax.log"; then
    pass "state-dirs.sh parses"
else
    fail "state-dirs.sh does not parse: $(cat "$WORK/syntax.log")"
fi

# --- 2. the list itself is well-formed --------------------------------------
# shellcheck source=/dev/null
source "$STATE_DIRS"
LIST="$WORK/list.txt"
opskeeper_state_dirs > "$LIST"

if [[ -s "$LIST" ]]; then
    pass "opskeeper_state_dirs emits a non-empty list"
else
    fail "opskeeper_state_dirs emitted nothing"
fi

bad_rows="$(awk 'NF < 2 || NF > 3 { print NR": "$0 }' "$LIST")"
if [[ -z "$bad_rows" ]]; then
    pass "every row has a directory and a uid (and at most a mode)"
else
    fail "rows with the wrong column count: ${bad_rows}"
fi

bad_uid="$(awk 'NF >= 2 && $2 != "-" && $2 !~ /^[0-9]+:[0-9]+$/ { print NR": "$0 }' "$LIST")"
if [[ -z "$bad_uid" ]]; then
    pass "every uid column is n:n or -"
else
    fail "rows whose uid column is neither n:n nor -: ${bad_uid}"
fi

dupes="$(awk '{ print $1 }' "$LIST" | sort | uniq -d)"
if [[ -z "$dupes" ]]; then
    pass "no directory is listed twice"
else
    fail "directories listed more than once: ${dupes}"
fi

# --- 3. running it creates every directory ----------------------------------
DATA="$WORK/data"
build_sandbox
opskeeper_state_dirs | PATH="$WORK/bin:$PATH" opskeeper_ensure_state_dirs "$DATA"

missing=""
while read -r dir _; do
    [[ -d "$DATA/$dir" ]] || missing+=" $dir"
done < <(opskeeper_state_dirs)
if [[ -z "$missing" ]]; then
    pass "every listed directory exists after a run"
else
    fail "directories not created:${missing}"
fi

# --- 4. THE ONE THAT MATTERS: chown never sees a missing directory ----------
#
# This is the assertion the whole file exists for. Chown runs after mkdir in
# the source, and a fake is the only thing that can tell: a real chown of a
# missing path exits non-zero, the caller swallows that with `|| true`, and
# the install continues into a directory docker will create as root.
ordered_bad="$(grep -c '^MISSING' "$WORK/chown.log" || true)"
if [[ "$ordered_bad" -eq 0 ]]; then
    pass "every chown ran against a directory that already existed"
else
    fail "$ordered_bad chown call(s) ran before the directory existed — mkdir and chown are in the wrong order"
fi

# --- 5. each chown names the right uid --------------------------------------
wrong_uid="$(grep '^OK' "$WORK/chown.log" | while read -r _ rest; do
    dir="${rest##* }"
    want="$(awk -v d="$dir" '$1 == d { print $2 }' "$LIST")"
    case "$rest" in
        *"$want "*) ;;
        *) printf '%s wanted %s\n' "$rest" "$want" ;;
    esac
done)"
if [[ -z "$wrong_uid" ]]; then
    pass "each chown carried the uid the list gives that directory"
else
    fail "chowns with the wrong uid: ${wrong_uid}"
fi

# --- 6. qdrant is created but not chowned -----------------------------------
if grep -q 'qdrant' "$WORK/chown.log"; then
    fail "qdrant was chowned; its container runs as root and the list says -"
else
    pass "qdrant is created without a chown, as the list asks"
fi
if [[ -d "$DATA/qdrant" ]]; then
    pass "qdrant was still created"
else
    fail "qdrant was not created"
fi

# --- 7. the mode column reaches chmod ----------------------------------------
if grep -q '0755' "$WORK/chmod.log" && grep -q 'embeddings' "$WORK/chmod.log"; then
    pass "embeddings' mode reached chmod"
else
    fail "embeddings' 0755 mode never reached chmod (log: $(cat "$WORK/chmod.log"))"
fi

# --- 8. a row with no uid is an error, not a silent skip ---------------------
# A directory created with nobody owning it is the failure this all prevents,
# so a malformed row must stop the install rather than pass through.
if printf 'orphan\n' | PATH="$WORK/bin:$PATH" opskeeper_ensure_state_dirs "$WORK/data2" 2>"$WORK/orphan.log"; then
    fail "a row with no uid column was accepted"
else
    pass "a row with no uid column is refused"
fi

# --- 9. both install paths actually use it -----------------------------------
for script in "$INSTALL_SH" "$UPGRADE_SH"; do
    name="$(basename "$script")"
    if grep -q 'source "\$SCRIPT_DIR/state-dirs.sh"' "$script" \
       && grep -q 'opskeeper_ensure_state_dirs "\$OPSKEEPER_DATA_DIR"' "$script"; then
        pass "$name sources the shared list and runs it"
    else
        fail "$name does not source and run the shared list — it may still carry its own copy of the directories"
    fi
    # The old shape was a `mkdir -p` list naming each directory inline. If
    # one is back, the single-source property is gone even though the new
    # call is still there.
    leftovers="$(grep -c 'OPSKEEPER_DATA_DIR/[a-z]' "$script" || true)"
    if [[ "$leftovers" -eq 0 ]]; then
        pass "$name names no data directory inline any more"
    else
        fail "$script still names $leftovers data director(ies) inline — two lists will drift again"
    fi
done

# --- 10. the tarball carries it, or nothing installs -------------------------
# copy_opt warns and continues on a missing source, which is right for an
# optional asset and wrong for a file two scripts `source` under `set -e`.
if grep -q 'die "deploy/install/state-dirs.sh missing' "$PACKAGE_SH"; then
    pass "package.sh treats state-dirs.sh as required, not optional"
else
    fail "package.sh does not fail when state-dirs.sh is absent — a tarball without it cannot install"
fi
if grep 'deploy/install/state-dirs.sh' "$PACKAGE_SH" | grep -q 'copy_opt'; then
    fail "package.sh uses copy_opt (warn-and-continue) for a required file"
else
    pass "package.sh does not use the lenient copy_opt for it"
fi

printf '\n%s\n' "$([[ $fails -eq 0 ]] && echo "all state-dir tests passed" || echo "$fails test(s) failed")"
[[ $fails -eq 0 ]]
