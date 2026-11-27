-- +goose Up
-- R39: the long or short of the order or position an event is about, so the dashboard's
-- activity log can show long and/or short entries (owner, 2026-11-27). '' = none.
ALTER TABLE events ADD COLUMN direction VARCHAR(8) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE events DROP COLUMN direction;
