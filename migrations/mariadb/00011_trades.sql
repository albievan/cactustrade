-- +goose Up
-- Phase 6: the closed trades of each account as eToro reports them (trade history, read
-- only), with whether cactusTrade opened the position (ours, OD15). Kept for as long as the
-- account exists (OD16): an accounting record, never purged; it goes with its account.
-- Money in cents, prices and units in micros, times in Unix microseconds (0 = not set).
-- fees_known = 0: eToro did not report the fees (unknown, never counted as 0).
CREATE TABLE trades (
    account_id         VARCHAR(64)  NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    position_id        BIGINT       NOT NULL,
    mode               VARCHAR(8)   NOT NULL,
    instrument_id      BIGINT       NOT NULL DEFAULT 0,
    direction          VARCHAR(8)   NOT NULL,
    leverage           INTEGER      NOT NULL DEFAULT 0,
    ours               TINYINT(1)   NOT NULL DEFAULT 0,
    open_request_id    VARCHAR(64)  NOT NULL DEFAULT '',
    opened_at          BIGINT       NOT NULL DEFAULT 0,
    closed_at          BIGINT       NOT NULL,
    units_micros       BIGINT       NOT NULL DEFAULT 0,
    invested_cents     BIGINT       NOT NULL DEFAULT 0,
    open_micros        BIGINT       NOT NULL DEFAULT 0,
    close_micros       BIGINT       NOT NULL DEFAULT 0,
    stop_micros        BIGINT       NOT NULL DEFAULT 0,
    take_profit_micros BIGINT       NOT NULL DEFAULT 0,
    net_profit_cents   BIGINT       NOT NULL DEFAULT 0,
    fees_cents         BIGINT       NOT NULL DEFAULT 0,
    fees_known         TINYINT(1)   NOT NULL DEFAULT 0,
    etoro_order_id     BIGINT       NOT NULL DEFAULT 0,
    synced_at          BIGINT       NOT NULL,
    PRIMARY KEY (account_id, position_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX trades_account_closed ON trades(account_id, closed_at);

-- +goose Down
DROP TABLE trades;
