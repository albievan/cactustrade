-- +goose Up
-- R45: a Trading 212 position's "sold so far" (00017) is summed over the orders of the
-- position: indexed by account and position. Split from 00017 (one DDL statement per
-- migration, re-review 2026-11-28, minor 4).
CREATE INDEX orders_position ON orders(account_id, position_id);

-- +goose Down
DROP INDEX orders_position;
