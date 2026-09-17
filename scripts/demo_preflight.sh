#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/demo_preflight.sh

This is a read-only readiness gate for an OpsKeeper + AgentTeams demo
environment. It checks public routing and TLS, static plugin bundles, core
containers, direct service readiness, plugin synchronization, disk capacity,
and recent fatal logs. It never restarts, updates, or writes to services.

Required environment:
  DEMO_SSH_HOST             SSH host used for read-only Docker and localhost checks
  TEAMS_URL                 Public AgentTeams Dashboard URL
  ROOMS_URL                 Public Element Web URL
  OPSKEEPER_PUBLIC_URL      Public OpsKeeper Web URL

Common overrides:
  EXPECTED_INSTALLER_VERSION   Default: 1.4.3
  EXPECTED_TEAMHARNESS_VERSION Default: 1.0.55
  OPSKEEPER_INTERNAL_URL       Default: http://127.0.0.1:28080
  PLUGIN_MANAGER_INTERNAL_URL  Default: http://127.0.0.1:18096
  AGENTTEAMS_INTERNAL_URL      Default: http://127.0.0.1:18888
  REQUIRED_CONTAINERS          Space-separated container list
  LOG_WINDOW                   Default: 15m

Optional:
  DEMO_PREFLIGHT_ENV           Path to a shell env file sourced before checks

Examples:
  DEMO_SSH_HOST=demo-host \
    TEAMS_URL=https://teams.example.com \
    ROOMS_URL=https://rooms.example.com \
    OPSKEEPER_PUBLIC_URL=https://opskeeper.example.com \
    scripts/demo_preflight.sh

  DEMO_PREFLIGHT_ENV=/secure/path/demo-preflight.env \
    scripts/demo_preflight.sh
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ -n "${DEMO_PREFLIGHT_ENV:-}" ]]; then
  if [[ ! -r "$DEMO_PREFLIGHT_ENV" ]]; then
    echo "cannot read DEMO_PREFLIGHT_ENV=$DEMO_PREFLIGHT_ENV" >&2
    exit 2
  fi
  set -o allexport
  # shellcheck disable=SC1090
  source "$DEMO_PREFLIGHT_ENV"
  set +o allexport
fi

: "${DEMO_SSH_HOST:?set DEMO_SSH_HOST, or run --help for required variables}"
: "${TEAMS_URL:?set TEAMS_URL}"
: "${ROOMS_URL:?set ROOMS_URL}"
: "${OPSKEEPER_PUBLIC_URL:?set OPSKEEPER_PUBLIC_URL}"

EXPECTED_INSTALLER_VERSION="${EXPECTED_INSTALLER_VERSION:-1.4.3}"
EXPECTED_TEAMHARNESS_VERSION="${EXPECTED_TEAMHARNESS_VERSION:-1.0.55}"
OPSKEEPER_INTERNAL_URL="${OPSKEEPER_INTERNAL_URL:-http://127.0.0.1:28080}"
PLUGIN_MANAGER_INTERNAL_URL="${PLUGIN_MANAGER_INTERNAL_URL:-http://127.0.0.1:18096}"
AGENTTEAMS_INTERNAL_URL="${AGENTTEAMS_INTERNAL_URL:-http://127.0.0.1:18888}"
LOG_WINDOW="${LOG_WINDOW:-15m}"
CERT_WARN_DAYS="${CERT_WARN_DAYS:-14}"
CERT_FAIL_DAYS="${CERT_FAIL_DAYS:-3}"
MAX_HTTP_SECONDS="${MAX_HTTP_SECONDS:-3}"
PLUGIN_MANAGER_CONTAINER="${PLUGIN_MANAGER_CONTAINER:-agentteams-plugin-manager}"
REQUIRED_CONTAINERS="${REQUIRED_CONTAINERS:-$(cat <<'EOF'
agentteams-controller
agentteams-dashboard
agentteams-https
agentteams-manager
agentteams-plugin-manager
agentteams-worker-ccc
agentteams-worker-lumos
agentteams-worker-opskeeper-alerter
agentteams-worker-opskeeper-investigator
agentteams-worker-opskeeper-reviewer
agentteams-worker-opskeeper-repairer
agentteams-worker-opskeeper-verifier
agentteams-worker-opskeeper-reporter
element-web
element-web-public
opskeeper
opskeeper-frontier
opskeeper-hitl-monitor
opskeeper-postgres
opskeeper-redis
opskeeper-tempo
opskeeper-pool-fixture
opskeeper-pool-auth-proxy
opskeeper-pool-metrics
EOF
)}"

