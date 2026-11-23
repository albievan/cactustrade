-- +goose Up
-- Per-user eToro accounts that can be shared (owner's request, 2026-11-23). A user who owns
-- an account cannot be deleted (no cascade): delete or hand over the account first.
CREATE TABLE accounts (
    id         VARCHAR(64) NOT NULL PRIMARY KEY,
    name       VARCHAR(255) NOT NULL,
    owner_id   VARCHAR(64) NOT NULL REFERENCES users(id),
    mode       VARCHAR(8)  NOT NULL,
    env_keys   TINYINT(1)  NOT NULL DEFAULT 0,
    created_at BIGINT      NOT NULL,
    updated_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX accounts_owner ON accounts(owner_id);

CREATE TABLE account_shares (
    account_id VARCHAR(64) NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id    VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access     VARCHAR(8)  NOT NULL,
    created_at BIGINT      NOT NULL,
    PRIMARY KEY (account_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX account_shares_user ON account_shares(user_id);

-- The watchlist becomes per account (the phase-2 global list is dropped; it held only
-- instruments added before accounts existed).
DROP TABLE watchlist;
CREATE TABLE watchlist (
    account_id    VARCHAR(64) NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    instrument_id BIGINT      NOT NULL REFERENCES instruments(id) ON DELETE CASCADE,
    added_by      VARCHAR(64) NOT NULL,
    added_at      BIGINT      NOT NULL,
    PRIMARY KEY (account_id, instrument_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Events belong to an account ('' = the whole system).
ALTER TABLE events ADD COLUMN account_id VARCHAR(64) NOT NULL DEFAULT '';
CREATE INDEX events_account ON events(account_id, at);

-- +goose Down
DROP INDEX events_account ON events;
ALTER TABLE events DROP COLUMN account_id;
DROP TABLE watchlist;
CREATE TABLE watchlist (
    instrument_id BIGINT      NOT NULL PRIMARY KEY REFERENCES instruments(id) ON DELETE CASCADE,
    added_by      VARCHAR(64) NOT NULL,
    added_at      BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
DROP TABLE account_shares;
DROP TABLE accounts;
