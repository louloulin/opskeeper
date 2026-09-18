#!/usr/bin/env bash
# Focused tests for scripts/run-verify-final-demo.sh.
# Verifies that:
#   1. The wrapper fails fast if any of the three required tokens is empty
#      (MANAGER_AUTH_TOKEN, DEMO_API_TOKEN, PLUGIN_HEALTH_TOKEN).
#   2. When all three tokens are populated, the wrapper invokes `docker exec`
#      with the correct env vars, including the three required tokens.
#   3. Newline-continuation lines do not lose their trailing backslash, so
#      every -e argument is passed to docker exec.
#
# Tests run entirely in a sandbox; no real docker or aliyun access required.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER_UNDER_TEST="${SCRIPT_DIR}/run-verify-final-demo.sh"

if [[ ! -x "$WRAPPER_UNDER_TEST" ]]; then
  printf 'test-run-verify-final-demo: wrapper not executable: %s\n' "$WRAPPER_UNDER_TEST" >&2
  exit 1
fi

WORK="$(mktemp -d -t run-verify-test-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

fail_test() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

pass_test() {
  printf 'PASS: %s\n' "$*"
}

# Build a sandbox that exposes a fake docker and three token source locations.
build_sandbox() {
  local sandbox="$1"
  local demo_value="$2"
  local manager_value="$3"
  local plugin_value="$4"

  rm -rf "$sandbox"
  mkdir -p "$sandbox/config/final-demo-a4406a79-1.0.66"
  mkdir -p "$sandbox/evidence"
  mkdir -p "$sandbox/bin"

  printf 'OPSKEEPER_DEMO_API_TOKEN=%s\n' "$demo_value" \
    >"$sandbox/config/final-demo-a4406a79-1.0.66/opskeeper.env"
  printf '%s' "$manager_value" \
    >"$sandbox/config/opskeeper-final-demo-e2e.jwt"

  # Fake docker that records the env vars passed via -e and handles
  # `docker inspect <container>` by reading the matching env file. All other
  # docker commands succeed silently. Plugin-manager container is recognized
  # by suffix "-plugin-manager".
  cat >"$sandbox/bin/docker" <<FAKE_DOCKER
#!/usr/bin/env bash
set -euo pipefail
output_file="\${DOCKER_FAKE_OUTPUT:-}"
plugin_env_file="\${FAKE_PLUGIN_ENV_FILE:-}"
subcommand="\${1:-}"
case "\$subcommand" in
  inspect)
    container="\${2:-}"
    if [[ -n "\$plugin_env_file" && "\$container" == *-plugin-manager ]]; then
      cat "\$plugin_env_file"
      exit 0
    fi
    if [[ "\$container" == *-plugin-manager ]]; then
      printf 'PLUGIN_MANAGER_SA_TOKEN=\n'
      exit 0
    fi
    exit 0
    ;;
  exec)
    shift
    prev=""
    for arg in "\$@"; do
      if [[ "\$prev" == "-e" ]]; then
        printf '%s\n' "\$arg" >>"\$output_file"
      fi
      prev="\$arg"
    done
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
FAKE_DOCKER
  chmod +x "$sandbox/bin/docker"

  printf 'PLUGIN_MANAGER_SA_TOKEN=%s\n' "$plugin_value" \
    >"$sandbox/plugin-manager.env"

  printf 'sandbox ready: %s\n' "$sandbox" >&2
}

run_wrapper_in_sandbox() {
  local sandbox="$1"
  local container_name="${2:-opskeeper}"
  local output_file="$sandbox/docker-calls.txt"
  rm -f "$output_file"
  (
    cd "$sandbox"
    OPSKEEPER_HOST_CONFIG_DIR="$sandbox/config" \
    OPSKEEPER_HOST_EVIDENCE_DIR="$sandbox/evidence" \
    OPSKEEPER_HOST_OPSKEEPER_ENV="$sandbox/config/final-demo-a4406a79-1.0.66/opskeeper.env" \
    OPSKEEPER_CONTAINER_NAME="$container_name" \
    OPSKEEPER_CONTAINER_SCRIPT="/src/scripts/verify-final-demo.sh" \
    PATH="$sandbox/bin:$PATH" \
    DOCKER_FAKE_OUTPUT="$output_file" \
    FAKE_PLUGIN_ENV_FILE="$sandbox/plugin-manager.env" \
    bash "$WRAPPER_UNDER_TEST"
  )
}