for command in curl jq python3; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "missing local dependency: $command" >&2
    exit 2
  fi
done

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  GREEN=$'\033[32m'
  YELLOW=$'\033[33m'
  RED=$'\033[31m'
  BOLD=$'\033[1m'
  RESET=$'\033[0m'
else
  GREEN=""
  YELLOW=""
  RED=""
  BOLD=""
  RESET=""
fi

PASS_COUNT=0
WARN_COUNT=0
FAIL_COUNT=0
FAILED_CHECKS=()

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '%sPASS%s %s\n' "$GREEN" "$RESET" "$1"
}

warn() {
  WARN_COUNT=$((WARN_COUNT + 1))
  printf '%sWARN%s %s\n' "$YELLOW" "$RESET" "$1"
}

fail() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  FAILED_CHECKS+=("$1")
  printf '%sFAIL%s %s\n' "$RED" "$RESET" "$1"
}

run_remote_shell() {
  local command_text="$1"
  if [[ -n "${DEMO_SSH_HOST}" ]]; then
    ssh -o BatchMode=yes -o ConnectTimeout=8 -o ConnectionAttempts=1 \
      "$DEMO_SSH_HOST" "/bin/sh -s" <<< "$command_text"
  else
    /bin/sh -s <<< "$command_text"
  fi
}

capture_remote() {
  local command_text="$1" output status
  if output=$(run_remote_shell "$command_text" 2>&1); then
    status=0
  else
    status=$?
  fi
  REMOTE_CAPTURE_OUTPUT="$output"
  return "$status"
}

check_http_200() {
  local label="$1" url="$2" metadata status elapsed verify
  if ! metadata=$(curl -sS --max-time 8 -o /dev/null -w '%{http_code} %{time_total} %{ssl_verify_result}' "$url"); then
    fail "$label unreachable: $url"
    return
  fi

  read -r status elapsed verify <<< "$metadata"
  if [[ "$status" != "200" ]]; then
    fail "$label returned HTTP $status at $url"
    return
  fi

  if [[ "$url" == https:* && "$verify" != "0" ]]; then
    fail "$label TLS verification failed at $url (verify=$verify)"
    return
  fi

  if awk -v seconds="$elapsed" -v limit="$MAX_HTTP_SECONDS" 'BEGIN { exit !(seconds > limit) }'; then
    warn "$label responded in ${elapsed}s, above ${MAX_HTTP_SECONDS}s"
  else
    pass "$label reachable in ${elapsed}s"
  fi
}

check_certificate() {
  local label="$1" url="$2" days
  if ! days=$(python3 - "$url" <<'PY'
import datetime
import socket
import ssl
import sys
from urllib.parse import urlparse

url = sys.argv[1]
parsed = urlparse(url)
if parsed.scheme != "https" or not parsed.hostname:
    print("skip")
    raise SystemExit(0)

context = ssl.create_default_context()
with socket.create_connection((parsed.hostname, 443), timeout=8) as connection:
    with context.wrap_socket(connection, server_hostname=parsed.hostname) as tls_connection:
        certificate = tls_connection.getpeercert()
not_after = ssl.cert_time_to_seconds(certificate["notAfter"])
print(int((not_after - datetime.datetime.now(datetime.timezone.utc).timestamp()) / 86400))
PY
); then
    fail "$label certificate could not be read"
    return
  fi

  if [[ "$days" == "skip" ]]; then
    return
  fi

  if [[ "$days" -le "$CERT_FAIL_DAYS" ]]; then
    fail "$label certificate expires in ${days}d"
  elif [[ "$days" -le "$CERT_WARN_DAYS" ]]; then
    warn "$label certificate expires in ${days}d"
  else
    pass "$label certificate valid for ${days}d"
  fi
}

