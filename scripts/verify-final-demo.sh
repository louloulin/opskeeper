#!/usr/bin/env bash

set -Eeuo pipefail

MODE="live"
SHOW_HELP=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      MODE="dry-run"
      shift
      ;;
    -h|--help)
      SHOW_HELP=true
      shift
      ;;
    *)
      printf 'verify-final-demo: unknown argument: %s\n' "$1" >&2
      SHOW_HELP=true
      MODE="invalid"
      shift
      ;;
  esac
done

if [[ "$SHOW_HELP" == true ]]; then
  cat <<'USAGE'
Usage: scripts/verify-final-demo.sh [--dry-run]

Required environment:
  MANAGER_URL, HOME_URL, TEAMS_URL, ROOMS_URL, OPSKEEPER_URL
  EXPECTED_MANAGER_VERSION, EXPECTED_PLUGIN_VERSION
  MANAGER_AUTH_COOKIE, DEMO_API_TOKEN
  SCENARIO_IDEMPOTENCY_KEY, TARGET_FINGERPRINT, ALERT_FINGERPRINT
  SCENARIO_DURATION_SECONDS, PROMETHEUS_URL, PLUGIN_HEALTH_URL

Optional environment:
  HTTP_TIMEOUT_SECONDS, POLL_INTERVAL_SECONDS
  WORKFLOW_TIMEOUT_SECONDS, RECOVERY_TIMEOUT_SECONDS
  DEGRADED_LATENCY_MS, MIN_STRESSED_UTILIZATION, MAX_RECOVERED_UTILIZATION
  PROMETHEUS_COOKIE, PROMETHEUS_TOKEN, EVIDENCE_OUTPUT

The live mode performs a real incident and requires a human to approve
Candidate A and close the incident through existing HITL/API surfaces.
USAGE
  [[ "$MODE" != "invalid" ]]
  exit 0
fi

for command_name in curl jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'verify-final-demo: required command not found: %s\n' "$command_name" >&2
    exit 1
  fi
done

required_environment=(
  MANAGER_URL
  HOME_URL
  TEAMS_URL
  ROOMS_URL
  OPSKEEPER_URL
  EXPECTED_MANAGER_VERSION
  EXPECTED_PLUGIN_VERSION
  MANAGER_AUTH_COOKIE
  DEMO_API_TOKEN
  SCENARIO_IDEMPOTENCY_KEY
  TARGET_FINGERPRINT
  ALERT_FINGERPRINT
  SCENARIO_DURATION_SECONDS
  PROMETHEUS_URL
  PLUGIN_HEALTH_URL
)

for variable_name in "${required_environment[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'verify-final-demo: missing required environment variable: %s\n' "$variable_name" >&2
    exit 1
  fi
done

