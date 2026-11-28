-- +goose Up
-- R45 (D44: only what cactusTrade bought is ever sold): the share quantity Trading 212 filled
-- of an order of ours, stored with the order's state in the same write. A Trading 212
-- position's "sold so far" is the sum over its own stops and sales, so a sale after a partly
-- filled one never reaches the owner's own shares in the same ticker. 0 for eToro.
-- One DDL statement per migration (MariaDB commits each implicitly); the index the sum reads
-- by is 00018 (re-review 2026-11-28, minor 4).
ALTER TABLE orders ADD COLUMN filled_micros BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE orders DROP COLUMN filled_micros;
