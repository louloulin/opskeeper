CREATE TABLE repair_preview_accounts (
    customer_id TEXT PRIMARY KEY,
    balance BIGINT NOT NULL
);
CREATE TABLE repair_preview_pool_settings (
    id BIGINT PRIMARY KEY,
    pool_size INTEGER NOT NULL
);
CREATE TABLE repair_preview_sessions (
    customer_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    PRIMARY KEY (customer_id, session_id)
);
INSERT INTO repair_preview_accounts (customer_id, balance) VALUES
    ('customer-a', 1000), ('customer-b', 2000), ('customer-c', 3000);
INSERT INTO repair_preview_pool_settings (id, pool_size) VALUES (1, 20);
INSERT INTO repair_preview_sessions (customer_id, session_id) VALUES
    ('customer-a', 'session-a'), ('customer-b', 'session-b'), ('customer-c', 'session-c');
CREATE TABLE repair_preview_target_identity (
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
        '0123456789abcdef0123456789abcdef',
        'sha256:db905b8f98c631212336b736f92d80b2a3040a75a44554687cfc782d39c31cc4'
    );
