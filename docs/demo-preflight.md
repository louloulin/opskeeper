# Demo environment preflight

`scripts/demo_preflight.sh` provides a read-only readiness gate for an
OpsKeeper + AgentTeams demo deployment.

## What it checks

- Public HTTPS routing and certificate validity for Dashboard, Element Web, and
  OpsKeeper Web.
- Required container state, health status, restart counts, disk capacity, and
  recent panic/fatal logs.
- OpsKeeper liveness and readiness, including database, Redis, and worker probes.
- AgentTeams manager and plugin-manager health.
- Plugin Installer and TeamHarness static manifests, bundle reachability,
  foreground contrast tokens, manager registration, and worker synchronization.

The script intentionally does not send chat messages, approve proposals, mutate
incidents, restart containers, or change configuration.

## Run

Create a private env file outside the repository:

```bash
cat > /secure/path/demo-preflight.env <<'EOF'
DEMO_SSH_HOST=demo-host
TEAMS_URL=https://teams.example.com
ROOMS_URL=https://rooms.example.com
OPSKEEPER_PUBLIC_URL=https://opskeeper.example.com
EXPECTED_INSTALLER_VERSION=1.4.3
EXPECTED_TEAMHARNESS_VERSION=1.0.55
EOF
```

Run:

```bash
DEMO_PREFLIGHT_ENV=/secure/path/demo-preflight.env \
  scripts/demo_preflight.sh
```

Exit code `0` means no blocking check failed. Warnings should be reviewed before
the presentation. Exit code `1` means the environment is not ready.

After a successful run, perform one manual browser login and open the intended
room to verify the final human-facing experience.
