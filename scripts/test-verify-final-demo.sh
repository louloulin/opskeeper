#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script_under_test="$repository_root/scripts/verify-final-demo.sh"
work_directory="$(mktemp -d)"

cleanup() {
  rm -rf "$work_directory"
}
trap cleanup EXIT

fail_test() {
  printf 'test-verify-final-demo: FAIL: %s\n' "$1" >&2
  exit 1
}

base_environment=(
  MANAGER_URL=https://manager.example
  HOME_URL=https://home.example
  TEAMS_URL=https://teams.example
  ROOMS_URL=https://rooms.example
  OPSKEEPER_URL=https://opskeeper.example
  EXPECTED_MANAGER_VERSION=test-manager
  EXPECTED_PLUGIN_VERSION=test-plugin
  MANAGER_AUTH_COOKIE=manager-cookie-secret
  DEMO_API_TOKEN=demo-token-secret
  SCENARIO_IDEMPOTENCY_KEY=test-idempotency-key
  TARGET_FINGERPRINT=0123456789abcdef0123456789abcdef
  ALERT_FINGERPRINT=fedcba9876543210fedcba9876543210
  SCENARIO_DURATION_SECONDS=180
  PROMETHEUS_URL=https://prometheus.example
  PLUGIN_HEALTH_URL=https://plugin-health.example/api/v1/plugins/opskeeper-teamharness/health
  PLUGIN_HEALTH_TOKEN=plugin-token-secret
)

assert_bad_threshold() {
  local variable_name="$1"
  local value="$2"
  local output_file="$work_directory/threshold-output.txt"
  if env "${base_environment[@]}" "$variable_name=$value" "$script_under_test" --dry-run >"$output_file" 2>&1; then
    fail_test "$variable_name=$value was accepted"
  fi
  grep -F "$variable_name must be a finite decimal number from 0 through 1" "$output_file" >/dev/null ||
    fail_test "$variable_name=$value did not produce the threshold failure"
}

missing_manager_auth_output_file="$work_directory/missing-manager-auth-output.txt"
if env "$(for item in "${base_environment[@]}"; do [[ "$item" != MANAGER_AUTH_COOKIE=* ]] && printf '%s\n' "$item"; done)" \
  "$script_under_test" --dry-run >"$missing_manager_auth_output_file" 2>&1; then
  fail_test 'missing Manager cookie and token was accepted'
fi
grep -F 'MANAGER_AUTH_COOKIE or MANAGER_AUTH_TOKEN is required' "$missing_manager_auth_output_file" >/dev/null ||
  fail_test 'missing Manager authentication did not produce the expected failure'

for malformed_threshold in NaN Inf -Inf abc 1.01 -0.1 ''; do
  assert_bad_threshold MIN_STRESSED_UTILIZATION "$malformed_threshold"
  assert_bad_threshold MAX_RECOVERED_UTILIZATION "$malformed_threshold"
done

url_output_file="$work_directory/url-output.txt"
if env "${base_environment[@]}" \
  MANAGER_URL='https://manager.example/readyz?access_token=url-secret-value' \
  "$script_under_test" --dry-run >"$url_output_file" 2>&1; then
  fail_test 'query-bearing MANAGER_URL was accepted'
fi
if grep -F 'url-secret-value' "$url_output_file" >/dev/null; then
  fail_test 'URL validation leaked query data'
fi

fake_bin_directory="$work_directory/bin"
mkdir -p "$fake_bin_directory"
fake_curl="$fake_bin_directory/curl"
cat > "$fake_curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -euo pipefail
output_file=""
write_out_arg=""
url=""
prev=""
for arg in "$@"; do
  case "$prev" in
    -o) output_file="$arg" ;;
    -w) write_out_arg="$arg" ;;
  esac
  case "$arg" in
    http://*|https://*) url="$arg" ;;
  esac
  prev="$arg"
done

if [[ -n "${CAPTURE_MANAGER_AUTH_ARGS:-}" ]]; then
  printf '%s\n' "$@" >"$CAPTURE_MANAGER_AUTH_ARGS"
fi

