-- Glicko-2 state. Stored on the display scale (1500 centred) and converted
-- for the maths, which is what the paper does.
ALTER TABLE users
    ADD COLUMN rating            DOUBLE PRECISION NOT NULL DEFAULT 1500,
    ADD COLUMN rating_deviation  DOUBLE PRECISION NOT NULL DEFAULT 350,
    ADD COLUMN rating_volatility DOUBLE PRECISION NOT NULL DEFAULT 0.06,
    ADD COLUMN rated_games       INT NOT NULL DEFAULT 0;

-- The leaderboard reads provisional players out, so the partial index matches
-- the query rather than covering every row.
CREATE INDEX users_rating_idx ON users (rating DESC) WHERE rated_games >= 5;
