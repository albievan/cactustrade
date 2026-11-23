-- +goose Up
-- Owner's requests 2026-11-23: e-mail (as cactusInv: an outgoing mail server, an outbox
-- with retries, notifications and password reset by e-mail) and an LLM profile per theme.

-- A user's e-mail address: notifications and password-reset links go there.
ALTER TABLE users ADD COLUMN email VARCHAR(254) NOT NULL DEFAULT '';

-- The reviewer profile a theme's candidates are reviewed with ('' = the default profile).
ALTER TABLE themes ADD COLUMN llm_profile_id VARCHAR(64) NOT NULL DEFAULT '';

-- Mail waiting to be sent, and mail sent (kept 30 days). A message the server accepted
-- (sent_at > 0) is never sent again.
CREATE TABLE outbox (
    id         VARCHAR(64)   NOT NULL PRIMARY KEY,
    to_addr    VARCHAR(254)  NOT NULL,
    subject    VARCHAR(255)  NOT NULL,
    body       TEXT          NOT NULL,
    created_at BIGINT        NOT NULL,
    next_at    BIGINT        NOT NULL,
    attempts   INTEGER       NOT NULL DEFAULT 0,
    sent_at    BIGINT        NOT NULL DEFAULT 0,
    last_error VARCHAR(500)  NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX outbox_due ON outbox(sent_at, next_at);

-- Password-reset links: only the SHA-256 of the token is stored; single use, short-lived.
CREATE TABLE password_resets (
    token_hash CHAR(64)    NOT NULL PRIMARY KEY,
    user_id    VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at BIGINT      NOT NULL,
    expires_at BIGINT      NOT NULL,
    used_at    BIGINT      NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX password_resets_user ON password_resets(user_id, created_at);

-- +goose Down
DROP TABLE password_resets;
DROP TABLE outbox;
ALTER TABLE themes DROP COLUMN llm_profile_id;
ALTER TABLE users DROP COLUMN email;
