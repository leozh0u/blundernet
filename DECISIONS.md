# Engineering log

Design decisions, tradeoffs, and the bugs worth remembering. Newest first.

## The overall bet

blundernet learns chess by imitating strong human play, then improves that
policy with search and a small amount of self-play. It runs entirely on
free CI hardware, so every choice is shaped by one constraint: a run has to
finish in a couple of CPU minutes. That constraint is the reason the
network is small, the reason search is capped at hundreds of simulations,
and the reason the honest strength ceiling is around 2000 to 2300 Elo
rather than anywhere near Stockfish. The project is built to be correct and
completely measured, not to be strong.

## Board and move encoding

- **18 input planes**: 12 for piece type and color, 1 for side to move, 4
  for castling rights, 1 for the en passant file. This is the minimal set
  that makes a position fully self-describing to the network, so it never
  has to infer castling or en passant legality from context it cannot see.
- **Policy head over 4096 = 64x64 move indices** (from square, to square).
  Underpromotions collapse into the queen-promotion move that shares the
  same squares. That loses under half a percent of real moves and keeps the
  head small. A full move encoding with promotion pieces would add
  thousands of rarely-used outputs for almost no accuracy.

## Why the network is small (about 450k parameters)

6 residual blocks at 64 channels. Bigger networks predict more moves
correctly, and Leela-class engines that actually rival Stockfish run
networks 100 to 1000 times larger. But those need GPU training. On a free
CI CPU a 450k-parameter network trains on a fresh batch in roughly two
minutes, which is what makes continuous scheduled training possible at all.
The size is a deliberate trade of peak strength for the ability to run
forever unattended.

## Continuous training on scheduled CI

The pipeline runs five times a day on GitHub Actions. Each run ingests new
games, trains, evaluates, and commits the results, so the model improves
whether or not any human is watching. The checkpoint persists between runs
as a rolling GitHub release rather than a committed file, which keeps large
binaries out of git history while still carrying learning forward.

## Data ingestion

- **Source: the live top-50 Lichess blitz leaderboard.** The first version
  used a hardcoded list of strong players. Two of four runs on 2026-07-12
  found no new games because several of those accounts had gone inactive,
  and a run with no data produces only a trivial commit. The leaderboard is
  active by construction, which fixed the dry runs. `gather_batch` now also
  keeps pulling players until a run has at least 2000 positions.
- **Per-player cursors plus game-id dedup.** Each player has a "seen up to"
  timestamp and every game id is remembered, so no game is ever trained on
  twice even across overlapping fetches.
- **Values are from the side to move's perspective.** A win for the player
  on move is +1 regardless of color, which is what the value head needs to
  predict a consistent target.

## Evaluation

Two views, both on data the model never trained on:

- **Held-out move prediction** (top-1 and top-3) on a slice removed from
  each training batch.
- **A fixed 1200-puzzle tactics suite** from the Lichess puzzle database,
  240 puzzles in each of five rating bands. The set is fixed and committed
  so the metric is comparable across every run for the life of the project.
  Accuracy is reported per band, so the climb through harder tactics is
  visible rather than averaged away.

## Search: PUCT, then a C++ core

The Python MCTS in `mcts.py` is the readable reference. `cpp/mctscore.cpp`
is the fast path: the tree lives in flat arrays behind a pybind11 API, and
selection uses virtual loss so each pass gathers a batch of leaves that the
network scores in one forward call.

- **The speedup comes from batching, not from C++ itself.** Profiling
  showed the cost is dominated by neural-net forward passes and Python
  chess-rule calls, not tree bookkeeping. A batch-32 forward pass costs
  about 20 times a single one while doing 32 times the work, so evaluating
  many leaves together is the real win. End to end the C++ path searches
  about 1.8 times faster at equal simulation counts.
- **The C++ side knows no chess and Python never reads tree internals.**
  The boundary is deliberately narrow: `select_batch` returns leaves and
  their move paths, Python replays the moves and evaluates, then hands back
  priors and a value. This keeps chess rules in one language and the hot
  loop in the other.

### The virtual-loss sign bug

The first C++ build produced a mean batch size of exactly 1.0: every
selection path re-converged on the same leaf, so batching did nothing. The
cause was a sign error. Node values are stored from the perspective of the
side to move at that node, and the parent negates a child's value when
scoring it. Virtual loss is meant to make an in-flight path look
temporarily bad so sibling selections explore elsewhere. Because of the
negation, a temporary penalty has to be written as a child-perspective
*win* for it to read as a loss to the parent. With the sign flipped it read
as a bonus, every path chose the same "best" child, and no diversity
appeared. Fixing the sign brought batch sizes up and the speedup with them.
This one is worth keeping because it only shows up as a performance
symptom, not a wrong answer, so it needs profiling to catch.

## Strength measurement

- **Baseline gauntlet first.** Before any absolute rating, the engine plays
  a random mover and a one-ply material-greedy player. If it cannot beat
  those, no other number means anything.
- **Stockfish as a ruler, not a target.** Matches against Stockfish at
  fixed skill levels convert each score to an implied Elo difference, and
  the levels that produce real signal (scores between 5 and 95 percent) are
  averaged into an estimate. The skill-level to Elo mapping is approximate
  and depends on time control, so the number is an anchor, not a
  certificate. Stockfish sits near 3600 and this engine will not approach
  it. The point of the benchmark is to record the climb toward roughly 2000,
  which is what the design can reach.

## Self-play

`selfplay.py` closes the reinforcement loop. The engine plays itself with
root Dirichlet noise and early-move temperature, stores each root's visit
distribution, and trains the policy toward that distribution while fitting
the value head to the game result. At a few CPU games per day the effect on
strength is small next to supervised training, and the log says so plainly.
It is here because the loop is real and measured, not because it is the main
driver.

## A CI bug worth recording

Early scheduled runs failed with git exit code 128 on no-data days. A run
that found no new games advanced the ingest cursor, which left an
uncommitted change in `data/state.json`, and the workflow's `git pull
--rebase` refused to run on a dirty tree. The fix is in two places: the
pipeline now commits the cursor advance on no-data runs, and the push step
stages any residual change before rebasing. The lesson is that a scheduled
job has to leave a clean tree on every path, including the do-nothing path.

## Known limitations

- **Strength is capped by design** at roughly 2000 to 2300 Elo. Closing the
  gap to a top engine would need a much larger network, GPU training, and
  heavy self-play, which would end the free-CI property that defines the
  project.
- **Underpromotions are approximated** as queen promotions in the policy
  head. Rare, but a real edge case the network cannot express.
- **The Stockfish Elo estimate is approximate.** It anchors to published
  skill-level ratings at fast time controls, which is good enough to track
  progress and not precise enough to quote as an exact rating.
