-- A way back into an account that does not need an email address.
--
-- The site never collects email. Adding it just to support password reset
-- would mean collecting personal data, verifying it (an unverified address is
-- worse than none, since a typo locks the account anyway and an unverified one
-- lets somebody claim a stranger's address), and running a sender with the
-- deliverability problems that brings. For a chess site holding a rating and
-- some game history, that is a lot of machinery and a lot of stored personal
-- data to protect something that is not worth stealing.
--
-- Instead every account gets one recovery code at signup, shown exactly once.
-- This is the same shape as a GitHub or Google backup code. The tradeoff is
-- explicit and it points the other way from email: nothing to leak and nothing
-- to deliver, but a user who loses the code has no way back.
--
-- Stored as an Argon2id hash, in the same format and with the same parameters
-- as password_hash, because a recovery code is a password by another name: it
-- is a bearer secret that takes over an account. A database leak must not hand
-- out working codes.
ALTER TABLE users ADD COLUMN recovery_hash TEXT;

-- Nullable on purpose. Guests have no credentials to recover, and the accounts
-- that already exist predate this column. Both keep working; they simply have
-- no recovery path until they set one.
COMMENT ON COLUMN users.recovery_hash IS
    'Argon2id hash of the one-time recovery code. NULL for guests and for accounts created before recovery codes existed.';

-- Rotated on use, so a code that has been spent cannot be replayed by anyone
-- who saw it over a shoulder or in a screenshot.
ALTER TABLE users ADD COLUMN recovery_used_at TIMESTAMPTZ;
