-- +goose Up
-- Phase 4 (demo execution): our orders to eToro, the positions cactusTrade opened, each
-- account's risk state, blocked instruments and the single-writer lease per account (D22).
-- Money in cents, prices in micros, times in Unix microseconds (0 = not set).
CREATE TABLE orders (
    request_id         VARCHAR(64)  NOT NULL PRIMARY KEY,
    account_id         VARCHAR(64)  NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    mode               VARCHAR(8)   NOT NULL,
    kind               VARCHAR(8)   NOT NULL,
    direction          VARCHAR(8)   NOT NULL DEFAULT '',
    instrument_id      BIGINT       NOT NULL DEFAULT 0,
    amount_cents       BIGINT       NOT NULL DEFAULT 0,
    entry_micros       BIGINT       NOT NULL DEFAULT 0,
    stop_micros        BIGINT       NOT NULL DEFAULT 0,
    take_profit_micros BIGINT       NOT NULL DEFAULT 0,
    position_id        BIGINT       NOT NULL DEFAULT 0,
    target_order_id    BIGINT       NOT NULL DEFAULT 0,
    etoro_order_id     BIGINT       NOT NULL DEFAULT 0,
    state              VARCHAR(20)  NOT NULL,
    reason             VARCHAR(500) NOT NULL DEFAULT '',
    sends              INTEGER      NOT NULL DEFAULT 0,
    created_at         BIGINT       NOT NULL,
    sent_at            BIGINT       NOT NULL DEFAULT 0,
    updated_at         BIGINT       NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX orders_account_state ON orders(account_id, state);
CREATE INDEX orders_created ON orders(created_at);

CREATE TABLE agent_positions (
    account_id       VARCHAR(64) NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    position_id      BIGINT      NOT NULL,
    open_request_id  VARCHAR(64) NOT NULL,
    instrument_id    BIGINT      NOT NULL,
    direction        VARCHAR(8)  NOT NULL,
    opened_at        BIGINT      NOT NULL,
    closed_at        BIGINT      NOT NULL DEFAULT 0,
    close_request_id VARCHAR(64) NOT NULL DEFAULT '',
    PRIMARY KEY (account_id, position_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX agent_positions_open ON agent_positions(account_id, closed_at);

-- version: optimistic concurrency (a stale write fails, so a kill is never overwritten).
CREATE TABLE risk_state (
    account_id          VARCHAR(64)  NOT NULL PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    risk_day            VARCHAR(10)  NOT NULL DEFAULT '',
    start_of_day_equity BIGINT       NOT NULL DEFAULT 0,
    high_water_mark     BIGINT       NOT NULL DEFAULT 0,
    day_stopped         TINYINT(1)   NOT NULL DEFAULT 0,
    killed              TINYINT(1)   NOT NULL DEFAULT 0,
    kill_reason         VARCHAR(500) NOT NULL DEFAULT '',
    killed_at           BIGINT       NOT NULL DEFAULT 0,
    version             BIGINT       NOT NULL DEFAULT 0,
    updated_at          BIGINT       NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE instrument_blocks (
    account_id    VARCHAR(64)  NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    instrument_id BIGINT       NOT NULL,
    reason        VARCHAR(500) NOT NULL DEFAULT '',
    at            BIGINT       NOT NULL,
    PRIMARY KEY (account_id, instrument_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE agent_leases (
    account_id VARCHAR(64) NOT NULL PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    holder     VARCHAR(64) NOT NULL,
    expires_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE agent_leases;
DROP TABLE instrument_blocks;
DROP TABLE risk_state;
DROP TABLE agent_positions;
DROP TABLE orders;
