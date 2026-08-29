-- A game somebody pasted in to have reviewed.
--
-- Its own table rather than a row in games, because a game played here has an
-- opponent, a rating, a result and a bot level, and a pasted one has none of
-- those. Forcing it into games would mean half the columns nullable and every
-- query having to say which kind it meant.
--
-- The review is filled in by the worker, so a row exists with a null review
-- while the queue gets to it. That is the same shape as a game review and the
-- client polls it the same way.
CREATE TABLE imports (
    id         UUID PRIMARY KEY,
    -- Null for someone not signed in. Reviewing a pasted game does not need an
    -- account: it is the most useful thing this site does for a stranger, and
    -- demanding a signup first is how you lose them.
    user_id    UUID REFERENCES users (id) ON DELETE SET NULL,
    moves      TEXT NOT NULL,
    review     JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "What have I had reviewed" on the account page, and nothing else reads this.
CREATE INDEX imports_user_idx ON imports (user_id, created_at DESC)
    WHERE user_id IS NOT NULL;