# Domain-retry simulation: tests can force transport errors or HTTP 500 on a
# specific domain root to exercise check_domain's bounded retry. The counter
# only increments when the URL is an exact domain root (no path), so API paths
# like /readyz or /api/v1/... never consume retry budget.
domain_root="${url#https://}"
domain_root="${domain_root%%/*}"
domain_target="${DOMAIN_RETRY_TARGET:-rooms.example}"
domain_counter_file="${DOMAIN_RETRY_COUNTER_FILE:-}"
domain_fail_first="${DOMAIN_RETRY_FAIL_FIRST:-0}"
domain_status_500="${DOMAIN_RETRY_STATUS_500:-0}"
domain_call_index=1
if [[ "$domain_root" == "$domain_target" ]] \
  && [[ "$url" == "https://${domain_root}/" || "$url" == "https://${domain_root}" ]]; then
  if [[ -n "$domain_counter_file" && -f "$domain_counter_file" ]]; then
    domain_call_index=$(( $(cat "$domain_counter_file" 2>/dev/null || printf '0') + 1 ))
  fi
  if [[ -n "$domain_counter_file" ]]; then
    printf '%s' "$domain_call_index" >"$domain_counter_file" 2>/dev/null || true
  fi
fi

# Pick a body/status based on the URL the script asked for. Tests can override
# either via BODY_OVERRIDE / STATUS_OVERRIDE env vars (no newlines) to simulate
# edge cases without changing the fake binary.
business_baseline_shape="${BUSINESS_BASELINE_SHAPE-envelope}"
business_degraded_shape="${BUSINESS_DEGRADED_SHAPE-envelope}"
business_degraded_status="${BUSINESS_DEGRADED_STATUS-503}"
prom_shape="${PROM_RESPONSE_SHAPE-envelope}"
# Simulate check_domain transport-error retries and HTTP 500 immediate-fail.
# Only the domain named by DOMAIN_RETRY_TARGET is affected; all other domains
# return their normal 200 response so the rest of the pipeline proceeds.
if [[ "$url" == "https://${domain_target}/" || "$url" == "https://${domain_target}" ]]; then
  if [[ "$domain_status_500" == "1" ]]; then
    if [[ -n "$output_file" ]]; then printf '%s' '{"ready":false}' >"$output_file"; fi
    printf '%s' "500"
    exit 0
  fi
  if [[ "$domain_fail_first" =~ ^[0-9]+$ ]] && (( domain_call_index <= domain_fail_first )); then
    if [[ -n "$output_file" ]]; then printf '%s' '' >"$output_file" 2>/dev/null || true; fi
    printf 'curl: (7) simulated transport error for %s call %d\n' "$url" "$domain_call_index" >&2
    exit 7
  fi
