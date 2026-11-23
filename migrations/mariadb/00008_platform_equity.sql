-- +goose Up
-- R17/R18 (owner's decision 2026-11-23): accounts get a platform (eToro, the default, or
-- Trading 212, which is view-only), and every account's equity is recorded every 15 minutes
-- for the overview chart (kept 400 days). Amounts are minor units of the account currency.
ALTER TABLE accounts ADD COLUMN platform VARCHAR(16) NOT NULL DEFAULT 'etoro';

CREATE TABLE equity_snapshots (
    account_id VARCHAR(64) NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    at         BIGINT      NOT NULL,
    currency   VARCHAR(8)  NOT NULL,
    cash       BIGINT      NOT NULL,
    equity     BIGINT      NOT NULL,
    PRIMARY KEY (account_id, at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX equity_snapshots_at ON equity_snapshots(at);

-- +goose Down
DROP TABLE equity_snapshots;
ALTER TABLE accounts DROP COLUMN platform;