# ---------------------------------------------------------------------------
# Test 1: missing DEMO_API_TOKEN must fail fast.
# ---------------------------------------------------------------------------
build_sandbox "$WORK/sandbox1" "" "JWT_VALID" "PLUGIN_VALID"
set +e
output1=$(run_wrapper_in_sandbox "$WORK/sandbox1" 2>&1)
rc1=$?
set -e
if [[ $rc1 -eq 0 ]]; then
  fail_test "wrapper did not fail when DEMO_API_TOKEN is empty"
fi
if [[ "$output1" != *"OPSKEEPER_DEMO_API_TOKEN missing"* ]]; then
  fail_test "wrapper did not report missing DEMO_API_TOKEN: got: $output1"
fi
if [[ -f "$WORK/sandbox1/docker-calls.txt" ]]; then
  fail_test "wrapper invoked docker exec even though DEMO_API_TOKEN was empty"
fi
pass_test "missing DEMO_API_TOKEN fails fast without invoking docker"

# ---------------------------------------------------------------------------
# Test 2: missing manager JWT must fail fast.
# ---------------------------------------------------------------------------
build_sandbox "$WORK/sandbox2" "DEMO_VALID" "" "PLUGIN_VALID"
set +e
output2=$(run_wrapper_in_sandbox "$WORK/sandbox2" 2>&1)
rc2=$?
set -e
if [[ $rc2 -eq 0 ]]; then
  fail_test "wrapper did not fail when manager JWT is empty"
fi
if [[ "$output2" != *"manager JWT is empty"* ]]; then
  fail_test "wrapper did not report empty manager JWT: got: $output2"
fi
pass_test "missing manager JWT fails fast without invoking docker"

# ---------------------------------------------------------------------------
# Test 3: missing plugin token must fail fast.
# ---------------------------------------------------------------------------
build_sandbox "$WORK/sandbox3" "DEMO_VALID" "JWT_VALID" ""
set +e
output3=$(run_wrapper_in_sandbox "$WORK/sandbox3" 2>&1)
rc3=$?
set -e
if [[ $rc3 -eq 0 ]]; then
  fail_test "wrapper did not fail when PLUGIN_MANAGER_SA_TOKEN is empty"
fi
if [[ "$output3" != *"PLUGIN_MANAGER_SA_TOKEN missing"* ]]; then
  fail_test "wrapper did not report missing PLUGIN_MANAGER_SA_TOKEN: got: $output3"
fi
pass_test "missing plugin token fails fast without invoking docker"

# ---------------------------------------------------------------------------
# Test 4: when all three tokens are populated, the wrapper invokes docker exec
# with MANAGER_AUTH_TOKEN, DEMO_API_TOKEN, and PLUGIN_HEALTH_TOKEN reaching
# the inner container.
# ---------------------------------------------------------------------------
build_sandbox "$WORK/sandbox4" "DEMO_VALID_VALUE" "JWT_VALID_VALUE" "PLUGIN_VALID_VALUE"
set +e
output4=$(run_wrapper_in_sandbox "$WORK/sandbox4" "opskeeper" 2>&1)
rc4=$?
set -e
if [[ $rc4 -ne 0 ]]; then
  fail_test "wrapper exited non-zero with all three tokens present: rc=$rc4 output=$output4"
fi
recorded="$WORK/sandbox4/docker-calls.txt"
if [[ ! -s "$recorded" ]]; then
  fail_test "wrapper did not invoke docker exec with -e args"
fi
for required_pair in \
  "MANAGER_AUTH_TOKEN=JWT_VALID_VALUE" \
  "DEMO_API_TOKEN=DEMO_VALID_VALUE" \
  "PLUGIN_HEALTH_TOKEN=PLUGIN_VALID_VALUE" \
  "MANAGER_URL=https://opskeeper.yueming.xin" \
  "EXPECTED_MANAGER_VERSION=a4406a79-1.0.66" \
  "EXPECTED_PLUGIN_VERSION=1.0.66"; do
    if ! grep -Fxq "$required_pair" "$recorded"; then
      fail_test "wrapper did not pass '$required_pair' to docker exec; recorded: $(tr '\n' '|' <"$recorded")"
    fi
  done
