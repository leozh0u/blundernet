-- The reaper looks for anonymous reviews old enough to drop, and the index
-- added with the table covers the opposite case: it is partial on
-- user_id IS NOT NULL, for "what have I had reviewed". A reap would therefore
-- scan the whole table every hour to find nothing.
--
-- This is the index the reaper actually needs. Partial, so it only carries the
-- rows that can ever be deleted, which is also the majority: most pastes come
-- from people without an account, which is the point of the feature.
CREATE INDEX imports_anonymous_created_idx ON imports (created_at)
    WHERE user_id IS NULL;