validate_url() {
  local variable_name="$1"
  local value="${!variable_name}"
  if [[ "$value" != http://* && "$value" != https://* ]] || [[ "$value" =~ [[:space:]] ]]; then
    printf 'verify-final-demo: %s must be an http(s) URL without whitespace: %s\n' "$variable_name" "$value" >&2
    exit 1
  fi
}

for url_variable in MANAGER_URL HOME_URL TEAMS_URL ROOMS_URL OPSKEEPER_URL PROMETHEUS_URL PLUGIN_HEALTH_URL; do
  validate_url "$url_variable"
done

if [[ -z "${PLUGIN_HEALTH_TOKEN:-}" && -z "${PLUGIN_HEALTH_COOKIE:-}" ]]; then
  printf 'verify-final-demo: PLUGIN_HEALTH_TOKEN or PLUGIN_HEALTH_COOKIE is required for runtime plugin readback\n' >&2
  exit 1
fi

if ! [[ "$SCENARIO_DURATION_SECONDS" =~ ^[0-9]+$ ]] || (( 10#$SCENARIO_DURATION_SECONDS < 60 || 10#$SCENARIO_DURATION_SECONDS > 600 )); then
  printf 'verify-final-demo: SCENARIO_DURATION_SECONDS must be between 60 and 600\n' >&2
  exit 1
fi

HTTP_TIMEOUT_SECONDS="${HTTP_TIMEOUT_SECONDS:-10}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
WORKFLOW_TIMEOUT_SECONDS="${WORKFLOW_TIMEOUT_SECONDS:-900}"
RECOVERY_TIMEOUT_SECONDS="${RECOVERY_TIMEOUT_SECONDS:-180}"
DEGRADED_LATENCY_MS="${DEGRADED_LATENCY_MS:-1500}"
MIN_STRESSED_UTILIZATION="${MIN_STRESSED_UTILIZATION:-0.90}"
MAX_RECOVERED_UTILIZATION="${MAX_RECOVERED_UTILIZATION:-0.25}"
HTTP_TIMEOUT_SECONDS="${HTTP_TIMEOUT_SECONDS%%.*}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS%%.*}"
WORKFLOW_TIMEOUT_SECONDS="${WORKFLOW_TIMEOUT_SECONDS%%.*}"
RECOVERY_TIMEOUT_SECONDS="${RECOVERY_TIMEOUT_SECONDS%%.*}"

for numeric_variable in HTTP_TIMEOUT_SECONDS POLL_INTERVAL_SECONDS WORKFLOW_TIMEOUT_SECONDS RECOVERY_TIMEOUT_SECONDS DEGRADED_LATENCY_MS; do
  if ! [[ "${!numeric_variable}" =~ ^[0-9]+$ ]] || (( ${!numeric_variable} == 0 )); then
    printf 'verify-final-demo: %s must be a positive integer\n' "$numeric_variable" >&2
    exit 1
  fi
done

EVIDENCE_FILE="$(mktemp "${TMPDIR:-/tmp}/opskeeper-final-demo-evidence.XXXXXX")"
CURL_ERROR_FILE="$(mktemp "${TMPDIR:-/tmp}/opskeeper-final-demo-curl-error.XXXXXX")"
EVIDENCE_OUTPUT="${EVIDENCE_OUTPUT:-}"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LAST_STEP="configuration"
LAST_URL=""
LAST_METHOD=""
LAST_HTTP_CODE=""
LAST_RESPONSE_BODY=""

cleanup() {
  rm -f "$EVIDENCE_FILE" "$CURL_ERROR_FILE"
}
trap cleanup EXIT

printf '{"started_at":"%s","mode":"%s"}\n' "$STARTED_AT" "$MODE" > "$EVIDENCE_FILE"

record() {
  local key="$1"
  local value="$2"
  local temporary_file
  temporary_file="$(mktemp "${TMPDIR:-/tmp}/opskeeper-final-demo-record.XXXXXX")"
  jq --arg key "$key" --arg value "$value" '.[$key] = $value' "$EVIDENCE_FILE" > "$temporary_file"
  mv "$temporary_file" "$EVIDENCE_FILE"
}

record_number() {
  local key="$1"
  local value="$2"
  local temporary_file
  temporary_file="$(mktemp "${TMPDIR:-/tmp}/opskeeper-final-demo-record.XXXXXX")"
  jq --arg key "$key" --argjson value "$value" '.[$key] = $value' "$EVIDENCE_FILE" > "$temporary_file"
  mv "$temporary_file" "$EVIDENCE_FILE"
}

sanitize_body() {
  local body="${1:-}"
  body="${body//\"Authorization\"\:\"\[redacted\]\"/\"Authorization\":\"[redacted]\"}"
  if [[ "${#body}" -gt 4000 ]]; then
    body="${body:0:4000}...[truncated]"
  fi
  printf '%s' "$body"
}

emit_evidence() {
  local outcome="$1"
  local message="$2"
  local finished_at
  finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  record "outcome" "$outcome"
  record "message" "$message"
  record "finished_at" "$finished_at"
  if [[ -n "$EVIDENCE_OUTPUT" ]]; then
    jq '.' "$EVIDENCE_FILE" > "$EVIDENCE_OUTPUT"
  fi
  jq '.' "$EVIDENCE_FILE"
}

fail_now() {
  local message="$1"
  trap - ERR
  printf '\nverify-final-demo: FAIL: %s\n' "$message" >&2
  if [[ -n "$LAST_URL" ]]; then
    printf 'Last operation: %s %s; HTTP=%s\n' "$LAST_METHOD" "$LAST_URL" "${LAST_HTTP_CODE:-n/a}" >&2
    printf 'Response context: %s\n' "$(sanitize_body "$LAST_RESPONSE_BODY")" >&2
  fi
  emit_evidence "failed" "$message" >&2
  exit 1
}

trap 'fail_now "unexpected failure at line $LINENO"' ERR

request() {
  local method="$1"
  local url="$2"
  local body="${3:-}"
  local authentication="${4:-none}"
  local raw_response
  local curl_status

  LAST_METHOD="$method"
  LAST_URL="$url"
  LAST_STEP="request $method $url"
  local curl_arguments=(
    -sS
    --max-time "$HTTP_TIMEOUT_SECONDS"
    -X "$method"
    -H 'Cache-Control: no-store'
    -w $'\n%{http_code}'
  )
  if [[ -n "$body" ]]; then
    curl_arguments+=('-H' 'Content-Type: application/json' '--data' "$body")
  fi
  case "$authentication" in
    cookie)
      curl_arguments+=('-H' "Cookie: $MANAGER_AUTH_COOKIE")
      ;;
    demo)
      curl_arguments+=('-H' "Authorization: Bearer $DEMO_API_TOKEN" '-H' 'X-Opskeeper-Version: v1')
      ;;
    plugin)
      if [[ -n "${PLUGIN_HEALTH_TOKEN:-}" ]]; then
        curl_arguments+=('-H' "Authorization: Bearer $PLUGIN_HEALTH_TOKEN")
      fi
      if [[ -n "${PLUGIN_HEALTH_COOKIE:-}" ]]; then
        curl_arguments+=('-H' "Cookie: $PLUGIN_HEALTH_COOKIE")
      fi
      ;;
    prometheus)
      if [[ -n "${PROMETHEUS_TOKEN:-}" ]]; then
        curl_arguments+=('-H' "Authorization: Bearer $PROMETHEUS_TOKEN")
      fi
      if [[ -n "${PROMETHEUS_COOKIE:-}" ]]; then
        curl_arguments+=('-H' "Cookie: $PROMETHEUS_COOKIE")
      fi
      ;;
  esac
  curl_arguments+=("$url")

  if ! raw_response="$(curl "${curl_arguments[@]}" 2>"$CURL_ERROR_FILE")"; then
    curl_status="transport_error: $(tr '\n' ' ' < "$CURL_ERROR_FILE")"
    LAST_RESPONSE_BODY="$curl_status"
    fail_now "request failed: $method $url"
  fi
  LAST_HTTP_CODE="${raw_response##*$'\n'}"
  LAST_RESPONSE_BODY="${raw_response%$'\n'}"
  if ! [[ "$LAST_HTTP_CODE" =~ ^[0-9]{3}$ ]]; then
    fail_now "curl did not return a valid HTTP status for $url"
  fi
}

