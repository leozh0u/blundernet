-- Picking a random puzzle out of six million, under filters, without an
-- ORDER BY random() over the whole matching set.
--
-- sample_key is a stable pseudo-random ordering of the table, derived from the
-- id rather than from random() so it survives a reload of the dump: a puzzle
-- keeps its place in the shuffle. Selection is then a range scan from a random
-- cursor, which touches only the rows it returns.
--
-- rating_band buckets the rating into hundreds so it can be an equality rather
-- than a range. That matters: a btree can only use its last column for a
-- range, and that column has to be sample_key for the scan to come out in
-- shuffled order. The exact rating stays as a recheck on top.
ALTER TABLE puzzles
    ADD COLUMN sample_key  INT      GENERATED ALWAYS AS (('x' || substr(md5(id), 1, 8))::bit(32)::int) STORED,
    ADD COLUMN rating_band SMALLINT GENERATED ALWAYS AS (floor(rating / 100)::smallint) STORED;

-- Every column but the last is matched by equality, so the scan walks straight
-- to the cursor and stops at the limit. Measured on the full 6.1M row import:
-- 0.9ms for a plain filter, 4.5ms with a common theme, 7.8ms with a rare one
-- (where the planner switches to a bitmap AND against the themes index).
CREATE INDEX puzzles_sample_idx ON puzzles (rating_band, phase, solution_plies, sample_key);

-- Superseded by the index above, and 237MB is too much to keep for nothing.
DROP INDEX puzzles_rating_phase_plies_idx;

-- How many puzzles sit in each cell of the filter grid. Splitting selection
-- across cells is what makes the equality prefix possible, and sampling cells
-- uniformly would then over-represent the sparse ones: there are 3.4M puzzles
-- with a three ply solution and one with thirty three. Cells are drawn in
-- proportion to this count instead, which puts the bias back at zero.
--
-- Themes are part of the key because they are not part of the index prefix. A
-- theme filter is a recheck, so a sampler drawing cells that hold no puzzle
-- with that theme spends every draw on nothing: smotheredMate is one puzzle in
-- 250, and drawing a dozen cells blind finds none of them. Counting per theme
-- means only cells that can answer are ever drawn.
--
-- The row with theme '' counts the whole cell, so the no-theme case reads the
-- same table the same way rather than through a second path.
--
-- 22k rows and under 2MB for the full import. Refreshed by the loader; it is
-- a summary of an import, not live state.
CREATE TABLE puzzle_cells (
    theme          TEXT NOT NULL,
    rating_band    SMALLINT NOT NULL,
    phase          TEXT NOT NULL,
    solution_plies INT NOT NULL,
    n              BIGINT NOT NULL,
    PRIMARY KEY (theme, rating_band, phase, solution_plies)
);
