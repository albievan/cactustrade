-- +goose Up
-- R30 (owner, 2026-11-24): trading statements e-mailed as PDFs.

-- Files attached to an outbox mail (statement PDFs). They go with their mail: the outbox's
-- 30-day purge removes them too. data holds the file's bytes (at most 10 MiB per mail).
CREATE TABLE outbox_attachments (
    mail_id      VARCHAR(64)  NOT NULL REFERENCES outbox(id) ON DELETE CASCADE,
    seq          INTEGER      NOT NULL,
    filename     VARCHAR(100) NOT NULL,
    content_type VARCHAR(100) NOT NULL,
    data         BLOB         NOT NULL,
    PRIMARY KEY (mail_id, seq)
);

-- The statement schedules of each user. account_ids and recipients are JSON lists (no
-- account ids = every account the owner may put on a schedule). last_period is the key of
-- the newest period handled ('' = none), so a period is never sent twice, also across
-- restarts and replicas (compare-and-set). Times in Unix microseconds (0 = not set).
CREATE TABLE statement_schedules (
    id                VARCHAR(64)  NOT NULL PRIMARY KEY,
    owner_id          VARCHAR(64)  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    period            VARCHAR(8)   NOT NULL,
    account_ids       TEXT         NOT NULL,
    recipients        TEXT         NOT NULL,
    include_combined  INTEGER      NOT NULL DEFAULT 0,
    include_accounts  INTEGER      NOT NULL DEFAULT 0,
    include_exchanges INTEGER      NOT NULL DEFAULT 0,
    enabled           INTEGER      NOT NULL DEFAULT 1,
    last_period       VARCHAR(16)  NOT NULL DEFAULT '',
    last_sent_at      BIGINT       NOT NULL DEFAULT 0,
    last_error        VARCHAR(500) NOT NULL DEFAULT '',
    manual_sent_at    BIGINT       NOT NULL DEFAULT 0,
    created_at        BIGINT       NOT NULL,
    updated_at        BIGINT       NOT NULL
);
CREATE INDEX statement_schedules_owner ON statement_schedules(owner_id, created_at);

-- +goose Down
DROP TABLE statement_schedules;
DROP TABLE outbox_attachments;
