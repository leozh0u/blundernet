-- Somewhere for people to say what is broken.
--
-- Stored rather than emailed. Email would mean SES, a verified sender, a
-- sandbox exit, and a delivery path that can fail silently; a table cannot
-- bounce, cannot land in spam, and is still there in a month. The cost is that
-- somebody has to look, which is a habit rather than a system.
--
-- Deliberately minimal on what it keeps. No user agent, no address, no
-- referrer: the privacy page says the site stores an account and its games,
-- and quietly starting to collect browser fingerprints would make that untrue.
-- The page path is kept because "the board did not load" is a different report
-- on /puzzles than on /play.
CREATE TABLE feedback (
    id         BIGSERIAL PRIMARY KEY,
    -- NULL for someone who never signed in, which is most people who hit a bug
    -- and leave. ON DELETE SET NULL so deleting an account keeps the report but
    -- forgets who sent it.
    user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    message    TEXT NOT NULL,
    page       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when the report has been dealt with, so a second read does not go
    -- back over everything already fixed.
    handled_at TIMESTAMPTZ
);

-- Reading is always "what is new, newest first", so that is the index.
CREATE INDEX feedback_unhandled_idx ON feedback (created_at DESC) WHERE handled_at IS NULL;
