-- What the Lichess dump knows that the first cut of the schema dropped.
--
-- popularity is their upvote score in [-100, 100] and is the only quality
-- signal in the file: a puzzle people dislike is usually one with a second
-- decent move, which is exactly what a drill must not serve.
--
-- nb_plays is how many times it was solved on Lichess, kept apart from the
-- plays column because that one counts attempts here. Mixing an imported
-- number into our own stats would make every later measurement a lie.
ALTER TABLE puzzles
    ADD COLUMN popularity   INT NOT NULL DEFAULT 0,
    ADD COLUMN nb_plays     INT NOT NULL DEFAULT 0,
    ADD COLUMN game_url     TEXT NOT NULL DEFAULT '',
    ADD COLUMN opening_tags TEXT[] NOT NULL DEFAULT '{}';

-- Openings are an array and the query is containment, same as themes.
CREATE INDEX puzzles_opening_tags_idx ON puzzles USING GIN (opening_tags)
    WHERE opening_tags <> '{}';
