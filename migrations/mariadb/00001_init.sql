-- +goose Up
-- Times are Unix microseconds (UTC) in BIGINT columns: identical in SQLite and MariaDB.
CREATE TABLE users (
    id            VARCHAR(64)  NOT NULL PRIMARY KEY,
    username      VARCHAR(64)  NOT NULL UNIQUE,
    role          VARCHAR(16)  NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    disabled      TINYINT(1)   NOT NULL DEFAULT 0,
    created_at    BIGINT       NOT NULL,
    updated_at    BIGINT       NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE sessions (
    token_hash CHAR(64)     NOT NULL PRIMARY KEY,
    user_id    VARCHAR(64)  NOT NULL,
    csrf_token VARCHAR(128) NOT NULL,
    created_at BIGINT       NOT NULL,
    last_seen  BIGINT       NOT NULL,
    expires_at BIGINT       NOT NULL,
    CONSTRAINT sessions_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX sessions_expires ON sessions(expires_at);

CREATE TABLE api_keys (
    id           VARCHAR(64) NOT NULL PRIMARY KEY,
    owner_id     VARCHAR(64) NOT NULL,
    name         VARCHAR(64) NOT NULL,
    role         VARCHAR(16) NOT NULL,
    secret_hash  CHAR(64)    NOT NULL,
    created_at   BIGINT      NOT NULL,
    last_used_at BIGINT      NULL,
    expires_at   BIGINT      NULL,
    CONSTRAINT api_keys_owner_fk FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Encrypted credentials (AES-256-GCM, the name is the associated data). Never plaintext.
CREATE TABLE secrets (
    name       VARCHAR(64) NOT NULL PRIMARY KEY,
    ciphertext BLOB        NOT NULL,
    updated_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE audit_log (
    id        BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    at        BIGINT       NOT NULL,
    actor_id  VARCHAR(64)  NOT NULL,
    actor     VARCHAR(64)  NOT NULL,
    action    VARCHAR(64)  NOT NULL,
    target    VARCHAR(128) NOT NULL,
    details   TEXT         NOT NULL,
    remote_ip VARCHAR(64)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX audit_log_at ON audit_log(at);

CREATE TABLE settings (
    k          VARCHAR(64) NOT NULL PRIMARY KEY,
    v          TEXT        NOT NULL,
    updated_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE settings;
DROP TABLE audit_log;
DROP TABLE secrets;
DROP TABLE api_keys;
DROP TABLE sessions;
DROP TABLE users;
