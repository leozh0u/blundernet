-- last_seen_at was added for a reaper that did not exist, and nothing ever
-- wrote to it, so every guest kept its insert-time default and the partial
-- index cost writes for no reader.
--
-- Reaping on created_at works instead, because a guest that never finished a
-- game has nothing worth keeping regardless of when it was last seen, and
-- created_at is already maintained.
DROP INDEX IF EXISTS users_guest_last_seen_idx;
ALTER TABLE users DROP COLUMN IF EXISTS last_seen_at;

CREATE INDEX users_guest_created_idx ON users (created_at) WHERE is_guest;

-- Authenticate scans username and password_hash into non-nullable Go strings
-- and relies on "AND NOT is_guest" to guarantee they are set. Make that the
-- database's job: without it, one bad UPDATE turns every login for that
-- username into a 500.
ALTER TABLE users ADD CONSTRAINT users_credentials_unless_guest
    CHECK (is_guest OR (username IS NOT NULL AND password_hash IS NOT NULL));
