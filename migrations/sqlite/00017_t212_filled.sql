-- +goose Up
-- R45 (D44: only what cactusTrade bought is ever sold): the share quantity Trading 212 filled
-- of an order of ours, stored with the order's state in the same write. A Trading 212
-- position's "sold so far" is the sum over its own stops and sales, so a sale after a partly
-- filled one never reaches the owner's own shares in the same ticker. 0 for eToro.
ALTER TABLE orders ADD COLUMN filled_micros BIGINT NOT NULL DEFAULT 0;
CREATE INDEX orders_position ON orders(account_id, position_id);

-- +goose Down
DROP INDEX orders_position;
ALTER TABLE orders DROP COLUMN filled_micros;
