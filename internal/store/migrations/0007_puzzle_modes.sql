-- Two modes, and they must not contaminate each other. Learning is a drill:
-- filters, hints, explanations, no rating, works signed out. Ranked is a test:
-- one puzzle at your level, no hints, rating moves, account required.
--
-- The mode lives on the attempt rather than being inferred later, because
-- "did this count" has to be answerable from the row itself.
ALTER TABLE puzzle_attempts
    ADD COLUMN mode        TEXT NOT NULL DEFAULT 'learning'
        CHECK (mode IN ('learning', 'ranked')),
    -- Using a hint still solves the puzzle, but it is not the same as solving
    -- it cold, and the "ones I got wrong" list should know the difference.
    ADD COLUMN hints_used  INT NOT NULL DEFAULT 0;

-- Ranked history is the only kind that feeds a rating, so it gets its own
-- index rather than filtering a mixed scan.
CREATE INDEX puzzle_attempts_ranked_idx ON puzzle_attempts (user_id, attempted_at DESC)
    WHERE mode = 'ranked';

-- Only ranked attempts move a puzzle's own rating, so it needs its own count
-- separate from total plays.
ALTER TABLE puzzles
    ADD COLUMN ranked_plays INT NOT NULL DEFAULT 0;
