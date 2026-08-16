# Puzzles: the plan

## The thing to understand first

Lichess generated their puzzle set by re-analysing 600 million games with
Stockfish NNUE at 40 meganodes. It cost them **over 100 years of CPU time**.
That is not a number I can beat with a laptop and a $6 instance, and any plan
that starts with "scan millions of games myself" is a plan that never ships.

They also publish the finished set under **CC0**: 6,057,356 puzzles, already
rated, already theme-tagged, explicitly free for commercial use, modification
and redistribution without asking. https://database.lichess.org/

So the corpus is solved and free. Generating it is not the interesting problem
and not a differentiator, because the best possible outcome is catching up to
something already given away.

## What is actually missing

Both chess.com and Lichess serve puzzles the same way: here is one at roughly
your rating, next. Chess.com puts most of the filtering behind a paywall.
Neither lets you say **"give me twenty 3-move knight forks in an endgame at
1600"** and drill exactly that.

That is the product. Not a puzzle generator, a **puzzle search engine**. The
LeetCode comparison is right: LeetCode did not invent the problems, it made
them filterable, rated, and trackable.

## Architecture

Two halves, same split as the engine: batch work offline, serving online.

### Ingest (offline, run once, then monthly)

Pull the Lichess CC0 puzzle CSV, parse, and bulk load into Postgres. Their
format gives PuzzleId, FEN, Moves, Rating, RatingDeviation, Popularity,
NbPlays, Themes, GameUrl, OpeningTags.

Derived at load time, because these are the filters and they must be indexed
rather than computed per query:

- **`solution_plies`** from the move list. This is "how many moves to finish",
  the length filter.
- **`phase`** from the FEN: piece count and material. Endgame when few pieces
  remain, opening by move number, middlegame otherwise. Crude, and correct
  often enough to be useful.
- **`piece_themes`** normalised out of their theme strings, so `knightFork`
  and `fork` both resolve sensibly.

The whole load is one `COPY` into a staging table then an `INSERT ... SELECT`
with the derived columns, which is minutes, not hours.

### My own generator (later, supplementary)

Worth building, but as an addition rather than the source. Run Stockfish over
games my own users play, find positions where somebody threw away a winning
evaluation, verify the refutation is **forced** (only one move keeps the win,
which is what separates a puzzle from a good move), and tag it.

This earns its place three ways: puzzles nobody else has, a real batch pipeline
on the existing SQS worker pattern, and something to say when asked how the
corpus grows. It does not need to work on day one and the site is complete
without it.

### Serving (online)

The interesting engineering is here.

**The query.** "A puzzle matching these filters that this user has not seen."
Naive is `WHERE ... AND id NOT IN (every id they have solved)`, which degrades
as somebody solves more, which is exactly the wrong direction. Plan: keep the
seen set in Redis, sample candidates by a random cursor over an indexed range,
and filter the handful of candidates against the seen set in memory.

**Indexing.** Themes are an array, so GIN. Rating, length and phase are range
and equality filters, so a composite btree. Worth measuring which composite
order actually helps rather than guessing, and worth an `EXPLAIN ANALYZE` in
the writeup either way.

**Rating both sides.** The user has a puzzle rating and the puzzle has one.
Solving a hard puzzle moves you more; a puzzle everyone fails drifts upward.
This is the Glicko-2 already in `internal/rating`, applied symmetrically, and
it is the same code with the arguments swapped.

Lichess's ratings come in the dump, so imported puzzles start with a real
rating rather than a guess. Only my own generated ones need a cold start.

## Where ML honestly fits

Not in generation. Stockfish is correct and an LLM would be confidently wrong
about chess, which is the worst possible failure mode for a puzzle that claims
to have one answer.

Where it does fit: **cold-start difficulty prediction** for my own generated
puzzles, before anyone has attempted them. Features are cheap and real:
solution length, material swing, whether the key move is a capture or quiet,
piece count, whether the first move is a check. Target is the eventual settled
rating. That is an honest regression problem, small enough to explain, and it
solves a problem that actually exists.

## Filters, concretely

- **Length:** 1, 2, 3, 4, 5+ moves to finish.
- **Difficulty:** rating bands, defaulting to a window around the user's own.
- **Phase:** opening, middlegame, endgame.
- **Theme:** fork, pin, skewer, discovered attack, deflection, back rank,
  sacrifice, mate in n, and the rest of the Lichess tag set.

Plus the LeetCode part that makes it sticky: which ones you got wrong, and a
way to drill only those.

## Order of work

1. Schema and ingest. Get 6M real puzzles queryable locally.
2. The selection query, with the seen set, measured under load.
3. Solve/fail API and the Glicko-2 update on both sides.
4. UI: board, filter panel, streak, the wrong-ones list.
5. My own generator, on the existing worker pattern.
6. The difficulty model, once there are generated puzzles that need one.

## Attribution

CC0 asks for nothing, but the site should say where the puzzles came from
anyway. It costs a line and it is the truth.
