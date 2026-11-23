-- +goose Up
-- eToro instrument metadata and the last eligibility check (real share, long, leverage 1).
CREATE TABLE instruments (
    id                     BIGINT       NOT NULL PRIMARY KEY,
    symbol                 VARCHAR(64)  NOT NULL,
    name                   VARCHAR(255) NOT NULL,
    type                   VARCHAR(32)  NOT NULL,
    exchange_id            BIGINT       NOT NULL DEFAULT 0,
    market                 VARCHAR(8)   NOT NULL DEFAULT '',
    currency               VARCHAR(8)   NOT NULL DEFAULT '',
    eligible_real_long     TINYINT(1)     NOT NULL DEFAULT 0,
    eligibility_reason     VARCHAR(255) NOT NULL DEFAULT '',
    min_position_cents     BIGINT       NOT NULL DEFAULT 0,
    eligibility_checked_at BIGINT       NOT NULL DEFAULT 0,
    updated_at             BIGINT       NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The instruments the agent may trade (phase 3+); every entry was eligible when added.
CREATE TABLE watchlist (
    instrument_id BIGINT      NOT NULL PRIMARY KEY REFERENCES instruments(id) ON DELETE CASCADE,
    added_by      VARCHAR(64) NOT NULL,
    added_at      BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE watchlist;
DROP TABLE instruments;