fi
case "$url" in
  */api/v1/version/deployment)
    # Default behavior matches the original fake: 503 + a body that contains
    # bearer-looking text so the sanitization test has something to scrub.
    # New tests can override BODY_OVERRIDE / STATUS_OVERRIDE to exercise the
    # refactored request() flow with valid or invalid JSON.
    default_body='{"error_code":"pool_exhausted","message":"response-body-secret-value","Authorization":"Bearer response-body-secret-value"}'
    default_status="503"
    ;;
  */api/v1/demo/scenarios/pg-pool-exhaustion/start)
    default_body='{"code":201,"message":"created","data":{"incident_id":"inc-1","scenario_id":"pg-pool-exhaustion","pool_manifest_id":"m-1"}}'
    default_status="201"
    ;;
  */api/v1/demo/scenarios/*/business/orders)
    case "$business_degraded_shape" in
      bare) default_body='{"section":"orders","value":"","detail":"","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}' ;;
      non_json) default_body='not-json-at-all' ;;
      *) default_body='{"code":503,"message":"pool_exhausted","data":{"section":"orders","value":"","detail":"","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}}' ;;
    esac
    default_status="$business_degraded_status"
    ;;
  */api/v1/demo/scenarios/*/business/inventory)
    case "$business_degraded_shape" in
      bare) default_body='{"section":"inventory","value":"","detail":"","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}' ;;
      non_json) default_body='not-json-at-all' ;;
      *) default_body='{"code":503,"message":"pool_exhausted","data":{"section":"inventory","value":"","detail":"","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}}' ;;
    esac
    default_status="$business_degraded_status"
    ;;
  */api/v1/demo/scenarios/*/business/audit)
    case "$business_degraded_shape" in
      bare) default_body='{"section":"audit","value":"","detail":"","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}' ;;
      non_json) default_body='not-json-at-all' ;;
      *) default_body='{"code":503,"message":"pool_exhausted","data":{"section":"audit","value":"","detail":"","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}}' ;;
    esac
    default_status="$business_degraded_status"
    ;;
  # Scenario read: GET /api/v1/demo/scenarios/<idempotency_key>. The path must
  # end right after the idempotency key (no further "/business/<section>" suffix),
  # so we require the trailing segment to be a non-slash character class.
  */api/v1/demo/scenarios/[^/]*)
    default_body='{"code":200,"message":"success","data":{"status":"awaiting_alert","incident_id":"inc-1","scenario_id":"pg-pool-exhaustion","pool_manifest_id":"m-1"}}'
    default_status="200"
    ;;
  */api/v1/demo/business/orders)
    case "$business_baseline_shape" in
      bare) default_body='{"section":"orders","value":"1","detail":"12500","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}' ;;
      non_json) default_body='not-json-at-all' ;;
      *) default_body='{"code":200,"message":"success","data":{"section":"orders","value":"1","detail":"12500","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}}' ;;
    esac
    default_status="200"
    ;;
  */api/v1/demo/business/inventory)
    case "$business_baseline_shape" in
      bare) default_body='{"section":"inventory","value":"42","detail":"300","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}' ;;
      non_json) default_body='still-not-json' ;;
      *) default_body='{"code":200,"message":"success","data":{"section":"inventory","value":"42","detail":"300","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}}' ;;
    esac
    default_status="200"
    ;;
  */api/v1/demo/business/audit)
    case "$business_baseline_shape" in
      bare) default_body='{"section":"audit","value":"7","detail":"80","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}' ;;
      non_json) default_body='definitely-not-json' ;;
      *) default_body='{"code":200,"message":"success","data":{"section":"audit","value":"7","detail":"80","latency_ms":0,"generated_at":"2026-09-18T00:00:00Z"}}' ;;
    esac
    default_status="200"
    ;;
  */readyz)
    default_body='{"ready":true,"checks":[]}'
    default_status="200"
    ;;
  */plugins/*/health)
    default_body='{"worker":{"version":"test-plugin","loaded":true,"enabled":true,"id":"opskeeper-teamharness"},"synced":true,"diff":[]}'
    default_status="200"
    ;;
  */api/v1/query)
    case "$prom_shape" in
      bare_value) default_body='0.95' ;;
      non_json) default_body='not-json-at-all' ;;
      bad_status) default_body='{"status":"error","errorType":"bad_data","error":"parse error"}' ;;
      empty_result) default_body='{"status":"success","data":{"resultType":"vector","result":[]}}' ;;
      timeout)
        # Simulate a transport failure by exiting non-zero before writing
        # any body. The script expects a curl-level error and fail_now.
        printf '%s' "" >"$output_file" 2>/dev/null || true
        exit 28
        ;;
      *)
        # Realistic envelope: status=success + a single vector sample.
        # PROMETHEUS_RATIO overrides the value; default 0.95 (≥ 0.90 threshold).
        ratio="${PROMETHEUS_RATIO-0.95}"
        default_body="{\"status\":\"success\",\"data\":{\"resultType\":\"vector\",\"result\":[{\"metric\":{},\"value\":[1700000000,\"$ratio\"]}]}}"
        ;;
    esac
    default_status="200"
    ;;
  *)
    default_body='{"ready":true,"checks":[]}'
    default_status="200"
    ;;
esac
# URL_BODY_OVERRIDE_VERSION targets /api/v1/version/deployment specifically,
# so plugin-health, demo-business, and readyz continue to return their own
# realistic bodies even when URL_BODY_OVERRIDE_VERSION is set. BODY_OVERRIDE
# applies to any other URL that falls through the case statement.
case "$url" in
  */api/v1/version/deployment)
    url_overridden_body="${URL_BODY_OVERRIDE_VERSION-$default_body}"
    url_overridden_status="${STATUS_OVERRIDE-$default_status}" ;;
  *)
    url_overridden_body="${BODY_OVERRIDE-$default_body}"
    url_overridden_status="$default_status" ;;
