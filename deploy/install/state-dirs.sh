#!/usr/bin/env bash
# The host directories bind-mounted into the stack, and the uid each one's
# container process runs as. install.sh and upgrade.sh both source this; it is
# the only place the list is written down.
#
# It is a script and not a data file because the thing that is easy to get
# wrong is not the list, it is the ORDER of the three steps. A chown of a
# directory that does not exist yet is a no-op that `|| true` hides; the
# directory is then created by `docker compose up` as root — so the mount is
# present, the manifest is right, nothing anywhere errors, and the nonroot
# process cannot write. The only order that produces a working install is
# mkdir, then chown, then chmod, and an install that gets the order wrong is
# indistinguishable from one that has no directories at all.
#
# The list itself was three times over — a mkdir block, a manager chown block
# and a per-service chown block — and the three had already drifted: upgrade.sh
# created pages/workspace/tools and install.sh did not. That drift is the
# whole argument for this file.

# opskeeper_ensure_state_dirs <data-dir> [<conf>]
#
# Creates every directory under <data-dir> and gives it the uid its container
# process runs as, reading the list from <conf> or from stdin when <conf> is
# omitted. Safe to re-run: it is what makes an upgrade re-assert ownership
# after an operator has deleted or renamed a directory.
#
# The list is piped in rather than passed as a path because the caller cannot
# write a file into the data dir before this has run — making the data dir is
# part of what this does. An earlier revision staged the list at
# "$DATA_DIR/.state-dirs.list" and therefore needed a mkdir that the function
# was supposed to be performing.
opskeeper_ensure_state_dirs() {
    local data_dir="$1" conf="${2:-/dev/stdin}"
    local dir uid mode

    while read -r dir uid mode || [[ -n "$dir" ]]; do
        # A comment or a blank line, tested with `case` rather than
        # `[[ … ]] && continue` because the latter returns non-zero on a
        # false test and `set -e` in the callers would read that as an error.
        case "$dir" in
            ''|'#'*) continue ;;
        esac
        if [[ -z "${uid:-}" ]]; then
            printf 'opskeeper_ensure_state_dirs: %s: %q has no uid column\n' "$conf" "$dir" >&2
            return 1
        fi

        mkdir -p "$data_dir/$dir"
        if [[ "$uid" != "-" ]]; then
            # `|| true` on purpose: a chown needs root, and a non-root re-run
            # of the install should still create the tree and carry on.
            chown -R "$uid" "$data_dir/$dir" 2>/dev/null || true
        fi
        if [[ -n "${mode:-}" ]]; then
            chmod -R "$mode" "$data_dir/$dir" 2>/dev/null || true
        fi
    done < "$conf"
}

# --- the manager (uid 65532 = nonroot in Dockerfile.opskeeper) ---
# Format: <dir-under-data-dir>  <uid:gid|->  [chmod-mode]
# `-` means create without chown, for a service whose container process runs
# as root and would be broken by one.
#
# embeddings carries a mode because fastembed-go reads the staged model and
# needs it world-readable. Bumping an image tag in docker-compose.yml without
# updating the uid here fails on first boot: chown to the wrong uid, service
# cannot write.
opskeeper_state_dirs() {
    cat <<'LIST'
embeddings   65532:65532 0755
skills       65532:65532
pages        65532:65532
workspace    65532:65532
tools        65532:65532
repos        65532:65532
plugins      65532:65532
federation   65532:65532
crystallize  65532:65532
mysql        999:999
prometheus   65534:65534
loki         10001:10001
tempo        10001:10001
grafana      472:472
qdrant       -
LIST
}