expect_json() {
  local expression="$1"
  local label="$2"
  if ! jq -e "$expression" >/dev/null 2>&1 <<<"$LAST_RESPONSE_BODY"; then
    fail_now "$label returned an invalid response"
  fi
}

business_probe() {
  local section="$1"
  local mode="$2"
  local path
  if [[ "$mode" == "scenario" ]]; then
    path="/api/v1/demo/scenarios/$(urlencode "$SCENARIO_IDEMPOTENCY_KEY")/business/$section"
  else
    path="/api/v1/demo/business/$section"
  fi
  local raw_response
  local temporary_time
  local temporary_body
  if ! raw_response="$(curl -sS --max-time "$HTTP_TIMEOUT_SECONDS" \
    -H "Authorization: Bearer $DEMO_API_TOKEN" \
    -H 'X-Opskeeper-Version: v1' \
    -H 'Cache-Control: no-store' \
    -w $'\n%{time_total}\n%{http_code}' \
    "$MANAGER_URL$path" 2>"$CURL_ERROR_FILE")"; then
    fail_now "business request failed: $section"
  fi
  LAST_METHOD="GET"
  LAST_URL="$MANAGER_URL$path"
  LAST_HTTP_CODE="${raw_response##*$'\n'}"
  temporary_body="${raw_response%$'\n'}"
  BUSINESS_LATENCY_SECONDS="${temporary_body##*$'\n'}"
  LAST_RESPONSE_BODY="${temporary_body%$'\n'}"
  BUSINESS_LATENCY_MS="$(awk -v seconds="$BUSINESS_LATENCY_SECONDS" 'BEGIN { printf "%.0f", seconds * 1000 }')"
}