esac
body="$url_overridden_body"
status_code="$url_overridden_status"

if [[ -n "$output_file" ]]; then
  printf '%s' "$body" >"$output_file"
else
  printf '%s\n' "$body"
fi
if [[ "$write_out_arg" == "%{http_code}" ]]; then
  printf '%s' "$status_code"
elif [[ "$write_out_arg" == "%{time_total} %{http_code}" ]]; then
  # Mimic curl -w '%{time_total} %{http_code}' for business_probe: synthetic
  # latency 0.001s + the status code on stdout (no body), so business_probe
  # can split body from meta the same way it does against the real curl.
  printf '0.001 %s' "$status_code"
else
  printf '%s\n' "$status_code"
fi
exit 0
FAKE_CURL
chmod +x "$fake_curl"

manager_token_capture_file="$work_directory/manager-token-capture.txt"
manager_token_output_file="$work_directory/manager-token-output.txt"
manager_token_evidence_file="$work_directory/manager-token-evidence.json"
if PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  MANAGER_AUTH_COOKIE='' \
  MANAGER_AUTH_TOKEN=manager-token-secret \
  CAPTURE_MANAGER_AUTH_ARGS="$manager_token_capture_file" \
  EVIDENCE_OUTPUT="$manager_token_evidence_file" \
  "$script_under_test" >"$manager_token_output_file" 2>&1; then
  fail_test 'Manager token request test unexpectedly passed a failed readiness request'
fi
grep -F 'Authorization: Bearer manager-token-secret' "$manager_token_capture_file" >/dev/null ||
  fail_test 'Manager token mode did not send the Bearer authorization header'
if grep -F 'Cookie: manager-cookie-secret' "$manager_token_capture_file" >/dev/null; then
  fail_test 'Manager token mode unexpectedly sent the disabled cookie header'
fi
if grep -F 'manager-token-secret' "$manager_token_output_file" "$manager_token_evidence_file" >/dev/null; then
  fail_test 'Manager token diagnostics leaked the bearer token'
fi

failure_output_file="$work_directory/failure-output.txt"
failure_evidence_file="$work_directory/failure-evidence.json"
if PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$failure_evidence_file" \
  "$script_under_test" >"$failure_output_file" 2>&1; then
  fail_test 'sanitization test unexpectedly passed a failed readiness request'
fi

for secret_value in manager-cookie-secret demo-token-secret response-body-secret-value; do
  if grep -F "$secret_value" "$failure_output_file" >/dev/null; then
    fail_test "failure diagnostics leaked $secret_value"
  fi
  if [[ -f "$failure_evidence_file" ]] && grep -F "$secret_value" "$failure_evidence_file" >/dev/null; then
    fail_test "failure evidence leaked $secret_value"
  fi
done

jq -e '.last_operation_origin == "https://manager.example"' "$failure_evidence_file" >/dev/null ||
  fail_test 'failure evidence did not use the sanitized origin'

# ---------------------------------------------------------------------------
# New request() flow: HTTP 200 + correct JSON body containing both
# .manager_version and .plugin.version must let the script PASS the version
# readback check. The next assertion (plugin runtime health) is allowed to
# fail later; what we verify here is that the failure is NOT at the version
# readback stage and NOT leaking secrets.
# ---------------------------------------------------------------------------
valid_version_evidence_file="$work_directory/valid-version-evidence.json"
valid_version_output_file="$work_directory/valid-version-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$valid_version_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  "$script_under_test" >"$valid_version_output_file" 2>&1
rc_valid=$?
set -e
if [[ $rc_valid -eq 0 ]]; then
  fail_test 'script unexpectedly succeeded with fake curl (no plugin runtime to satisfy later checks)'
fi
if grep -F 'version readback returned an invalid response' "$valid_version_output_file" >/dev/null; then
  fail_test 'script failed at version readback despite valid 200 JSON'
fi
if grep -F 'manager-token-secret' "$valid_version_output_file" "$valid_version_evidence_file" >/dev/null; then
  fail_test 'valid-version run leaked the manager bearer token in diagnostics or evidence'
fi
if [[ -f "$valid_version_evidence_file" ]]; then
  jq -e '.actual_manager_version == "test-manager" and .actual_plugin_version == "test-plugin"' \
    "$valid_version_evidence_file" >/dev/null ||
    fail_test 'valid-version evidence did not record actual manager/plugin versions'
