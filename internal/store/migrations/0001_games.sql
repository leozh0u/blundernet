-- Baseline. IF NOT EXISTS because this table predates the migration runner
-- and already exists in any database created by the old boot-time DDL.
CREATE TABLE IF NOT EXISTS games (
    id           UUID PRIMARY KEY,
    player_color TEXT NOT NULL CHECK (player_color IN ('white','black')),
    result       TEXT NOT NULL,
    termination  TEXT NOT NULL,
    moves        TEXT NOT NULL,
    ply          INT  NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