urlencode() {
  jq -rn --arg value "$1" '$value|@uri'
}

scenario_read() {
  local key="$1"
  request GET "$MANAGER_URL/api/v1/demo/scenarios/$(urlencode "$key")" "" demo
  expect_json '.data.incident_id != null and .data.scenario_id != null and .data.status != null' 'scenario status'
  SCENARIO_READ_INCIDENT_ID="$(jq -r '.data.incident_id' <<<"$LAST_RESPONSE_BODY")"
  SCENARIO_READ_SCENARIO_ID="$(jq -r '.data.scenario_id' <<<"$LAST_RESPONSE_BODY")"
  SCENARIO_READ_STATUS="$(jq -r '.data.status' <<<"$LAST_RESPONSE_BODY")"
  SCENARIO_READ_MANIFEST_ID="$(jq -r '.data.pool_manifest_id // ""' <<<"$LAST_RESPONSE_BODY")"
}

prometheus_query() {
  local query="$1"
  local label="$2"
  local raw_response
  local curl_arguments=(-sS --max-time "$HTTP_TIMEOUT_SECONDS" -G --data-urlencode "query=$query" -w $'\n%{http_code}')
  if [[ -n "${PROMETHEUS_TOKEN:-}" ]]; then
    curl_arguments+=(-H "Authorization: Bearer $PROMETHEUS_TOKEN")
  fi
  if [[ -n "${PROMETHEUS_COOKIE:-}" ]]; then
    curl_arguments+=(-H "Cookie: $PROMETHEUS_COOKIE")
  fi
  curl_arguments+=("$PROMETHEUS_URL/api/v1/query")
  LAST_METHOD="GET"
  LAST_URL="$PROMETHEUS_URL/api/v1/query"
  LAST_STEP="Prometheus query $label"
  if ! raw_response="$(curl "${curl_arguments[@]}" 2>"$CURL_ERROR_FILE")"; then
    LAST_RESPONSE_BODY="$(tr '\n' ' ' < "$CURL_ERROR_FILE")"
    fail_now "Prometheus query failed: $label"
  fi
  LAST_HTTP_CODE="${raw_response##*$'\n'}"
  LAST_RESPONSE_BODY="${raw_response%$'\n'}"
  expect_json '.status == "success"' "Prometheus query $label"
  PROMETHEUS_VALUE="$(jq -r '.data.result[0]["value"][1] // ""' <<<"$LAST_RESPONSE_BODY")"
  if [[ -z "$PROMETHEUS_VALUE" ]]; then
    return 1
  fi
}