fi

# ---------------------------------------------------------------------------
# New request() flow: HTTP 200 but body is NOT valid JSON must still FAIL
# the version readback. safe_response_fields should report format:non_json
# and the script should exit non-zero with the readback failure message.
# ---------------------------------------------------------------------------
bad_body_evidence_file="$work_directory/bad-body-evidence.json"
bad_body_output_file="$work_directory/bad-body-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$bad_body_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='this is not json at all' \
  STATUS_OVERRIDE="200" \
  "$script_under_test" >"$bad_body_output_file" 2>&1
rc_bad=$?
set -e
if [[ $rc_bad -eq 0 ]]; then
  fail_test 'script unexpectedly accepted non-JSON body at version readback'
fi
if ! grep -F 'version readback returned an invalid response' "$bad_body_output_file" >/dev/null; then
  fail_test 'script did not fail version readback on invalid JSON body'
fi
if ! grep -F 'Safe response context: {"format":"non_json"}' "$bad_body_output_file" >/dev/null; then
  fail_test 'safe_response_fields did not report format:non_json for invalid body'
fi

# ---------------------------------------------------------------------------
# business_probe envelope-vs-bare-object tolerance: HTTP 200 + envelope shape
# ({"code":200,"message":"success","data":{...}}) must produce readable
# actual_<section>_value fields for all three sections (orders/inventory/audit).
# ---------------------------------------------------------------------------
business_envelope_evidence_file="$work_directory/business-envelope-evidence.json"
business_envelope_output_file="$work_directory/business-envelope-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$business_envelope_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  "$script_under_test" >"$business_envelope_output_file" 2>&1
rc_business_envelope=$?
set -e
if [[ -f "$business_envelope_evidence_file" ]]; then
  jq -e '.actual_orders_value == "1" and .actual_inventory_value == "42" and .actual_audit_value == "7"' \
    "$business_envelope_evidence_file" >/dev/null ||
    fail_test 'envelope-shaped business responses did not produce readable actual_<section>_value fields'
fi

# ---------------------------------------------------------------------------
# business_probe bare-object shape: HTTP 200 + body without envelope wrapper
# ({"section":"...","value":"...","detail":"...","latency_ms":N,...}) must also
# produce readable actual_<section>_value fields, with no envelope dependency.
# ---------------------------------------------------------------------------
business_bare_evidence_file="$work_directory/business-bare-evidence.json"
business_bare_output_file="$work_directory/business-bare-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$business_bare_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="bare" \
  "$script_under_test" >"$business_bare_output_file" 2>&1
rc_business_bare=$?
set -e
if [[ -f "$business_bare_evidence_file" ]]; then
  jq -e '.actual_orders_value == "1" and .actual_inventory_value == "42" and .actual_audit_value == "7"' \
    "$business_bare_evidence_file" >/dev/null ||
    fail_test 'bare-object business responses did not produce readable actual_<section>_value fields'
fi

# ---------------------------------------------------------------------------
# business_probe non-JSON body: HTTP 200 with body that is NOT valid JSON must
# still FAIL at the version readback stage (the earliest check) and report
# format:non_json in safe_response_fields. It must NOT crash or report
# "Cannot index number with string 'code'" because safe_response_fields is
# hardened with try/catch.
# ---------------------------------------------------------------------------
business_bad_evidence_file="$work_directory/business-bad-evidence.json"
business_bad_output_file="$work_directory/business-bad-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$business_bad_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='this is not json at all' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="non_json" \
  "$script_under_test" >"$business_bad_output_file" 2>&1
rc_business_bad=$?
set -e
if [[ $rc_business_bad -eq 0 ]]; then
  fail_test 'script unexpectedly accepted non-JSON body at version readback (business non-JSON path)'
fi
if ! grep -F 'version readback returned an invalid response' "$business_bad_output_file" >/dev/null; then
  fail_test 'script did not fail version readback on invalid JSON body (business non-JSON path)'
fi
if ! grep -F 'Safe response context: {"format":"non_json"}' "$business_bad_output_file" >/dev/null; then
  fail_test 'safe_response_fields did not report format:non_json for invalid body (business non-JSON path)'
fi