check_static_plugin() {
  local label="$1" plugin_id="$2" expected_version="$3" manifest entry bundle_url bundle foreground_count
  if ! manifest=$(curl -fsS --max-time 8 "${TEAMS_URL}/plugins/${plugin_id}/plugin.json?preflight=$(date +%s)"); then
    fail "$label manifest unreachable"
    return
  fi

  if [[ "$(jq -r '.id // ""' <<< "$manifest")" != "$plugin_id" ]]; then
    fail "$label manifest id mismatch"
    return
  fi

  local actual_version
  actual_version=$(jq -r '.version // ""' <<< "$manifest")
  if [[ "$actual_version" != "$expected_version" ]]; then
    fail "$label version is ${actual_version:-missing}; expected $expected_version"
    return
  fi

  entry=$(jq -r '.entry.dashboard // ""' <<< "$manifest")
  if [[ -z "$entry" ]]; then
    fail "$label dashboard entry is missing"
    return
  fi

  case "$entry" in
    /*) bundle_url="${TEAMS_URL}${entry}" ;;
    *) bundle_url="${TEAMS_URL}/plugins/${plugin_id}/${entry}" ;;
  esac

  if ! bundle=$(curl -fsS --max-time 10 "${bundle_url}?preflight=$(date +%s)"); then
    fail "$label bundle unreachable: $bundle_url"
    return
  fi

  if [[ "${#bundle}" -lt 1000 ]]; then
    fail "$label bundle is unexpectedly small (${#bundle} bytes)"
    return
  fi

  if grep -qF 'color:"var(--muted)"' <<< "$bundle"; then
    fail "$label bundle still uses the background token for muted text"
    return
  fi

  foreground_count=$(grep -oF 'var(--muted-foreground)' <<< "$bundle" | wc -l | tr -d ' ' || true)
  if [[ "$foreground_count" -eq 0 ]]; then
    fail "$label bundle does not contain muted foreground tokens"
    return
  fi

  pass "$label v${actual_version} manifest and bundle verified"
}

printf '%sOpsKeeper demo environment preflight%s\n' "$BOLD" "$RESET"
printf 'SSH host: %s\n' "$DEMO_SSH_HOST"
printf 'Teams: %s\n' "$TEAMS_URL"
printf 'Rooms: %s\n' "$ROOMS_URL"
printf 'OpsKeeper: %s\n' "$OPSKEEPER_PUBLIC_URL"
printf 'Mode: read-only\n\n'

if capture_remote 'command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1'; then
  pass "remote Docker is reachable"
else
  fail "remote Docker is unreachable: ${REMOTE_CAPTURE_OUTPUT}"
fi

check_http_200 "AgentTeams Dashboard" "${TEAMS_URL}/"
check_http_200 "Element Web" "${ROOMS_URL}/"
check_http_200 "OpsKeeper Web login" "${OPSKEEPER_PUBLIC_URL}/login"
check_certificate "AgentTeams Dashboard" "$TEAMS_URL"
check_certificate "Element Web" "$ROOMS_URL"
check_certificate "OpsKeeper Web" "$OPSKEEPER_PUBLIC_URL"

check_static_plugin "Plugin Installer" "agentteams-plugin-installer" "$EXPECTED_INSTALLER_VERSION"
check_static_plugin "OpsKeeper TeamHarness" "opskeeper-teamharness" "$EXPECTED_TEAMHARNESS_VERSION"

container_list=$(printf '%s\n' "$REQUIRED_CONTAINERS" | xargs)
container_script=$(cat <<EOF
for container in $container_list; do
  if ! state=\$(docker inspect -f '{{.State.Status}}' "\$container" 2>/dev/null); then
    printf 'MISSING|%s\n' "\$container"
    continue
  fi
  health=\$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "\$container" 2>/dev/null)
  restarts=\$(docker inspect -f '{{.RestartCount}}' "\$container" 2>/dev/null)
  printf 'STATE|%s|%s|%s|%s\n' "\$container" "\$state" "\$health" "\$restarts"
done
EOF
)

if capture_remote "$container_script"; then
  while IFS='|' read -r kind container state health restarts; do
    [[ "$kind" == "STATE" || "$kind" == "MISSING" ]] || continue
    if [[ "$kind" == "MISSING" ]]; then
      fail "required container missing: $container"
      continue
    fi
    if [[ "$state" != "running" ]]; then
      fail "container $container is $state"
      continue
    fi
    if [[ "$health" != "healthy" && "$health" != "none" ]]; then
      fail "container $container health is $health"
      continue
    fi
    if [[ "$restarts" -gt 0 ]]; then
      warn "container $container has restarted $restarts time(s)"
    else
      pass "container $container running"
    fi
  done <<< "$REMOTE_CAPTURE_OUTPUT"
else
  fail "could not inspect required containers: ${REMOTE_CAPTURE_OUTPUT}"
fi

if capture_remote "curl -fsS --max-time 8 '$OPSKEEPER_INTERNAL_URL/healthz'"; then
  if [[ "$REMOTE_CAPTURE_OUTPUT" == "ok" ]]; then
    pass "OpsKeeper liveness endpoint returns ok"
  else
    fail "OpsKeeper liveness endpoint returned unexpected body: $REMOTE_CAPTURE_OUTPUT"
  fi
else
  fail "OpsKeeper liveness endpoint failed: ${REMOTE_CAPTURE_OUTPUT}"
fi

if capture_remote "curl -fsS --max-time 8 '$OPSKEEPER_INTERNAL_URL/readyz'"; then
  readiness="$REMOTE_CAPTURE_OUTPUT"
  if jq -e '.ready == true and ([.checks[].ok] | all)' >/dev/null <<< "$readiness"; then
    pass "OpsKeeper readiness reports DB, Redis, and workers ready"
  else
    fail "OpsKeeper readiness is not fully ready: $(jq -c . <<< "$readiness")"
  fi
else
  fail "OpsKeeper readiness endpoint failed: ${REMOTE_CAPTURE_OUTPUT}"
fi

if capture_remote "curl -fsS --max-time 8 '$PLUGIN_MANAGER_INTERNAL_URL/healthz'"; then
  pass "plugin-manager health endpoint reachable"
else
  fail "plugin-manager health endpoint failed: ${REMOTE_CAPTURE_OUTPUT}"
fi

if capture_remote "curl -fsS --max-time 8 '$AGENTTEAMS_INTERNAL_URL/healthz'"; then
  pass "AgentTeams manager health endpoint reachable"
else
  fail "AgentTeams manager health endpoint failed: ${REMOTE_CAPTURE_OUTPUT}"
fi

plugin_script=$(cat <<EOF
TOKEN=\$(docker inspect '$PLUGIN_MANAGER_CONTAINER' --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^PLUGIN_MANAGER_SA_TOKEN=//p' | tr -d '\r')
if [ -z "\$TOKEN" ]; then
  exit 1
fi
curl -fsS --max-time 8 -H "Authorization: Bearer \$TOKEN" '$PLUGIN_MANAGER_INTERNAL_URL/api/v1/plugins/opskeeper-teamharness/health' | jq -c .
curl -fsS --max-time 8 -H "Authorization: Bearer \$TOKEN" '$PLUGIN_MANAGER_INTERNAL_URL/api/v1/plugins' | jq -c .
EOF
)

if capture_remote "$plugin_script"; then
  plugin_health=$(sed -n '1p' <<< "$REMOTE_CAPTURE_OUTPUT")
  plugin_list=$(sed -n '2p' <<< "$REMOTE_CAPTURE_OUTPUT")

  if jq -e \
    '.synced == true and (.diff | length == 0) and .worker.version == "'"$EXPECTED_TEAMHARNESS_VERSION"'" and .worker.loaded == true and .worker.enabled == true' \
    >/dev/null <<< "$plugin_health"; then
    pass "TeamHarness v$EXPECTED_TEAMHARNESS_VERSION loaded, enabled, and in sync"
  else
    fail "TeamHarness worker state is not ready: $plugin_health"
  fi

  installer_row=$(jq -c '.plugins[] | select(.id == "agentteams-plugin-installer")' <<< "$plugin_list")
  if [[ -z "$installer_row" ]]; then
    fail "Plugin Installer is absent from plugin-manager roster"
  elif jq -e \
    '.version == "'"$EXPECTED_INSTALLER_VERSION"'" and any(.present_in[]?; . == "manager") and any(.present_in[]?; . == "dashboard")' \
    >/dev/null <<< "$installer_row"; then
    pass "Plugin Installer v$EXPECTED_INSTALLER_VERSION registered in manager and Dashboard"
  else
    fail "Plugin Installer roster is not ready: $installer_row"
  fi
else
  fail "could not read plugin-manager roster: ${REMOTE_CAPTURE_OUTPUT}"
fi

if capture_remote "df -P / | awk 'NR == 2 {gsub(/%/, \"\", \$5); print \$5}'"; then
  disk_percent="${REMOTE_CAPTURE_OUTPUT//[[:space:]]/}"
  if [[ ! "$disk_percent" =~ ^[0-9]+$ ]]; then
    fail "could not parse root disk usage: $REMOTE_CAPTURE_OUTPUT"
  elif [[ "$disk_percent" -ge 95 ]]; then
    fail "root disk usage is ${disk_percent}%"
  elif [[ "$disk_percent" -ge 85 ]]; then
    warn "root disk usage is ${disk_percent}%"
  else
    pass "root disk usage is ${disk_percent}%"
  fi
else
  fail "root disk usage check failed: ${REMOTE_CAPTURE_OUTPUT}"
fi

log_script=$(cat <<EOF
for container in $container_list; do
  logs=\$(docker logs --since '$LOG_WINDOW' "\$container" 2>&1 || true)
  hard_count=\$(printf '%s\n' "\$logs" | grep -Eic 'panic|fatal' || true)
  soft_count=\$(printf '%s\n' "\$logs" | grep -Eic 'level=error|ERROR' || true)
  printf 'LOGS|%s|%s|%s\n' "\$container" "\$hard_count" "\$soft_count"
done
EOF
)

if capture_remote "$log_script"; then
  while IFS='|' read -r kind container hard_count soft_count; do
    [[ "$kind" == "LOGS" ]] || continue
    if [[ "$hard_count" -gt 0 ]]; then
      fail "$container has $hard_count panic/fatal log line(s) in the last $LOG_WINDOW"
    elif [[ "$soft_count" -gt 0 ]]; then
      warn "$container has $soft_count error-level log line(s) in the last $LOG_WINDOW"
    fi
  done <<< "$REMOTE_CAPTURE_OUTPUT"
  pass "recent panic/fatal log scan completed for required containers"
else
  fail "recent log scan failed: ${REMOTE_CAPTURE_OUTPUT}"
fi

printf '\n%sSummary%s pass=%d warn=%d fail=%d\n' "$BOLD" "$RESET" "$PASS_COUNT" "$WARN_COUNT" "$FAIL_COUNT"
if [[ "$FAIL_COUNT" -gt 0 ]]; then
  printf '\nBlocked checks:\n'
  for check in "${FAILED_CHECKS[@]}"; do
    printf -- '- %s\n' "$check"
  done
  exit 1
fi

if [[ "$WARN_COUNT" -gt 0 ]]; then
  printf 'Result: GO WITH WARNINGS\n'
else
  printf 'Result: GO\n'
fi
