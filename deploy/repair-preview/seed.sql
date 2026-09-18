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
