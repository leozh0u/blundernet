-- Puzzles somebody wants to keep: a position worth coming back to, or one
-- they want to show someone. Separate from the wrong-answer list, which is
-- derived from attempts and empties itself as you improve. A favourite is a
-- deliberate act and only leaves when it is taken back.
CREATE TABLE puzzle_favourites (
    user_id    UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    puzzle_id  TEXT NOT NULL REFERENCES puzzles (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, puzzle_id)
);

-- The list is read newest first, and the primary key leads with user_id so it
-- already covers "is this one saved".
CREATE INDEX puzzle_favourites_user_idx ON puzzle_favourites (user_id, created_at DESC);
