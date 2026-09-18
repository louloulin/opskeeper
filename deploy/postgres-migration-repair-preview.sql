BEGIN;

CREATE TABLE IF NOT EXISTS repair_preview_runs (
    id UUID PRIMARY KEY,
    run_id TEXT NOT NULL,
    tenant_id TEXT NOT NULL,
    incident_id TEXT NOT NULL,
    branch_prefix TEXT NOT NULL,
    seed_fingerprint TEXT NOT NULL,
    workload_fingerprint TEXT NOT NULL,
    workload_revision TEXT NOT NULL,
    controlled_load BOOLEAN NOT NULL,
    isolation_boundary TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    error_summary TEXT NOT NULL DEFAULT '',
    artifact_ref TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, incident_id, run_id)
);

CREATE TABLE IF NOT EXISTS repair_preview_candidates (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    incident_id TEXT NOT NULL,
    candidate_id TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    action TEXT NOT NULL,
    change_summary TEXT NOT NULL,
    branch TEXT NOT NULL,
    result_checksum TEXT NOT NULL,
    consistent BOOLEAN NOT NULL,
    average_latency_ms DOUBLE PRECISION NOT NULL,
    median_latency_ms DOUBLE PRECISION NOT NULL,
    p95_latency_ms DOUBLE PRECISION NOT NULL,
    sample_count INTEGER NOT NULL,
    tps DOUBLE PRECISION NOT NULL,
    error_count INTEGER NOT NULL,
    write_impact TEXT NOT NULL,
    storage_delta_bytes BIGINT NOT NULL,
    business_probe_pass BOOLEAN NOT NULL,
    decision TEXT NOT NULL,
    rejection_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, candidate_id)
);

CREATE INDEX IF NOT EXISTS idx_repair_preview_runs_incident
    ON repair_preview_runs (tenant_id, incident_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_repair_preview_candidates_run
    ON repair_preview_candidates (run_id, candidate_id);

COMMIT;
