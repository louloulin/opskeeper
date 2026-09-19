# OpsKeeper ModelScope Creative Space

This read-only Gradio showcase presents the OpsKeeper product journey, architecture, safety boundary, and public-safe evidence assets. The app consumes only local Markdown and files under `../assets/evidence`; it does not connect to Manager, PostgreSQL, Matrix, AgentTeams, or any server-side write API.

## Safety claims

preview-pg + controlled fixed-load replay; passing preview only yields HITL eligibility; no PolarDB HA claim; no active-session copying claim.

## Run locally

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python app.py
```

Open `http://127.0.0.1:7860`. External entry cards open in a new window with `rel="noopener noreferrer"` and keep authentication in the original service. Do not add secrets, private endpoints, privileged clients, or write APIs to this showcase.
