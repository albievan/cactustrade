-- +goose Up
-- The event feed behind the bell: watchlist changes, scanner suggestions, orders, trades,
-- kill switch, limits, restarts. Unread = newer than the user's "events.seen" mark.
CREATE TABLE events (
    id       VARCHAR(64)  NOT NULL PRIMARY KEY,
    at       BIGINT       NOT NULL,
    kind     VARCHAR(64)  NOT NULL,
    severity VARCHAR(16)  NOT NULL,
    title    VARCHAR(255) NOT NULL,
    detail   VARCHAR(2000) NOT NULL DEFAULT '',
    actor    VARCHAR(64)  NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX events_at ON events(at);

-- +goose Down
DROP TABLE events;
