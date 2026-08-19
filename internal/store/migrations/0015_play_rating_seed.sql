-- New players started at 1500, which is above the entire bot ladder.
--
-- 1500 is the Glicko-2 convention, and it is the right number when the pool it
-- describes averages 1500. This one does not. The ladder is anchored at level
-- 5 = 1000 (the one rung actually measured) with 120 points a rung, so the six
-- bots sit at 520, 640, 760, 880, 1000 and 1120. Seeding a player at 1500 put
-- every new account 380 points above the strongest opponent available, which
-- meant a new rating could only travel downwards. The first recorded test of
-- it did exactly that: 1500 to 916 on a single loss to level 3, because losing
-- to a 760 from 1500 is an enormous surprise to Glicko-2 and it corrects hard.
--
-- 1000 instead, which is the rung that was measured rather than assumed. A new
-- player now sits in the middle of the ladder and their rating can move both
-- ways from the first game.
--
-- Note this is NOT rating.DefaultRating, which stays 1500. That constant is
-- the centre of the Glicko-2 display scale and appears in the mu transform;
-- changing it would rescale the whole algorithm rather than move the start.
ALTER TABLE users ALTER COLUMN rating SET DEFAULT 1000;

-- Existing accounts that never finished a rated game are seeds too, so they
-- move with the default. Anything with a played game keeps what it earned:
-- a rating that came from results is data, and rewriting it would be a lie
-- about games that happened.
UPDATE users
SET rating = 1000
WHERE rated_games = 0
  AND rating = 1500;

-- puzzle_rating deliberately stays at 1500. That scale is not this one: puzzle
-- ratings come from the Lichess corpus, where 1500 is a genuine middle, and
-- the opponent in a puzzle attempt is a puzzle carrying its own Lichess
-- rating rather than a bot from the ladder above.