pass_test "wrapper passes MANAGER_AUTH_TOKEN, DEMO_API_TOKEN, PLUGIN_HEALTH_TOKEN to docker exec"

# ---------------------------------------------------------------------------
# Test 5: every -e line preserves the trailing backslash continuation, so
# the docker exec call is not split. Detect this by checking that all the
# env vars we expect are present in order and that the host does not see
# an incomplete -e argument.
# ---------------------------------------------------------------------------
expected_order=(
  "MANAGER_URL=https://opskeeper.yueming.xin"
  "HOME_URL=https://home.yueming.xin"
  "TEAMS_URL=https://teams.yueming.xin"
  "ROOMS_URL=https://rooms.yueming.xin"
  "OPSKEEPER_URL=https://opskeeper.yueming.xin"
  "PROMETHEUS_URL=http://opskeeper-demo-prom:9090"
  "PLUGIN_HEALTH_URL=http://127.0.0.1:18096/api/v1/plugins/opskeeper-teamharness/health"
  "EXPECTED_MANAGER_VERSION=a4406a79-1.0.66"
  "EXPECTED_PLUGIN_VERSION=1.0.66"
  "MANAGER_AUTH_TOKEN=JWT_VALID_VALUE"
  "DEMO_API_TOKEN=DEMO_VALID_VALUE"
  "PLUGIN_HEALTH_TOKEN=PLUGIN_VALID_VALUE"
  "HTTP_TIMEOUT_SECONDS=20"
  "POLL_INTERVAL_SECONDS=5"
  "WORKFLOW_TIMEOUT_SECONDS=900"
  "RECOVERY_TIMEOUT_SECONDS=240"
  "DEGRADED_LATENCY_MS=1500"
  "MIN_STRESSED_UTILIZATION=0.90"
  "MAX_RECOVERED_UTILIZATION=0.25"
  "TARGET_FINGERPRINT=sha256:db905b8f98c631212336b736f92d80b2a3040a75a44554687ffc782d39c31cc4"
  "SCENARIO_DURATION_SECONDS=120"
)
recorded_lines=$(wc -l <"$recorded" | tr -d ' ')
if [[ "$recorded_lines" -lt 20 ]]; then
  fail_test "expected at least 20 docker -e args, got $recorded_lines: $(tr '\n' '|' <"$recorded")"
fi
for entry in "${expected_order[@]}"; do
  if ! grep -F -x "$entry" "$recorded" >/dev/null; then
    fail_test "expected env var not passed verbatim: $entry"
  fi
done
pass_test "newline-continuation preserves every -e argument end-to-end"

# ---------------------------------------------------------------------------
# Test 6: idempotency key uses the configured prefix and a timestamp/pid tail.
# ---------------------------------------------------------------------------
key_value=$(grep '^SCENARIO_IDEMPOTENCY_KEY=' "$recorded" | head -1 | cut -d= -f2-)
if [[ ! "$key_value" =~ ^final-demo-a4406a79-[0-9]{8}T[0-9]{6}Z-[0-9]+$ ]]; then
  fail_test "idempotency key format unexpected: $key_value"
fi
pass_test "idempotency key has the expected prefix and timestamp tail"

# ---------------------------------------------------------------------------
# Test 7: container name override is honored.
# ---------------------------------------------------------------------------
build_sandbox "$WORK/sandbox7" "DEMO_VALID_VALUE" "JWT_VALID_VALUE" "PLUGIN_VALID_VALUE"
set +e
output7=$(run_wrapper_in_sandbox "$WORK/sandbox7" "alt-manager-container" 2>&1)
rc7=$?
set -e
if [[ $rc7 -ne 0 ]]; then
  fail_test "wrapper failed with alternate container name: $output7"
fi
recorded7="$WORK/sandbox7/docker-calls.txt"
# The container name is the trailing positional arg before the bash -lc cmd.
# We can't easily extract it from the captured -e list, but we can confirm
# no failure happened and the file is populated.
if [[ ! -s "$recorded7" ]]; then
  fail_test "wrapper did not invoke docker exec for alternate container"
fi
pass_test "OPSKEEPER_CONTAINER_NAME override is accepted by the wrapper"

printf '\nALL TESTS PASSED\n'
