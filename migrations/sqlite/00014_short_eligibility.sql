-- +goose Up
-- R33: the short side of an instrument's eligibility (cfd / short / leverage 1), which was
-- read from eToro but never stored, so every cached instrument read back as "not confirmed
-- shortable" and the agent could never short (owner, 2026-11-25). Every cached check is
-- marked unchecked so the next scan asks eToro again and stores both sides.
ALTER TABLE instruments ADD COLUMN eligible_short_cfd INTEGER NOT NULL DEFAULT 0;
ALTER TABLE instruments ADD COLUMN short_eligibility_reason VARCHAR(200) NOT NULL DEFAULT '';
ALTER TABLE instruments ADD COLUMN min_short_cents BIGINT NOT NULL DEFAULT 0;
UPDATE instruments SET eligibility_checked_at = 0;

-- +goose Down
ALTER TABLE instruments DROP COLUMN min_short_cents;
ALTER TABLE instruments DROP COLUMN short_eligibility_reason;
ALTER TABLE instruments DROP COLUMN eligible_short_cfd;
