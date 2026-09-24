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

## Two modes

They are different products sharing a corpus, and keeping them apart is what
stops either from being watered down.

### Learning

A drill. Everything customizable: filter by length, difficulty, phase and
theme, then work through as many as you want. Hints available. An explanation
after every one, right or wrong. A "another like this" button that carries the
puzzle's own tags straight back into the filter, so a fork you just failed
becomes twenty more forks.

**No rating, and no account needed.** Nothing here is a measurement, so there
is nothing to protect and nothing to lose by letting anyone in. This is the
part a recruiter clicking the link can use immediately.

### Ranked

A test. One puzzle at your level, no filters, no hints, no second try. Rating
moves on every attempt, both yours and the puzzle's. Same shape as chess.com
and Lichess rated puzzles, because that format works and does not need
reinventing.

**Account required.** This is the first thing on the site that genuinely needs
one, which makes it the honest place to ask. Everything else stays free and
signed out, so the ask lands as "keep your rating" rather than a wall.

The mode is stored on each attempt rather than inferred, because "did this
count towards my rating" has to be answerable from the row. Learning attempts
never touch a rating, on either side.

## Hints, explanations, and the next one

**Hints, progressively.** Which piece moves, then which square, then the move
itself. Each one is recorded on the attempt, because solving cold and solving
after three hints are different things and the drill list should know.

**Explanations, derived rather than written.** Every puzzle arrives tagged, and
the position after the solution is inspectable, so the explanation is
assembled: which enemy pieces the moved piece now attacks, what was hanging,
whether it was mate. "The knight lands on f7 attacking the king and the rook"
falls out of the board, not out of a model.

Deliberately **not** an LLM. Asking one to explain chess is asking it to be
confidently wrong next to a solution that is provably right, which is worse
than saying less. The tags and the position are enough for the common cases,
and the honest fallback for the rest is the theme name and the evaluation
swing.

**"Another like this"** needs no similarity model. Take the current puzzle's
theme, length and rating band, and feed them back in as the filter. It is the
search that already exists, seeded from where you are standing.

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

1. Schema and ingest. Get 6M real puzzles queryable locally. Schema is done,
   migrations 0006 and 0007.
2. The selection query, with the seen set, measured under load.
3. Learning mode first: filters, solve/fail, no rating. It works signed out,
   so it is testable without touching accounts.
4. Hints and derived explanations.
5. "Another like this", which is the filter seeded from the current puzzle.
6. Ranked mode: account gate, Glicko-2 on both sides, no hints.
7. The wrong-ones drill list.
8. My own generator, on the existing worker pattern.
9. The difficulty model, once there are generated puzzles that need one.

## Attribution

CC0 asks for nothing, but the site should say where the puzzles came from
anyway. It costs a line and it is the truth.
