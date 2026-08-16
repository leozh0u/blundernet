-- A guest is a real user row with no credentials. Everything downstream then
-- works unchanged: games reference it, ratings apply, history queries hit the
-- same index. Signing up later fills in the username and password on this same
-- row, so progress carries over without migrating anything.
ALTER TABLE users
    ALTER COLUMN username DROP NOT NULL,
    ALTER COLUMN password_hash DROP NOT NULL,
    ADD COLUMN is_guest     BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- The unique index on lower(username) already tolerates this: Postgres allows
-- any number of NULLs in a unique index, so guests do not collide.

-- Guests accumulate forever otherwise. This index is what the reaper walks.
CREATE INDEX users_guest_last_seen_idx ON users (last_seen_at) WHERE is_guest;