# ---------------------------------------------------------------------------
# prometheus_query refactor: full live run that reaches wait_for_prometheus_ratio
# should record actual_prom_status="success" and actual_pool_ratio from the
# envelope-shaped /api/v1/query response. No threshold, no expect_json change.
# ---------------------------------------------------------------------------
prom_envelope_evidence_file="$work_directory/prom-envelope-evidence.json"
prom_envelope_output_file="$work_directory/prom-envelope-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$prom_envelope_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  PROMETHEUS_RATIO="0.97" \
  RECOVERY_TIMEOUT_SECONDS="30" \
  "$script_under_test" >"$prom_envelope_output_file" 2>&1
rc_prom=$?
set -e
if [[ -f "$prom_envelope_evidence_file" ]]; then
  jq -e '.baseline_orders_http == "200" and .baseline_inventory_http == "200" and .baseline_audit_http == "200"' \
    "$prom_envelope_evidence_file" >/dev/null ||
    fail_test 'prom-envelope run did not record all baseline business HTTP 200'
  jq -e '.incident_id and .scenario_id and .pool_manifest_id' \
    "$prom_envelope_evidence_file" >/dev/null ||
    fail_test 'prom-envelope run did not record scenario/incident/manifest IDs'
  jq -e '.degraded_orders_http == "503" and .business_impact_observed == "true"' \
    "$prom_envelope_evidence_file" >/dev/null ||
    fail_test 'prom-envelope run did not record degraded 503 + business impact'
fi

# ---------------------------------------------------------------------------
# prometheus_query bare-value tolerance: when /api/v1/query returns a bare
# string ("0.95"), prometheus_query must still parse a numeric ratio so
# wait_for_prometheus_ratio can complete. PROMETHEUS_RATIO=0.85 < 0.90 forces
# the script to keep polling (a few iterations). With RECOVERY_TIMEOUT_SECONDS=30
# and bare_value shape, PROMETHEUS_VALUE should be set to "0.95".
# ---------------------------------------------------------------------------
prom_bare_evidence_file="$work_directory/prom-bare-evidence.json"
prom_bare_output_file="$work_directory/prom-bare-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$prom_bare_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  PROM_RESPONSE_SHAPE="bare_value" \
  RECOVERY_TIMEOUT_SECONDS="30" \
  "$script_under_test" >"$prom_bare_output_file" 2>&1
rc_prom_bare=$?
set -e
if [[ -f "$prom_bare_evidence_file" ]]; then
  jq -e '.baseline_orders_http == "200" and .baseline_inventory_http == "200" and .baseline_audit_http == "200"' \
    "$prom_bare_evidence_file" >/dev/null ||
    fail_test 'prom-bare run did not record all baseline business HTTP 200'
  jq -e '.incident_id and .scenario_id and .pool_manifest_id' \
    "$prom_bare_evidence_file" >/dev/null ||
    fail_test 'prom-bare run did not record scenario/incident/manifest IDs'
fi

# ---------------------------------------------------------------------------
# prometheus_query non-JSON body must still FAIL the request via expect_json
# and produce format:non_json in safe_response_fields (no "Cannot index").
# ---------------------------------------------------------------------------
prom_bad_evidence_file="$work_directory/prom-bad-evidence.json"
prom_bad_output_file="$work_directory/prom-bad-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$prom_bad_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  PROM_RESPONSE_SHAPE="non_json" \
  RECOVERY_TIMEOUT_SECONDS="30" \
  "$script_under_test" >"$prom_bad_output_file" 2>&1
rc_prom_bad=$?
set -e
if [[ $rc_prom_bad -eq 0 ]]; then
  fail_test 'script unexpectedly accepted non-JSON Prometheus body'
fi
if ! grep -F 'Prometheus query pool utilization returned an invalid response' "$prom_bad_output_file" >/dev/null; then
  fail_test 'script did not fail Prometheus query on non-JSON body'
fi
if ! grep -F 'Safe response context: {"format":"non_json"}' "$prom_bad_output_file" >/dev/null; then
  fail_test 'safe_response_fields did not report format:non_json for invalid Prometheus body'
fi

