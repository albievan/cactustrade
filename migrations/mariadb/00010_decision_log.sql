-- +goose Up
-- Phase 5: every call to an LLM reviewer (prompt = the request body as sent, response = the
-- raw body, each cut at 32 KiB) and the decision log (one row per candidate the agent
-- handled). Both are kept 400 days (OD12). Money in cents, prices in micros, cost in USD
-- micros (-1 = unknown), times in Unix microseconds.
-- llm_calls.account_id '' = no account (the admin's test); no foreign key, so the usage
-- figures survive a deleted account.
CREATE TABLE llm_calls (
    id            VARCHAR(64)   NOT NULL PRIMARY KEY,
    account_id    VARCHAR(64)   NOT NULL DEFAULT '',
    purpose       VARCHAR(16)   NOT NULL,
    profile_id    VARCHAR(64)   NOT NULL DEFAULT '',
    provider      VARCHAR(32)   NOT NULL DEFAULT '',
    model         VARCHAR(200)  NOT NULL DEFAULT '',
    prompt        MEDIUMTEXT    NOT NULL,
    response      MEDIUMTEXT    NOT NULL,
    input_tokens  BIGINT        NOT NULL DEFAULT 0,
    output_tokens BIGINT        NOT NULL DEFAULT 0,
    cost_micros   BIGINT        NOT NULL DEFAULT -1,
    latency_ms    BIGINT        NOT NULL DEFAULT 0,
    error         VARCHAR(500)  NOT NULL DEFAULT '',
    at            BIGINT        NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX llm_calls_at ON llm_calls(at);
CREATE INDEX llm_calls_account_at ON llm_calls(account_id, at);

CREATE TABLE decisions (
    id                 VARCHAR(64)  NOT NULL PRIMARY KEY,
    account_id         VARCHAR(64)  NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    at                 BIGINT       NOT NULL,
    instrument_id      BIGINT       NOT NULL,
    symbol             VARCHAR(32)  NOT NULL DEFAULT '',
    direction          VARCHAR(8)   NOT NULL,
    playbook           VARCHAR(64)  NOT NULL DEFAULT '',
    signal_reason      VARCHAR(500) NOT NULL DEFAULT '',
    entry_micros       BIGINT       NOT NULL DEFAULT 0,
    stop_micros        BIGINT       NOT NULL DEFAULT 0,
    take_profit_micros BIGINT       NOT NULL DEFAULT 0,
    suggested_cents    BIGINT       NOT NULL DEFAULT 0,
    probs              TEXT         NOT NULL,
    llm_call_id        VARCHAR(64)  NOT NULL DEFAULT '',
    verdict            VARCHAR(16)  NOT NULL DEFAULT '',
    size_bp            BIGINT       NOT NULL DEFAULT 0,
    verdict_reason     VARCHAR(500) NOT NULL DEFAULT '',
    risk_allowed       TINYINT(1)   NOT NULL DEFAULT 0,
    risk_size_cents    BIGINT       NOT NULL DEFAULT 0,
    risk_reason        VARCHAR(500) NOT NULL DEFAULT '',
    outcome            VARCHAR(24)  NOT NULL,
    order_request_id   VARCHAR(64)  NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX decisions_account_at ON decisions(account_id, at);
CREATE INDEX decisions_account_outcome_at ON decisions(account_id, outcome, at);
CREATE INDEX decisions_at ON decisions(at);

-- +goose Down
DROP TABLE decisions;
DROP TABLE llm_calls;
