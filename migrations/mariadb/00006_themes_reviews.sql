-- +goose Up
-- Themes (owner's request R11): an account's own "industries" such as Space or
-- Semiconductors: search words, an optional eToro stock industry and picked shares.
CREATE TABLE themes (
    id          VARCHAR(64)  NOT NULL PRIMARY KEY,
    account_id  VARCHAR(64)  NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    keywords    VARCHAR(1000) NOT NULL DEFAULT '',
    industry_id BIGINT       NOT NULL DEFAULT 0,
    enabled     TINYINT(1)   NOT NULL DEFAULT 1,
    created_at  BIGINT       NOT NULL,
    updated_at  BIGINT       NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX themes_account ON themes(account_id);

CREATE TABLE theme_instruments (
    theme_id      VARCHAR(64) NOT NULL REFERENCES themes(id) ON DELETE CASCADE,
    instrument_id BIGINT      NOT NULL REFERENCES instruments(id) ON DELETE CASCADE,
    PRIMARY KEY (theme_id, instrument_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The scanner's latest review of each stock per account (R8/R9): the rules it applied and
-- what came of it.
CREATE TABLE reviews (
    account_id    VARCHAR(64)   NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    instrument_id BIGINT        NOT NULL,
    at            BIGINT        NOT NULL,
    symbol        VARCHAR(64)   NOT NULL,
    theme         VARCHAR(255)  NOT NULL DEFAULT '',
    outcome       VARCHAR(16)   NOT NULL,
    direction     VARCHAR(8)    NOT NULL DEFAULT '',
    probs         VARCHAR(255)  NOT NULL DEFAULT '{}',
    checks        TEXT          NOT NULL,
    summary       VARCHAR(1000) NOT NULL DEFAULT '',
    PRIMARY KEY (account_id, instrument_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE INDEX reviews_at ON reviews(account_id, at);

-- +goose Down
DROP TABLE reviews;
DROP TABLE theme_instruments;
DROP TABLE themes;
