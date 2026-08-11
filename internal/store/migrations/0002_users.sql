CREATE TABLE users (
    id            UUID PRIMARY KEY,
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Usernames are compared case insensitively, so "Leo" and "leo" cannot both
-- exist. A functional unique index does that without the citext extension,
-- which RDS would otherwise have to have enabled.
CREATE UNIQUE INDEX users_username_lower_idx ON users (lower(username));

-- Anonymous games stay allowed, so the column is nullable. Deleting an
-- account keeps its games and detaches them rather than destroying history.
ALTER TABLE games ADD COLUMN user_id UUID REFERENCES users (id) ON DELETE SET NULL;

-- The one query the profile page makes: this user's games, newest first.
CREATE INDEX games_user_finished_idx ON games (user_id, finished_at DESC)
    WHERE user_id IS NOT NULL;
