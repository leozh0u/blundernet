-- Puzzles imported from the Lichess CC0 database, plus any generated here.
-- Columns that are filters are stored rather than computed, because computing
-- solution length or phase per query means no index can help.
CREATE TABLE puzzles (
    id             TEXT PRIMARY KEY,
    fen            TEXT NOT NULL,
    -- UCI moves. The first is the opponent's blunder, played automatically;
    -- the solution is everything after it.
    moves          TEXT NOT NULL,
    rating         DOUBLE PRECISION NOT NULL,
    rating_deviation DOUBLE PRECISION NOT NULL DEFAULT 80,
    rating_volatility DOUBLE PRECISION NOT NULL DEFAULT 0.06,
    plays          INT NOT NULL DEFAULT 0,
    solved         INT NOT NULL DEFAULT 0,

    -- Derived at load time. These are the filters.
    solution_plies INT NOT NULL,
    phase          TEXT NOT NULL CHECK (phase IN ('opening','middlegame','endgame')),
    themes         TEXT[] NOT NULL DEFAULT '{}',

    source         TEXT NOT NULL DEFAULT 'lichess',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Themes are an array and the query is containment, which is what GIN is for.
CREATE INDEX puzzles_themes_idx ON puzzles USING GIN (themes);

-- The common query filters on rating band first, then narrows. Rating leads
-- because it is the one filter always present: the default is a window around
-- the player's own rating.
CREATE INDEX puzzles_rating_phase_plies_idx ON puzzles (rating, phase, solution_plies);

-- Attempts, one row per try. Kept rather than a counter because "show me the
-- ones I got wrong" is the feature that makes this worth using twice.
CREATE TABLE puzzle_attempts (
    user_id     UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    puzzle_id   TEXT NOT NULL REFERENCES puzzles (id) ON DELETE CASCADE,
    solved      BOOLEAN NOT NULL,
    ms          INT,
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, puzzle_id, attempted_at)
);

-- "Which have I already seen" and "which did I fail", both keyed by user.
CREATE INDEX puzzle_attempts_user_idx ON puzzle_attempts (user_id, attempted_at DESC);
CREATE INDEX puzzle_attempts_failed_idx ON puzzle_attempts (user_id, puzzle_id)
    WHERE NOT solved;

-- A separate puzzle rating per user. Tactical strength and playing strength
-- are different things, and Lichess and chess.com both keep them apart.
ALTER TABLE users
    ADD COLUMN puzzle_rating            DOUBLE PRECISION NOT NULL DEFAULT 1500,
    ADD COLUMN puzzle_rating_deviation  DOUBLE PRECISION NOT NULL DEFAULT 350,
    ADD COLUMN puzzle_rating_volatility DOUBLE PRECISION NOT NULL DEFAULT 0.06,
    ADD COLUMN puzzles_solved           INT NOT NULL DEFAULT 0;
