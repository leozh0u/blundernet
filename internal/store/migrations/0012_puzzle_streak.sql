-- Streak: puzzles get harder until you miss one, then it is over. The mode
-- goes on the attempt like the other two, so "did this count towards a
-- rating" stays answerable from the row. It does not: a streak is a game
-- rather than a measurement, which is also why the only thing kept from it is
-- the best run.
ALTER TABLE puzzle_attempts
    DROP CONSTRAINT puzzle_attempts_mode_check,
    ADD CONSTRAINT puzzle_attempts_mode_check
        CHECK (mode IN ('learning', 'ranked', 'streak'));

ALTER TABLE users
    ADD COLUMN best_streak INT NOT NULL DEFAULT 0;
