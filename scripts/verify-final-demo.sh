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
  MANAGER_AUTH_COOKIE or MANAGER_AUTH_TOKEN, DEMO_API_TOKEN
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
  DEMO_API_TOKEN
  SCENARIO_IDEMPOTENCY_KEY
  TARGET_FINGERPRINT
  ALERT_FINGERPRINT
  SCENARIO_DURATION_SECONDS
  PROMETHEUS_URL
  PLUGIN_HEALTH_URL
)

if [[ -z "${MANAGER_AUTH_COOKIE:-}" && -z "${MANAGER_AUTH_TOKEN:-}" ]]; then
  printf 'verify-final-demo: MANAGER_AUTH_COOKIE or MANAGER_AUTH_TOKEN is required\n' >&2
  exit 1
fi

for variable_name in "${required_environment[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'verify-final-demo: missing required environment variable: %s\n' "$variable_name" >&2
    exit 1
  fi
done

validate_url() {
  local variable_name="$1"
  local value="${!variable_name}"
  local scheme="${value%%://*}"
  local remainder="${value#*://}"
  local authority="${remainder%%[/?#]*}"
  if [[ "$value" != http://* && "$value" != https://* ]] || [[ "$value" =~ [[:space:]] ]] ||
     [[ "$value" == *\?* ]] || [[ "$value" == *\#* ]] || [[ "$authority" == *@* ]]; then
    printf 'verify-final-demo: %s must be an http(s) base URL without whitespace, query, fragment, or user info\n' "$variable_name" >&2
    exit 1
  fi
}

url_origin_from_value() {
  local value="$1"
  local scheme="${value%%://*}"
  local remainder="${value#*://}"
  local authority="${remainder%%[/?#]*}"
  printf '%s://%s' "$scheme" "$authority"
}

safe_url_label_from_value() {
  local value="$1"
  local scheme="${value%%://*}"
  local remainder="${value#*://}"
  local authority="${remainder%%[/?#]*}"
  local path_source="${remainder%%\?*}"
  path_source="${path_source%%\#*}"
  local path="/"
  if [[ "$path_source" == */* ]]; then
    path="/${path_source#*/}"
  fi
  path="$(printf '%s' "$path" | LC_ALL=C tr -Cd '[:alnum:]._/:-' | cut -c1-200)"
  printf '%s://%s%s' "$scheme" "$authority" "$path"
}

configured_url_origin() {
  url_origin_from_value "${!1}"
}

validate_unit_interval() {
  local variable_name="$1"
  local value="${!variable_name}"
  if ! [[ "$value" =~ ^(0|1)(\.[0-9]+)?$ ]] ||
     ! awk -v value="$value" 'BEGIN { exit !(value >= 0 && value <= 1) }'; then
    printf 'verify-final-demo: %s must be a finite decimal number from 0 through 1\n' "$variable_name" >&2
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
ARCHIVE_TENANT_ID="${OPSKEEPER_ARCHIVE_TENANT_ID:-goai-demo}"
DEGRADED_LATENCY_MS="${DEGRADED_LATENCY_MS:-1500}"
MIN_STRESSED_UTILIZATION="${MIN_STRESSED_UTILIZATION-0.90}"
MAX_RECOVERED_UTILIZATION="${MAX_RECOVERED_UTILIZATION-0.25}"
validate_unit_interval MIN_STRESSED_UTILIZATION
validate_unit_interval MAX_RECOVERED_UTILIZATION
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
# Per-request response body sink. The request() function writes curl's body
# here (via -o) and reads it back as LAST_RESPONSE_BODY, so the HTTP status
# code (captured separately via -w '%{http_code}') is never mixed into the
# raw body parsing path.
LAST_BODY_FILE="$(mktemp "${TMPDIR:-/tmp}/opskeeper-final-demo-body.XXXXXX")"
EVIDENCE_OUTPUT="${EVIDENCE_OUTPUT:-}"
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LAST_STEP="configuration"
LAST_OPERATION_LABEL=""
LAST_OPERATION_ORIGIN=""
LAST_METHOD=""
LAST_HTTP_CODE=""
LAST_RESPONSE_BODY=""
LAST_TRANSPORT_ERROR=false

cleanup() {
  rm -f "$EVIDENCE_FILE" "$CURL_ERROR_FILE" "$LAST_BODY_FILE"
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

safe_response_fields() {
  local body="${1:-}"
  if ! jq -e . >/dev/null 2>&1 <<<"$body"; then
    printf '{"format":"non_json"}'
    return 0
  fi
  # Wrap every field access in try-catch so that bare scalars (numbers, strings, booleans, arrays)
  # never produce "Cannot index X with string 'key'" diagnostics that would obscure the real cause.
  jq -c '
    def safe_string(value):
      try (if (value | type) == "string" and (value | test("^[A-Za-z0-9_-]{1,64}$")) then value else null end)
      catch null;
    def safe_top_level(field):
      try (.[field]) catch null;
    {
      format: "json",
      input_type: (try (type) catch "unknown"),
      code: (try (if (.code | type) == "number" then .code else null end) catch null),
      error_code: safe_string(safe_top_level("error_code")),
      status: safe_string(safe_top_level("status")),
      data_type: (
        try (
          if (.data | type) == "string" then "present"
          elif (.data | type) != "null" then (.data | type)
          else null end
        ) catch null
      ),
      transport_error: (try (if .transport_error == true then true else null end) catch null)
    }
  ' <<<"$body" 2>/dev/null || printf '{"format":"non_json"}'
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
  if [[ -n "$LAST_OPERATION_LABEL" ]]; then
    printf 'Last operation: %s %s; HTTP=%s\n' "$LAST_METHOD" "$LAST_OPERATION_LABEL" "${LAST_HTTP_CODE:-n/a}" >&2
    printf 'Safe response context: %s\n' "$(safe_response_fields "$LAST_RESPONSE_BODY")" >&2
    record "last_operation_origin" "$LAST_OPERATION_ORIGIN"
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
  LAST_METHOD="$method"
  LAST_OPERATION_LABEL="$(safe_url_label_from_value "$url")"
  LAST_OPERATION_ORIGIN="$(url_origin_from_value "$url")"
  LAST_STEP="request $method $LAST_OPERATION_LABEL"
  LAST_TRANSPORT_ERROR=false
  local curl_arguments=(
    -sS
    --max-time "$HTTP_TIMEOUT_SECONDS"
    -X "$method"
    -H 'Cache-Control: no-store'
    -o "$LAST_BODY_FILE"
    -w '%{http_code}'
  )
  if [[ -n "$body" ]]; then
    curl_arguments+=('-H' 'Content-Type: application/json' '--data' "$body")
  fi
  case "$authentication" in
    cookie)
      if [[ -n "${MANAGER_AUTH_COOKIE:-}" ]]; then
        curl_arguments+=('-H' "Cookie: $MANAGER_AUTH_COOKIE")
      fi
      if [[ -n "${MANAGER_AUTH_TOKEN:-}" ]]; then
        curl_arguments+=('-H' "Authorization: Bearer $MANAGER_AUTH_TOKEN")
      fi
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

  LAST_HTTP_CODE="$(curl "${curl_arguments[@]}" 2>"$CURL_ERROR_FILE")" || {
    LAST_RESPONSE_BODY='{"transport_error":true}'
    LAST_TRANSPORT_ERROR=true
    fail_now "request failed during $LAST_STEP"
  }
  LAST_RESPONSE_BODY="$(cat "$LAST_BODY_FILE")"
  if ! [[ "$LAST_HTTP_CODE" =~ ^[0-9]{3}$ ]]; then
    fail_now "curl did not return a valid HTTP status during $LAST_STEP"
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
  if ! curl -sS --max-time "$HTTP_TIMEOUT_SECONDS" \
    -H "Authorization: Bearer $DEMO_API_TOKEN" \
    -H 'X-Opskeeper-Version: v1' \
    -H 'Cache-Control: no-store' \
    -o "$LAST_BODY_FILE" \
    -w '%{time_total} %{http_code}' \
    "$MANAGER_URL$path" 1>"$CURL_ERROR_FILE" 2>/dev/null; then
    fail_now "business request failed for section $section"
  fi
  LAST_METHOD="GET"
  LAST_OPERATION_LABEL="$(safe_url_label_from_value "$MANAGER_URL$path")"
  LAST_OPERATION_ORIGIN="$(url_origin_from_value "$MANAGER_URL$path")"
  LAST_STEP="business snapshot $section"
  LAST_RESPONSE_BODY="$(cat "$LAST_BODY_FILE" 2>/dev/null || true)"
  local business_meta_line
  business_meta_line="$(cat "$CURL_ERROR_FILE" 2>/dev/null || true)"
  # curl -w '%{time_total} %{http_code}' writes a single line; tokens are split on the last whitespace.
  BUSINESS_LATENCY_SECONDS="${business_meta_line% *}"
  LAST_HTTP_CODE="${business_meta_line##* }"
  if ! [[ "$LAST_HTTP_CODE" =~ ^[0-9]{3}$ ]]; then
    fail_now "curl did not return a valid HTTP status during business snapshot $section"
  fi
  if ! [[ "$BUSINESS_LATENCY_SECONDS" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
    BUSINESS_LATENCY_SECONDS="0"
  fi
  BUSINESS_LATENCY_MS="$(awk -v seconds="$BUSINESS_LATENCY_SECONDS" 'BEGIN { printf "%.0f", seconds * 1000 }')"
  # Tolerate two demo business response shapes:
  #   envelope: {"code":200,"message":"success","data":{...}}
  #   bare object: {"section":"...","value":"...","detail":"...","latency_ms":N,"generated_at":"..."}
  BUSINESS_BODY="$(jq -c 'if (.data | type) == "object" then .data else . end' <<<"$LAST_RESPONSE_BODY" 2>/dev/null || printf '{}')"
  BUSINESS_VALUE="$(jq -r '.value // ""' <<<"$BUSINESS_BODY" 2>/dev/null || printf '')"
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
  local curl_arguments=(-sS --max-time "$HTTP_TIMEOUT_SECONDS" -G --data-urlencode "query=$query")
  if [[ -n "${PROMETHEUS_TOKEN:-}" ]]; then
    curl_arguments+=(-H "Authorization: Bearer $PROMETHEUS_TOKEN")
  fi
  if [[ -n "${PROMETHEUS_COOKIE:-}" ]]; then
    curl_arguments+=(-H "Cookie: $PROMETHEUS_COOKIE")
  fi
  curl_arguments+=(-o "$LAST_BODY_FILE" -w '%{http_code}' "$PROMETHEUS_URL/api/v1/query")
  LAST_METHOD="GET"
  LAST_OPERATION_LABEL="$(safe_url_label_from_value "$PROMETHEUS_URL/api/v1/query")"
  LAST_OPERATION_ORIGIN="$(url_origin_from_value "$PROMETHEUS_URL/api/v1/query")"
  LAST_STEP="Prometheus query $label"
  PROMETHEUS_VALUE=""
  if ! LAST_HTTP_CODE="$(curl "${curl_arguments[@]}" 2>"$CURL_ERROR_FILE")"; then
    LAST_RESPONSE_BODY='{"transport_error":true}'
    LAST_TRANSPORT_ERROR=true
    fail_now "Prometheus query failed for $label"
  fi
  LAST_RESPONSE_BODY="$(cat "$LAST_BODY_FILE" 2>/dev/null || true)"
  if ! [[ "$LAST_HTTP_CODE" =~ ^[0-9]{3}$ ]]; then
    fail_now "curl did not return a valid HTTP status during Prometheus query $label"
  fi
  expect_json '.status == "success"' "Prometheus query $label"
  # Tolerate two Prometheus response shapes:
  #   envelope: {"status":"success","data":{"resultType":"vector","result":[{"value":[...,"0.95"]}]}}
  #   bare result value: "0.95" (only valid in degenerate test stubs).
  PROMETHEUS_VALUE="$(jq -r '
    if (.data.result | type) == "array" then
      (.data.result[0].value[1] // "")
    elif (. | type) == "string" then .
    else ""
    end
  ' <<<"$LAST_RESPONSE_BODY" 2>/dev/null || printf '')"
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

prometheus_expected_value() {
  local metric="$1"
  local query="$2"
  local expected="$3"
  if ! prometheus_query "$query" "$metric"; then
    return 1
  fi
  awk -v value="$PROMETHEUS_VALUE" -v expected="$expected" 'BEGIN { exit !(value == expected) }'
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

wait_for_proposal_bound_recovery() {
  local timeout_seconds="$1"
  local deadline=$((SECONDS + timeout_seconds))
  local repair_dispatched_observed=false
  while (( SECONDS < deadline )); do
    scenario_read "$SCENARIO_IDEMPOTENCY_KEY"
    printf 'scenario status: %s; repair_dispatched_observed=%s\n' "$SCENARIO_READ_STATUS" "$repair_dispatched_observed"
    if [[ "$SCENARIO_READ_STATUS" == "repair_dispatched" ]]; then
      repair_dispatched_observed=true
      record "repair_dispatched_observed" "true"
    elif [[ "$SCENARIO_READ_STATUS" == "recovered" ]]; then
      if [[ "$repair_dispatched_observed" != true ]]; then
        request GET "$MANAGER_URL/api/v1/incidents/$(urlencode "$SCENARIO_READ_INCIDENT_ID")/archive?tenant_id=$(urlencode "$ARCHIVE_TENANT_ID")" "" cookie
        if [[ "$LAST_HTTP_CODE" == 200 ]] && jq -e '[.data.timeline[]? | select(.event_type == "recommendation.approved" and .status == "approved")] | length > 0' >/dev/null <<<"$LAST_RESPONSE_BODY" && jq -e '[.data.timeline[]? | select(.event_type == "action.executed" and .status == "executed")] | length > 0' >/dev/null <<<"$LAST_RESPONSE_BODY"; then
          repair_dispatched_observed=true
          record "repair_dispatched_observed" "true"
          record "repair_dispatched_evidence" "authority_archive"
        else
          fail_now "recovered was observed without an explicit repair_dispatched transition; TTL or unbound recovery is not accepted"
        fi
      fi
      return 0
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

record "mode" "$MODE"
record "manager_origin" "$(configured_url_origin MANAGER_URL)"
record "home_origin" "$(configured_url_origin HOME_URL)"
record "teams_origin" "$(configured_url_origin TEAMS_URL)"
record "rooms_origin" "$(configured_url_origin ROOMS_URL)"
record "opskeeper_origin" "$(configured_url_origin OPSKEEPER_URL)"
record "expected_manager_version" "$EXPECTED_MANAGER_VERSION"
record "expected_plugin_version" "$EXPECTED_PLUGIN_VERSION"
record "plugin_health_origin" "$(configured_url_origin PLUGIN_HEALTH_URL)"
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
  local domain_url="${!domain_variable}"
  local max_attempts=3
  local attempt
  for (( attempt = 1; attempt <= max_attempts; attempt++ )); do
    LAST_STEP="domain check $domain_variable (attempt ${attempt}/${max_attempts})"
    LAST_METHOD="GET"
    LAST_OPERATION_LABEL="$(safe_url_label_from_value "$domain_url")"
    LAST_OPERATION_ORIGIN="$(url_origin_from_value "$domain_url")"
    LAST_TRANSPORT_ERROR=false
    if ! LAST_HTTP_CODE="$(curl -sS --max-time "$HTTP_TIMEOUT_SECONDS" \
      -X GET -H 'Cache-Control: no-store' \
      -o "$LAST_BODY_FILE" -w '%{http_code}' \
      "$domain_url" 2>"$CURL_ERROR_FILE")"; then
      LAST_RESPONSE_BODY='{"transport_error":true}'
      LAST_TRANSPORT_ERROR=true
      LAST_HTTP_CODE="000"
    else
      LAST_RESPONSE_BODY="$(cat "$LAST_BODY_FILE" 2>/dev/null || true)"
      if ! [[ "$LAST_HTTP_CODE" =~ ^[0-9]{3}$ ]]; then
        LAST_TRANSPORT_ERROR=true
        LAST_HTTP_CODE="000"
      fi
    fi
    # Any valid HTTP status that is not 200 fails immediately without retry.
    if [[ "$LAST_HTTP_CODE" != "000" && "$LAST_HTTP_CODE" != "200" ]]; then
      record "$result_key" "$LAST_HTTP_CODE"
      fail_now "$domain_variable did not return HTTP 200"
    fi
    if [[ "$LAST_HTTP_CODE" == "200" ]]; then
      record "$result_key" "$LAST_HTTP_CODE"
      return 0
    fi
    # Only transport_error / HTTP=000 reaches here. Retry up to max_attempts.
    if (( attempt < max_attempts )); then
      printf 'domain check %s: transport error (attempt %d/%d); retrying in 2s\n' \
        "$domain_variable" "$attempt" "$max_attempts" >&2
      sleep 2
    fi
  done
  record "$result_key" "$LAST_HTTP_CODE"
  fail_now "$domain_variable did not return HTTP 200 after ${max_attempts} transport-error attempts"
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
  if ! jq -e --arg section "$section" '.section == $section' <<<"$BUSINESS_BODY" >/dev/null; then
    fail_now "baseline business response $section is invalid"
  fi
  record "baseline_${section}_http" "$LAST_HTTP_CODE"
  record_number "baseline_${section}_latency_ms" "$BUSINESS_LATENCY_MS"
  record "actual_${section}_value" "$BUSINESS_VALUE"
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
  record "degraded_${section}_value" "$BUSINESS_VALUE"
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
if ! prometheus_expected_value "pool active connections" \
  "max(opskeeper_pool_fixture_active_connections{pool_manifest_id=\"$MANIFEST_ID\"})" 4; then
  fail_now "monitoring did not confirm 4/4 pool saturation"
fi
record_number "monitor_stressed_active_connections" "$PROMETHEUS_VALUE"
if ! prometheus_expected_value "pool capacity" \
  "max(opskeeper_pool_fixture_capacity{pool_manifest_id=\"$MANIFEST_ID\"})" 4; then
  fail_now "monitoring did not confirm initial pool capacity 4"
fi
record_number "monitor_stressed_capacity" "$PROMETHEUS_VALUE"

if ! wait_for_scenario_status awaiting_approval "$WORKFLOW_TIMEOUT_SECONDS"; then
  fail_now "scenario did not reach awaiting_approval"
fi
if ! jq -e '.data.preview_decision.eligible_for_hitl == true and (.data.preview_decision.candidate_a | length > 0) and (.data.preview_decision.candidate_b | length > 0)' <<<"$LAST_RESPONSE_BODY" >/dev/null; then
  fail_now "awaiting_approval lacks complete PASS/FAIL preview eligibility"
fi
PREVIEW_DECISION="$(jq -c '.data.preview_decision' <<<"$LAST_RESPONSE_BODY")"
record "preview_decision" "$PREVIEW_DECISION"

printf '\nACTION REQUIRED: approve only Candidate A at %s using the existing AgentTeams HITL approval surface.\n' "$(safe_url_label_from_value "$ROOMS_URL")"
printf 'The preview PASS is eligibility only. The approved repair must run through recovery.execute with the exact proposal, incident, candidate, execution, target, and fingerprint bindings.\n'
printf 'Type approve-candidate-a to confirm that the real approval and repair have been initiated: '
read -r human_confirmation
if [[ "$human_confirmation" != "approve-candidate-a" ]]; then
  fail_now "operator did not confirm real Candidate A HITL approval"
fi
record "human_hitl_confirmed" "true"

if ! wait_for_proposal_bound_recovery "$WORKFLOW_TIMEOUT_SECONDS"; then
  fail_now "scenario did not recover after HITL approval and repair"
fi
if ! wait_for_prometheus_ratio maximum "$MAX_RECOVERED_UTILIZATION" "$RECOVERY_TIMEOUT_SECONDS" "$MANIFEST_ID"; then
  fail_now "monitoring did not confirm active/capacity recovery"
fi
record_number "monitor_recovered_utilization" "$PROMETHEUS_VALUE"
if ! prometheus_expected_value "pool capacity" \
  "max(opskeeper_pool_fixture_capacity{pool_manifest_id=\"$MANIFEST_ID\"})" 8; then
  fail_now "monitoring did not confirm recovered pool capacity 8"
fi
record_number "monitor_recovered_capacity" "$PROMETHEUS_VALUE"

RECOVERED_BUSINESS_COUNT=0
for section in orders inventory audit; do
  business_probe "$section" "scenario"
  if [[ "$LAST_HTTP_CODE" != 200 ]]; then
    continue
  fi
  if jq -e --arg section "$section" '.section == $section' <<<"$BUSINESS_BODY" >/dev/null; then
    RECOVERED_BUSINESS_COUNT=$((RECOVERED_BUSINESS_COUNT + 1))
    record "recovered_${section}_http" "$LAST_HTTP_CODE"
    record_number "recovered_${section}_latency_ms" "$BUSINESS_LATENCY_MS"
    record "recovered_${section}_value" "$BUSINESS_VALUE"
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

ARCHIVE_URL="$MANAGER_URL/api/v1/incidents/$(urlencode "$INCIDENT_ID")/archive?tenant_id=$(urlencode "$ARCHIVE_TENANT_ID")"
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