# ---------------------------------------------------------------------------
# prometheus_query bad_status (status=error) must also fail the request
# because expect_json '.status == "success"' is still enforced.
# ---------------------------------------------------------------------------
prom_bad_status_evidence_file="$work_directory/prom-bad-status-evidence.json"
prom_bad_status_output_file="$work_directory/prom-bad-status-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$prom_bad_status_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  PROM_RESPONSE_SHAPE="bad_status" \
  RECOVERY_TIMEOUT_SECONDS="30" \
  "$script_under_test" >"$prom_bad_status_output_file" 2>&1
rc_prom_bs=$?
set -e
if [[ $rc_prom_bs -eq 0 ]]; then
  fail_test 'script unexpectedly accepted Prometheus status=error response'
fi
if ! grep -F 'Prometheus query pool utilization returned an invalid response' "$prom_bad_status_output_file" >/dev/null; then
  fail_test 'script did not fail Prometheus query on status=error body'
fi
if ! grep -F '"status":"error"' "$prom_bad_status_output_file" >/dev/null; then
  fail_test 'safe_response_fields did not report status=error from status=error envelope'
fi

# ---------------------------------------------------------------------------
# prometheus_query timeout: curl exits non-zero (exit 28). The script's
# LAST_TRANSPORT_ERROR path must engage and fail_now. safe_response_fields
# still renders as valid JSON because of try/catch hardening.
# ---------------------------------------------------------------------------
prom_timeout_evidence_file="$work_directory/prom-timeout-evidence.json"
prom_timeout_output_file="$work_directory/prom-timeout-output.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$prom_timeout_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  PROM_RESPONSE_SHAPE="timeout" \
  RECOVERY_TIMEOUT_SECONDS="30" \
  HTTP_TIMEOUT_SECONDS="1" \
  "$script_under_test" >"$prom_timeout_output_file" 2>&1
rc_prom_to=$?
set -e
if [[ $rc_prom_to -eq 0 ]]; then
  fail_test 'script unexpectedly accepted Prometheus transport timeout'
fi
if ! grep -F 'Prometheus query failed for pool utilization' "$prom_timeout_output_file" >/dev/null; then
  fail_test 'script did not fail_now on Prometheus transport timeout'
fi

# ---------------------------------------------------------------------------
# check_domain bounded transport-error retry: first attempt fails with a curl
# transport error, second attempt returns 200. The script must proceed past
# domain checks and record rooms_domain_http=200.
# ---------------------------------------------------------------------------
domain_retry_pass_evidence_file="$work_directory/domain-retry-pass-evidence.json"
domain_retry_pass_output_file="$work_directory/domain-retry-pass-output.txt"
domain_retry_pass_counter="$work_directory/domain-retry-pass-counter.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$domain_retry_pass_evidence_file" \
  URL_BODY_OVERRIDE_VERSION='{"manager_version":"test-manager","plugin":{"version":"test-plugin","id":"opskeeper-teamharness"}}' \
  STATUS_OVERRIDE="200" \
  BUSINESS_BASELINE_SHAPE="envelope" \
  PROM_RESPONSE_SHAPE="envelope" \
  DOMAIN_RETRY_TARGET="rooms.example" \
  DOMAIN_RETRY_FAIL_FIRST="1" \
  DOMAIN_RETRY_COUNTER_FILE="$domain_retry_pass_counter" \
  HTTP_TIMEOUT_SECONDS="5" \
  "$script_under_test" >"$domain_retry_pass_output_file" 2>&1
rc_domain_retry_pass=$?
set -e
if [[ -f "$domain_retry_pass_evidence_file" ]]; then
  jq -e '.rooms_domain_http == "200"' "$domain_retry_pass_evidence_file" >/dev/null ||
    fail_test 'domain retry pass did not record rooms_domain_http=200'
fi
if ! grep -F 'transport error (attempt 1/3); retrying in 2s' "$domain_retry_pass_output_file" >/dev/null; then
  fail_test 'domain retry pass did not log the first transport error retry'
fi