wait_for_prometheus_ratio() {
  local comparison="$1"
  local threshold="$2"
  local timeout_seconds="$3"
  local manifest_id="$4"
  local deadline=$((SECONDS + timeout_seconds))
  local query="max(opskeeper_pool_fixture_active_connections{pool_manifest_id=\"$manifest_id\"}) / max(opskeeper_pool_fixture_capacity{pool_manifest_id=\"$manifest_id\"})"
  while (( SECONDS < deadline )); do
    if prometheus_query "$query" "pool utilization"; then
      if awk -v value="$PROMETHEUS_VALUE" -v threshold="$threshold" -v mode="$comparison" 'BEGIN { exit !((mode == "minimum" && value >= threshold) || (mode == "maximum" && value <= threshold)) }'; then
        return 0
      fi
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

wait_for_scenario_status() {
  local expected_status="$1"
  local timeout_seconds="$2"
  local deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    scenario_read "$SCENARIO_IDEMPOTENCY_KEY"
    printf 'scenario status: %s\n' "$SCENARIO_READ_STATUS"
    if [[ "$SCENARIO_READ_STATUS" == "$expected_status" ]]; then
      return 0
    fi
    if [[ "$SCENARIO_READ_STATUS" == "start_failed" ]]; then
      fail_now "scenario failed to start"
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

record "mode" "$MODE"
record "manager_url" "$MANAGER_URL"
record "home_url" "$HOME_URL"
record "teams_url" "$TEAMS_URL"
record "rooms_url" "$ROOMS_URL"
record "opskeeper_url" "$OPSKEEPER_URL"
record "expected_manager_version" "$EXPECTED_MANAGER_VERSION"
record "expected_plugin_version" "$EXPECTED_PLUGIN_VERSION"
record "plugin_health_url" "$PLUGIN_HEALTH_URL"
record "scenario_idempotency_key" "$SCENARIO_IDEMPOTENCY_KEY"
record_number "scenario_duration_seconds" "$SCENARIO_DURATION_SECONDS"

if [[ "$MODE" == "dry-run" ]]; then
  record "planned_checks" "ready,domain,version,baseline,idempotency,impact,monitoring,human-hitl,recovery,archive"
  emit_evidence "dry-run" "configuration validated; no network request performed"
  exit 0
fi

LAST_STEP="Manager readiness"
request GET "$MANAGER_URL/readyz"
if [[ "$LAST_HTTP_CODE" != 200 ]]; then
  fail_now "Manager /readyz did not return HTTP 200"
fi
record "manager_ready_http" "$LAST_HTTP_CODE"

check_domain() {
  local domain_variable="$1"
  local result_key="$2"
  LAST_STEP="domain check $domain_variable"
  request GET "${!domain_variable}"
  if [[ "$LAST_HTTP_CODE" != 200 ]]; then
    fail_now "$domain_variable did not return HTTP 200"
  fi
  record "$result_key" "$LAST_HTTP_CODE"
}

check_domain OPSKEEPER_URL opskeeper_domain_http
check_domain HOME_URL home_domain_http
check_domain TEAMS_URL teams_domain_http
check_domain ROOMS_URL rooms_domain_http
check_domain MANAGER_URL manager_domain_http

LAST_STEP="version readback"
request GET "$MANAGER_URL/api/v1/version/deployment" "" cookie
if [[ "$LAST_HTTP_CODE" != 200 ]]; then
  fail_now "version readback did not return HTTP 200"
fi
expect_json '.manager_version and .plugin.version' 'version readback'
ACTUAL_MANAGER_VERSION="$(jq -r '.manager_version' <<<"$LAST_RESPONSE_BODY")"
ACTUAL_PLUGIN_VERSION="$(jq -r '.plugin.version' <<<"$LAST_RESPONSE_BODY")"
if [[ "$ACTUAL_MANAGER_VERSION" != "$EXPECTED_MANAGER_VERSION" ]]; then
  fail_now "Manager version mismatch: expected $EXPECTED_MANAGER_VERSION, got $ACTUAL_MANAGER_VERSION"
fi
if [[ "$ACTUAL_PLUGIN_VERSION" != "$EXPECTED_PLUGIN_VERSION" ]]; then
  fail_now "Plugin version mismatch: expected $EXPECTED_PLUGIN_VERSION, got $ACTUAL_PLUGIN_VERSION"
fi
record "actual_manager_version" "$ACTUAL_MANAGER_VERSION"
record "actual_plugin_version" "$ACTUAL_PLUGIN_VERSION"

LAST_STEP="runtime plugin health readback"
request GET "$PLUGIN_HEALTH_URL" "" plugin
if [[ "$LAST_HTTP_CODE" != 200 ]]; then
  fail_now "plugin runtime health readback did not return HTTP 200"
fi
expect_json '.worker.version and .worker.loaded == true and .worker.enabled == true and .synced == true and (.diff | length == 0)' 'plugin runtime health'
ACTUAL_RUNTIME_PLUGIN_VERSION="$(jq -r '.worker.version' <<<"$LAST_RESPONSE_BODY")"
if [[ "$ACTUAL_RUNTIME_PLUGIN_VERSION" != "$EXPECTED_PLUGIN_VERSION" ]]; then
  fail_now "runtime plugin version mismatch: expected $EXPECTED_PLUGIN_VERSION, got $ACTUAL_RUNTIME_PLUGIN_VERSION"
fi
record "actual_runtime_plugin_version" "$ACTUAL_RUNTIME_PLUGIN_VERSION"
record "plugin_runtime_synced" "true"

for section in orders inventory audit; do
  business_probe "$section" "baseline"
  if [[ "$LAST_HTTP_CODE" != 200 ]]; then
    fail_now "healthy baseline business API $section did not return HTTP 200"
  fi
  if ! jq -e --arg section "$section" '.data.section == $section' <<<"$LAST_RESPONSE_BODY" >/dev/null; then
    fail_now "baseline business response $section is invalid"
  fi
  record "baseline_${section}_http" "$LAST_HTTP_CODE"
  record_number "baseline_${section}_latency_ms" "$BUSINESS_LATENCY_MS"
done

LAST_STEP="scenario start"
START_BODY="$(jq -n \
  --arg idempotency_key "$SCENARIO_IDEMPOTENCY_KEY" \
  --arg target_fingerprint "$TARGET_FINGERPRINT" \
  --arg alert_fingerprint "$ALERT_FINGERPRINT" \
  --argjson duration "$SCENARIO_DURATION_SECONDS" \
  '{idempotency_key:$idempotency_key,scenario_id:"pg-pool-exhaustion",target:"pg:pool-fixture",target_fingerprint:$target_fingerprint,alert_fingerprint:$alert_fingerprint,duration_seconds:$duration}')"
request POST "$MANAGER_URL/api/v1/demo/scenarios/pg-pool-exhaustion/start" "$START_BODY" demo
if [[ "$LAST_HTTP_CODE" != 201 && "$LAST_HTTP_CODE" != 200 ]]; then
  fail_now "scenario start did not return HTTP 200/201"
fi
expect_json '.data.incident_id != null and .data.scenario_id != null and .data.pool_manifest_id != ""' 'scenario start response'
INCIDENT_ID="$(jq -r '.data.incident_id' <<<"$LAST_RESPONSE_BODY")"
SCENARIO_ID="$(jq -r '.data.scenario_id' <<<"$LAST_RESPONSE_BODY")"
MANIFEST_ID="$(jq -r '.data.pool_manifest_id' <<<"$LAST_RESPONSE_BODY")"
record "initial_scenario_http" "$LAST_HTTP_CODE"
record "incident_id" "$INCIDENT_ID"
record "scenario_id" "$SCENARIO_ID"
record "pool_manifest_id" "$MANIFEST_ID"

request POST "$MANAGER_URL/api/v1/demo/scenarios/pg-pool-exhaustion/start" "$START_BODY" demo
if [[ "$LAST_HTTP_CODE" != 200 && "$LAST_HTTP_CODE" != 201 ]]; then
  fail_now "idempotent scenario retry did not return HTTP 200/201"
fi
RETRY_INCIDENT_ID="$(jq -r '.data.incident_id' <<<"$LAST_RESPONSE_BODY")"
RETRY_SCENARIO_ID="$(jq -r '.data.scenario_id' <<<"$LAST_RESPONSE_BODY")"
if [[ "$RETRY_INCIDENT_ID" != "$INCIDENT_ID" || "$RETRY_SCENARIO_ID" != "$SCENARIO_ID" ]]; then
  fail_now "idempotent scenario retry returned different IDs"
fi
record "idempotent_retry_http" "$LAST_HTTP_CODE"
record "idempotency_ids_stable" "true"

scenario_read "$SCENARIO_IDEMPOTENCY_KEY"
if [[ "$SCENARIO_READ_INCIDENT_ID" != "$INCIDENT_ID" || "$SCENARIO_READ_SCENARIO_ID" != "$SCENARIO_ID" ]]; then
  fail_now "scenario read returned different IDs"
fi
if [[ "$SCENARIO_READ_MANIFEST_ID" != "$MANIFEST_ID" ]]; then
  fail_now "scenario read returned a different pool manifest"
fi
record "scenario_read_ids_stable" "true"

BUSINESS_IMPACT_OBSERVED=false
for section in orders inventory audit; do
  business_probe "$section" "scenario"
  record "degraded_${section}_http" "$LAST_HTTP_CODE"
  record_number "degraded_${section}_latency_ms" "$BUSINESS_LATENCY_MS"
  if [[ "$LAST_HTTP_CODE" == 503 ]] || (( BUSINESS_LATENCY_MS >= DEGRADED_LATENCY_MS )); then
    BUSINESS_IMPACT_OBSERVED=true
  fi
done
record "business_impact_observed" "$BUSINESS_IMPACT_OBSERVED"
if [[ "$BUSINESS_IMPACT_OBSERVED" != true ]]; then
  fail_now "no business API returned 503 or exceeded the degraded latency threshold"
fi

if ! wait_for_prometheus_ratio minimum "$MIN_STRESSED_UTILIZATION" "$RECOVERY_TIMEOUT_SECONDS" "$MANIFEST_ID"; then
  fail_now "monitoring did not confirm pool saturation"
fi
record_number "monitor_stressed_utilization" "$PROMETHEUS_VALUE"

if ! wait_for_scenario_status awaiting_approval "$WORKFLOW_TIMEOUT_SECONDS"; then
  fail_now "scenario did not reach awaiting_approval"
fi
if ! jq -e '.data.preview_decision.eligible_for_hitl == true and (.data.preview_decision.candidate_a | length > 0) and (.data.preview_decision.candidate_b | length > 0)' <<<"$LAST_RESPONSE_BODY" >/dev/null; then
  fail_now "awaiting_approval lacks complete PASS/FAIL preview eligibility"
fi
PREVIEW_DECISION="$(jq -c '.data.preview_decision' <<<"$LAST_RESPONSE_BODY")"
record "preview_decision" "$PREVIEW_DECISION"

printf '\nACTION REQUIRED: approve only Candidate A in %s using the existing AgentTeams HITL approval surface.\n' "$ROOMS_URL"
printf 'The preview PASS is eligibility only. The approved repair must run through recovery.execute with the exact proposal, incident, candidate, execution, target, and fingerprint bindings.\n'
printf 'Type approve-candidate-a to confirm that the real approval and repair have been initiated: '
read -r human_confirmation
if [[ "$human_confirmation" != "approve-candidate-a" ]]; then
  fail_now "operator did not confirm real Candidate A HITL approval"
fi
record "human_hitl_confirmed" "true"

if ! wait_for_scenario_status recovered "$WORKFLOW_TIMEOUT_SECONDS"; then
  fail_now "scenario did not recover after HITL approval and repair"
fi
if ! wait_for_prometheus_ratio maximum "$MAX_RECOVERED_UTILIZATION" "$RECOVERY_TIMEOUT_SECONDS" "$MANIFEST_ID"; then
  fail_now "monitoring did not confirm active/capacity recovery"
fi
record_number "monitor_recovered_utilization" "$PROMETHEUS_VALUE"

RECOVERED_BUSINESS_COUNT=0
for section in orders inventory audit; do
  business_probe "$section" "scenario"
  if [[ "$LAST_HTTP_CODE" != 200 ]]; then
    continue
  fi
  if jq -e --arg section "$section" '.data.section == $section' <<<"$LAST_RESPONSE_BODY" >/dev/null; then
    RECOVERED_BUSINESS_COUNT=$((RECOVERED_BUSINESS_COUNT + 1))
    record "recovered_${section}_http" "$LAST_HTTP_CODE"
    record_number "recovered_${section}_latency_ms" "$BUSINESS_LATENCY_MS"
  fi
done
record_number "recovered_business_api_count" "$RECOVERED_BUSINESS_COUNT"
if (( RECOVERED_BUSINESS_COUNT != 3 )); then
  fail_now "not all business APIs returned healthy snapshots after repair"
fi

scenario_read "$SCENARIO_IDEMPOTENCY_KEY"
if [[ "$SCENARIO_READ_INCIDENT_ID" != "$INCIDENT_ID" || "$SCENARIO_READ_SCENARIO_ID" != "$SCENARIO_ID" ]]; then
  fail_now "post-recovery scenario read returned different IDs"
fi
record "post_recovery_ids_stable" "true"

ARCHIVE_URL="$MANAGER_URL/api/v1/incidents/$(urlencode "$INCIDENT_ID")/archive?tenant_id=1"
request GET "$ARCHIVE_URL" "" cookie
expect_json '.data.incident_id != null' 'incident archive'
ARCHIVE_EVIDENCE_COMPLETE="$(jq -r '.data.evidence_complete' <<<"$LAST_RESPONSE_BODY")"
if [[ "$ARCHIVE_EVIDENCE_COMPLETE" != true ]]; then
  printf '\nACTION REQUIRED: close incident %s through the existing incident closure API/UI after verifying recovery.\n' "$INCIDENT_ID"
  printf 'Type close-incident to confirm the real closure was recorded: '
  read -r closure_confirmation
  if [[ "$closure_confirmation" != "close-incident" ]]; then
    fail_now "operator did not confirm real incident closure"
  fi
  record "human_closure_confirmed" "true"
  ARCHIVE_DEADLINE=$((SECONDS + RECOVERY_TIMEOUT_SECONDS))
  while (( SECONDS < ARCHIVE_DEADLINE )); do
    request GET "$ARCHIVE_URL" "" cookie
    if jq -e '.data.evidence_complete == true' <<<"$LAST_RESPONSE_BODY" >/dev/null; then
      ARCHIVE_EVIDENCE_COMPLETE=true
      break
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
fi

if [[ "$ARCHIVE_EVIDENCE_COMPLETE" != true ]]; then
  fail_now "Archive is not evidence complete"
fi
if ! jq -e '
  .data.missing_event_types == [] and
  ([.data.repair_previews[].candidates[] | select(.candidate_id == "candidate-a" and .decision == "PASS")] | length > 0) and
  ([.data.repair_previews[].candidates[] | select(.candidate_id == "candidate-b" and (.decision == "FAIL" or .decision == "REJECTED_BY_PREVIEW"))] | length > 0)
' <<<"$LAST_RESPONSE_BODY" >/dev/null; then
  fail_now "Archive lacks complete evidence or Candidate A/B preview decisions"
fi
record "archive_evidence_complete" "$ARCHIVE_EVIDENCE_COMPLETE"
record "archive_preview_ab_verified" "true"
record "archive_event_count" "$(jq -r '.data.event_count' <<<"$LAST_RESPONSE_BODY")"
record "archive_missing_event_types" "$(jq -c '.data.missing_event_types' <<<"$LAST_RESPONSE_BODY")"

emit_evidence "passed" "final demo E2E assertions passed"
