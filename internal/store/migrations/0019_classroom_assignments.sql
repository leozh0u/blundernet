-- Homework. A coach sets a target, students work through it in their own time,
-- and both sides see how far along everyone is.
--
-- The assignment stores what to practise rather than which puzzles: a theme, a
-- rating window, and how many to solve. Pinning a fixed list of puzzle ids
-- would look tidier and be worse, because then twenty students all solve the
-- same twenty puzzles and can trade the answers. Two students working on
-- "twenty forks between 1200 and 1500" are doing the same homework without
-- doing the same puzzles.
CREATE TABLE classroom_assignments (
    id           UUID PRIMARY KEY,
    classroom_id UUID NOT NULL REFERENCES classrooms (id) ON DELETE CASCADE,
    created_by   UUID REFERENCES users (id) ON DELETE SET NULL,
    -- Empty for "any theme", matching how the puzzle filter already treats it.
    theme        TEXT NOT NULL DEFAULT '',
    min_rating   INT NOT NULL DEFAULT 0,
    max_rating   INT NOT NULL DEFAULT 0,
    target       INT NOT NULL CHECK (target BETWEEN 1 AND 100),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Progress is counted from the attempts that already exist rather than kept as
-- its own counter. A counter would need every attempt to know which
-- assignments it might advance, and would drift the first time that was
-- missed; counting is a query against rows the site already writes, and it is
-- right by construction.
--
-- Attempts are indexed by user already; this covers the join back to puzzles
-- for the theme and rating test.
CREATE INDEX classroom_assignments_room_idx
    ON classroom_assignments (classroom_id, created_at DESC);
