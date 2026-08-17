-- The post-game review, stored on the game rather than recomputed.
--
-- It is one network evaluation per position, which is cheap but not free, and
-- a review does not change once the game is over. Storing it also means the
-- browser can ask for it again after a reload instead of paying for it twice.
ALTER TABLE games ADD COLUMN review JSONB;
