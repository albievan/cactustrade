-- +goose Up
-- R45 (D41-D44): Trading 212 practice accounts trade. An order names its platform, and a
-- Trading 212 order its ticker and share quantity (millionths); etoro_order_id holds the
-- broker's order id for both platforms (kept, not renamed). Kind 'stop' fits VARCHAR(8).
-- A Trading 212 position of ours is keyed by its buy order's id and carries its ticker and
-- the quantity cactusTrade bought. Trading 212's instrument catalogue (one for every
-- account, replaced atomically; short_name upper case) and, per account, the match of an
-- eToro watchlist share to a Trading 212 ticker, confirmed by the account's owner (D42).
ALTER TABLE orders ADD COLUMN platform VARCHAR(16) NOT NULL DEFAULT 'etoro';
ALTER TABLE orders ADD COLUMN ticker VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN quantity_micros BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_positions ADD COLUMN ticker VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE agent_positions ADD COLUMN quantity_micros BIGINT NOT NULL DEFAULT 0;

CREATE TABLE t212_instruments (
    ticker                   VARCHAR(64)  NOT NULL PRIMARY KEY,
    isin                     VARCHAR(16)  NOT NULL DEFAULT '',
    name                     VARCHAR(255) NOT NULL DEFAULT '',
    short_name               VARCHAR(64)  NOT NULL DEFAULT '',
    currency                 VARCHAR(8)   NOT NULL DEFAULT '',
    type                     VARCHAR(32)  NOT NULL DEFAULT '',
    max_open_quantity_micros BIGINT       NOT NULL DEFAULT 0,
    fetched_at               BIGINT       NOT NULL
);
CREATE INDEX t212_instruments_isin ON t212_instruments(isin);
CREATE INDEX t212_instruments_short_name ON t212_instruments(short_name);

CREATE TABLE instrument_matches (
    account_id    VARCHAR(64) NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    instrument_id BIGINT      NOT NULL,
    ticker        VARCHAR(64) NOT NULL,
    isin          VARCHAR(16) NOT NULL DEFAULT '',
    method        VARCHAR(16) NOT NULL DEFAULT '',
    confirmed_by  VARCHAR(64) NOT NULL DEFAULT '',
    confirmed_at  BIGINT      NOT NULL DEFAULT 0,
    updated_at    BIGINT      NOT NULL,
    PRIMARY KEY (account_id, instrument_id)
);

-- +goose Down
DROP TABLE instrument_matches;
DROP TABLE t212_instruments;
ALTER TABLE agent_positions DROP COLUMN quantity_micros;
ALTER TABLE agent_positions DROP COLUMN ticker;
ALTER TABLE orders DROP COLUMN quantity_micros;
ALTER TABLE orders DROP COLUMN ticker;
ALTER TABLE orders DROP COLUMN platform;