# ---------------------------------------------------------------------------
# check_domain bounded transport-error retry: all 3 attempts fail. The script
# must fail_now with the exact "after 3 transport-error attempts" message and
# record rooms_domain_http=000.
# ---------------------------------------------------------------------------
domain_retry_fail_evidence_file="$work_directory/domain-retry-fail-evidence.json"
domain_retry_fail_output_file="$work_directory/domain-retry-fail-output.txt"
domain_retry_fail_counter="$work_directory/domain-retry-fail-counter.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$domain_retry_fail_evidence_file" \
  DOMAIN_RETRY_TARGET="rooms.example" \
  DOMAIN_RETRY_FAIL_FIRST="3" \
  DOMAIN_RETRY_COUNTER_FILE="$domain_retry_fail_counter" \
  HTTP_TIMEOUT_SECONDS="5" \
  "$script_under_test" >"$domain_retry_fail_output_file" 2>&1
rc_domain_retry_fail=$?
set -e
if [[ $rc_domain_retry_fail -eq 0 ]]; then
  fail_test 'check_domain unexpectedly succeeded after 3 transport errors'
fi
if ! grep -F 'ROOMS_URL did not return HTTP 200 after 3 transport-error attempts' "$domain_retry_fail_output_file" >/dev/null; then
  fail_test 'check_domain did not fail with the exact 3-attempt transport-error message'
fi
if [[ -f "$domain_retry_fail_evidence_file" ]]; then
  jq -e '.rooms_domain_http == "000"' "$domain_retry_fail_evidence_file" >/dev/null ||
    fail_test 'check_domain 3-fail evidence did not record rooms_domain_http=000'
fi
domain_retry_fail_count="$(cat "$domain_retry_fail_counter" 2>/dev/null || printf '0')"
if [[ "$domain_retry_fail_count" != "3" ]]; then
  fail_test "check_domain 3-fail test expected exactly 3 curl calls for rooms, got $domain_retry_fail_count"
fi

# ---------------------------------------------------------------------------
# check_domain HTTP 500 immediate fail, no retry. The counter must remain at 1
# (a single curl call for the target domain) and the script must fail_now with
# the standard non-200 message (not the transport-error retry message).
# ---------------------------------------------------------------------------
domain_500_evidence_file="$work_directory/domain-500-evidence.json"
domain_500_output_file="$work_directory/domain-500-output.txt"
domain_500_counter="$work_directory/domain-500-counter.txt"
set +e
PATH="$fake_bin_directory:$PATH" env "${base_environment[@]}" \
  EVIDENCE_OUTPUT="$domain_500_evidence_file" \
  DOMAIN_RETRY_TARGET="rooms.example" \
  DOMAIN_RETRY_STATUS_500="1" \
  DOMAIN_RETRY_COUNTER_FILE="$domain_500_counter" \
  HTTP_TIMEOUT_SECONDS="5" \
  "$script_under_test" >"$domain_500_output_file" 2>&1
rc_domain_500=$?
set -e
if [[ $rc_domain_500 -eq 0 ]]; then
  fail_test 'check_domain unexpectedly succeeded on HTTP 500'
fi
if ! grep -F 'ROOMS_URL did not return HTTP 200' "$domain_500_output_file" >/dev/null; then
  fail_test 'check_domain did not fail immediately on HTTP 500'
fi
if grep -F 'transport-error attempts' "$domain_500_output_file" >/dev/null; then
  fail_test 'check_domain incorrectly used the transport-error retry message for HTTP 500'
fi
if [[ -f "$domain_500_evidence_file" ]]; then
  jq -e '.rooms_domain_http == "500"' "$domain_500_evidence_file" >/dev/null ||
    fail_test 'check_domain HTTP 500 evidence did not record rooms_domain_http=500'
fi
domain_500_count="$(cat "$domain_500_counter" 2>/dev/null || printf '0')"
if [[ "$domain_500_count" != "1" ]]; then
  fail_test "check_domain HTTP 500 test expected exactly 1 curl call for rooms, got $domain_500_count"
fi

dry_run_evidence_file="$work_directory/dry-run-evidence.json"
env "${base_environment[@]}" EVIDENCE_OUTPUT="$dry_run_evidence_file" "$script_under_test" --dry-run >/dev/null
jq -e '
  .outcome == "dry-run" and
  .manager_origin == "https://manager.example" and
  .plugin_health_origin == "https://plugin-health.example" and
  (.manager_url | not) and
  (.plugin_health_url | not)
' "$dry_run_evidence_file" >/dev/null || fail_test 'dry-run evidence contained non-origin endpoint data'

printf 'test-verify-final-demo: PASS\n'
