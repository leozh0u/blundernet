-- The bot ladder. Every account, guest included, sits on a level, and a rated
-- game moves it: win and the next bot is stronger, lose and it is weaker.
--
-- This is separate from the Glicko rating on purpose. The rating answers "how
-- strong is this player" and moves slowly by design; the ladder answers "what
-- should the next opponent be" and has to move after every game, or the answer
-- is stale exactly when somebody is improving fastest.
ALTER TABLE users
    ADD COLUMN bot_level INT NOT NULL DEFAULT 3 CHECK (bot_level BETWEEN 1 AND 6);

-- The level the game was played at, so a rating update can tell which opponent
-- it was against, and so history can show it.
ALTER TABLE games
    ADD COLUMN bot_level INT NOT NULL DEFAULT 4,
    -- Learning games are not rated. Stored rather than inferred, same
    -- reasoning as the puzzle attempt mode: "did this count" has to be
    -- answerable from the row.
    ADD COLUMN rated BOOLEAN NOT NULL DEFAULT TRUE;
