-- +goose Up
-- Phase 7 (PLAN 7.12): the real-mode gate of each real-money account. state is 'locked' or
-- 'armed'; an armed gate names the linked demo account whose history is the evidence (JSON
-- list of qualifying risk days), who armed it and when it expires (OD24). version:
-- optimistic concurrency (a stale write fails, so a disarm is never overwritten by an older
-- arm). Times in Unix microseconds (0 = not set). This build never stores an armed gate:
-- real trading is not enabled (PLAN 7.18).
CREATE TABLE real_gates (
    account_id      VARCHAR(64)  NOT NULL PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    state           VARCHAR(8)   NOT NULL,
    demo_account_id VARCHAR(64)  NOT NULL DEFAULT '',
    armed_at        BIGINT       NOT NULL DEFAULT 0,
    armed_by        VARCHAR(64)  NOT NULL DEFAULT '',
    expires_at      BIGINT       NOT NULL DEFAULT 0,
    evidence        TEXT         NOT NULL,
    disarmed_at     BIGINT       NOT NULL DEFAULT 0,
    disarmed_by     VARCHAR(64)  NOT NULL DEFAULT '',
    disarm_reason   VARCHAR(500) NOT NULL DEFAULT '',
    version         BIGINT       NOT NULL DEFAULT 0,
    updated_at      BIGINT       NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The demo evidence reads a window of orders and positions per account.
CREATE INDEX orders_account_created ON orders(account_id, created_at);
CREATE INDEX agent_positions_opened ON agent_positions(account_id, opened_at);

-- +goose Down
DROP INDEX agent_positions_opened ON agent_positions;
DROP INDEX orders_account_created ON orders;
DROP TABLE real_gates;
