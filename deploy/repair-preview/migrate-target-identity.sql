CREATE TABLE IF NOT EXISTS repair_preview_target_identity (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    scenario_id TEXT NOT NULL,
    target_fingerprint TEXT NOT NULL,
    workload_fingerprint TEXT NOT NULL
);
INSERT INTO repair_preview_target_identity
    (singleton, scenario_id, target_fingerprint, workload_fingerprint)
VALUES
    (
        TRUE,
        'pg-pool-exhaustion',
        'sha256:db905b8f98c631212336b736f92d80b2a3040a75a44554687ffc782d39c31cc4',
        'sha256:db905b8f98c631212336b736f92d80b2a3040a75a44554687ffc782d39c31cc4'
    )
ON CONFLICT (singleton) DO NOTHING;
