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
manager_auth_request=false
for argument in "$@"; do
  if [[ "$argument" == */api/v1/version/deployment ]]; then
    manager_auth_request=true
  fi
done
if [[ -n "${CAPTURE_MANAGER_AUTH_ARGS:-}" ]]; then
  printf '%s\n' "$@" >"$CAPTURE_MANAGER_AUTH_ARGS"
  if [[ "$manager_auth_request" == true ]]; then
    printf '%s\n' '{"error_code":"pool_exhausted","message":"response-body-secret-value","Authorization":"Bearer response-body-secret-value"}'
    printf '503\n'
    exit 0
  fi
  printf '%s\n' '{"ready":true,"checks":[]}'
  printf '200\n'
  exit 0
fi
printf '%s\n' '{"error_code":"pool_exhausted","message":"response-body-secret-value","Authorization":"Bearer response-body-secret-value"}'
printf '503\n'
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
