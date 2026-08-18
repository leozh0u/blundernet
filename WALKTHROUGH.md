# BlunderNet, explained

Notes for defending this in an interview. Every decision, why it went that
way, what else it could have been, and the bugs that got caught along the way.
Not committed as documentation for anyone else. This is for me.

---

## What happens when someone plays a move

Worth being able to trace this from memory, because most questions are some
version of "walk me through a request."

1. Browser sends `POST /api/games/{id}/moves` with a UCI string like `e2e4`.
2. `obs.Middleware` starts a timer and wraps the ResponseWriter so the status
   code can be recorded afterwards.
3. `withUser` reads the session cookie, looks the token up in Redis, loads the
   user, and puts them on the request context. It never rejects.
4. The rate limiter spends a token from a bucket keyed by user id (registered)
   or IP address (guest or signed out).
5. The handler loads the game from Redis, replays the move list to rebuild the
   position, and validates the move against the rules.
6. The new state is written back to Redis with a Lua compare-and-set on the
   ply number. If the stored ply is not what we read, the write loses.
7. `afterChange` publishes the new state to a Redis pub/sub channel, then
   enqueues an SQS job carrying the game id and ply.
8. Every api instance holding a WebSocket for that game is subscribed to the
   channel, so the browser gets the update regardless of which instance it is
   connected to.
9. A worker receives the SQS job, checks the ply still matches, runs 300
   simulations of PUCT search over the ONNX network, and plays the reply
   through the same compare-and-set.
10. Publish again. The browser sees the engine's move.

The thing to notice: nothing in the request path waits on inference. That is
the whole reason for the queue.

---

## Decisions, and what else I could have used

### Redis holds live games, Postgres holds finished ones

Live state is read and written on every move and stops mattering when the game
ends, so it goes in Redis with a 24 hour TTL and never needs cleaning up.
Finished games are small, permanent, and queried by user, so they go in
Postgres.

**Alternatives:** everything in Postgres would work and is simpler, at the cost
of a write per move to durable storage. Everything in Redis loses games on a
restart. The split is worth defending as "different lifetimes, different
stores" rather than "Redis is fast."

### Compare-and-set on ply, in a Lua script

Two writers can act on one game: the player and the worker. Both read state,
modify, write back. Without a check, whoever writes last wins and the other
move disappears.

The script checks the stored ply is the one we read before writing. Lua because
Redis runs a script to completion, so read-check-write is atomic. Doing it as
three round trips from Go has a window between the read and the write.

**Alternatives:** `WATCH`/`MULTI`/`EXEC` gives optimistic locking without Lua,
at the cost of retry loops in application code. A Postgres row lock would work
if state lived there. A single-writer actor per game (a goroutine owning each
game) removes the race entirely but does not survive more than one instance.

### SQS with idempotent workers, not a direct call

An engine move costs about a quarter second of CPU. An HTTP request costs
almost nothing. Putting them on the same thread means a burst of games blocks
the API for everyone.

SQS delivers **at least once**, so the worker must tolerate duplicates. It does
that by carrying the ply in the job and dropping anything where the game has
moved on. There are four non-played outcomes and each is counted separately:
`expired`, `stale`, `conflict`, `error`.

**Alternatives:** Redis Streams or a Postgres-backed queue avoid the AWS
dependency and are easier to run locally. A goroutine pool is simplest but
scales with the API rather than independently, which defeats the point.

### Argon2id, not bcrypt

Argon2id is memory hard, so a GPU rig gains far less over a CPU than it does
against bcrypt. The cost parameters are encoded into the hash string, so they
can be raised later without invalidating anyone's existing password.

The subtle part: when the username does not exist, the code still runs a hash
before returning. Skipping it makes the unknown-user path measurably faster
than the wrong-password path, and that timing difference is exactly what the
shared "wrong username or password" message exists to hide.

**Alternatives:** bcrypt is fine and everywhere. scrypt is also memory hard.
PBKDF2 is the weakest of the four and only worth it for FIPS compliance.

### Sessions in Redis, not JWTs

A JWT cannot be revoked. Signing out, changing a password, or losing a laptop
all require the old credential to stop working immediately, and the usual fix
is a server-side denylist, which is a session table with extra steps.

Redis stores the SHA-256 of the token rather than the token, so dumping the
keyspace does not hand over live sessions. The token is random, so a fast hash
is fine; there is nothing to brute force the way there is with a password.

**Alternatives:** JWTs do win when you have many services that must
verify without a shared datastore. Not the case here.

### Guests are real user rows

A guest is a `users` row with `is_guest = true` and no credentials. Games
already reference `users(id)`, so a guest's games attach normally and ratings
apply. Signing up later is an `UPDATE` filling in username and password on the
same row.

This is the decision I am happiest with. The obvious alternative, keeping guest
progress somewhere separate and migrating it on signup, means writing and
testing a migration path that can fail halfway. Here there is nothing to
migrate, so there is nothing to get wrong.

### Glicko-2, not Elo

Elo carries no uncertainty, so it moves a first-timer and a hundred-game
regular by the same amount for the same result. Glicko-2 tracks a deviation
alongside the rating, moves an uncertain rating further, and widens the
deviation again while somebody is away.

**Known departure:** a rating period here is one game. The paper prefers
periods of 10 to 15, and per-game updates shrink the deviation faster than the
model intends. Traded deliberately for a number that moves when you finish a
game.

### Token bucket in Lua, keyed by identity

Same atomicity argument as the game CAS. Read, decide, write has to be one
step, or two instances handling the same user both see enough tokens.

Only registered users get a bucket of their own. Guests fall back to the
address, because a guest account is free to create, and an identity you can
mint on demand is not a rate limit.

It fails **open** on a Redis error. The limiter protects against load; it is
not an authorization check, and losing the site because the limiter is
unavailable is worse than briefly not limiting.

**Alternatives:** a fixed window is simpler and allows double the burst across
a boundary. A sliding window log is exact and stores every request timestamp.
Token bucket is the usual middle.

### Import the puzzles, do not generate them

Lichess re-analysed 600 million games with Stockfish NNUE to build their puzzle
set and spent over 100 years of CPU time doing it. They publish the result
under CC0: 6.1 million puzzles, rated and theme-tagged. Building a generator
first means spending months to catch up to something already given away.

So the corpus is imported and the **search** is the product. Neither Lichess
nor chess.com lets you ask for twenty three-move knight forks in an endgame at
1600 and drill exactly that.

The import is `cmd/puzzleload`: stream the CSV, derive the filter columns in
Go, `COPY` into an unlogged staging table, then one `INSERT ... SELECT` with
`ON CONFLICT DO UPDATE`. **6,100,960 rows in 103 seconds**, none skipped. Two
steps rather than one because the load is re-run monthly against a fresh dump
to refresh ratings, and a straight COPY would collide on every existing row
while a per-row upsert of six million rows would take hours. Unlogged staging
skips the write-ahead log, and the thing it gives up, surviving a crash, is
exactly what a re-runnable import does not need.

### Picking a random puzzle out of six million

`ORDER BY random()` sorts the whole matching set. Measured on the real import:
**1.4 seconds** for a rating band, because the planner bitmap-scanned 290k rows
and top-N sorted them.

The fix is a stored pseudo-random ordering, `sample_key`, derived from
`md5(id)` rather than `random()` so a puzzle keeps its place in the shuffle
across a reload. Selection becomes a range scan from a random cursor, which
touches only the rows it returns.

For that scan to come out in shuffled order, `sample_key` has to be the last
column of the index and **every column before it has to be an equality**. A
btree can only range on its final column. That is why rating is bucketed into
`rating_band` of 100: a band is an equality, the exact rating stays as a
recheck. It is also why selection happens inside one **cell**, one exact
(band, phase, length), rather than across a filter's whole span. Postgres will
not produce ordered output from a multi-value array on a leading index column;
it gives up and sequential scans six million rows.

Measured with the full import, `(rating_band, phase, solution_plies,
sample_key)`:

| query | before | after |
|---|---|---|
| rating band, ordered by sample_key | 1372ms | **0.9ms** |
| same, plus a common theme | | 4.5ms |
| same, plus a rare theme (bitmap AND with the GIN index) | | 7.8ms |

Two things follow from splitting by cell. Cells must be drawn **in proportion
to their size**, or a filter spanning them makes a thirteen-move puzzle as
likely as a three-move one: there are 3.4M of the latter and one of the former.
And the scan **wraps**: a cursor landing near the end of the shuffle would
otherwise make the tail of every cell unreachable. There is a test that drains
a small cell to prove every puzzle can be reached.

The counts come from `puzzle_cells`, a summary rebuilt by the loader. It is
keyed by theme as well, with `theme = ''` counting the whole cell, because a
theme is a recheck rather than an index column: drawing cells blind for
`smotheredMate`, which is one puzzle in 250, spends every draw on nothing.

End to end for a batch of 20 puzzles against 6.1M rows: **1ms** unfiltered,
**2 to 4ms** by rating and phase, **17ms** with a common theme, **38ms** with
a rare one. The rare case is the honest worst case, and the next lever for it
is a `btree_gin` index putting the cell columns inside the themes index.

### The seen set lives in Redis

"A puzzle I have not seen" as `id NOT IN (everything I have solved)` gets
slower the more somebody solves, which is backwards: the people who use the
site most would get the worst latency. The set is a Redis set, read once per
search, and candidates are filtered against it in memory. Postgres keeps the
durable record in `puzzle_attempts`; losing the Redis key costs a repeated
puzzle, not history.

### Two modes, and the solution only travels in one

Learning is a drill: filters, hints, an explanation every time, no rating,
works signed out. Ranked is a test: one puzzle at your level, no filters, no
hints, rating moves, account required. The mode is stored **on the attempt**
rather than inferred later, because "did this count" has to be answerable from
the row.

Learning ships the solution to the browser with the puzzle, which makes solving
instant and is what Lichess does too. Ranked does not: the solution stays on
the server and moves are posted one at a time to be graded. A rating that moves
is worth protecting; a drill is not.

Two more things fall out of ranked keeping a rating:

**One puzzle in progress, held server side.** If a new puzzle arrived on every
request, an unwanted one could be reloaded away until an easy one turned up,
and the rating would measure patience rather than tactics. The attempt lives in
Redis for two hours, and asking again returns the same puzzle.

**Guests are refused, and they are the only thing on the site that is.** A
guest account is minted for free by playing a game, so a rating attached to one
measures nothing. Learning mode stays open to everybody, which is what makes
the ask land as "keep your rating" rather than as a wall.

Both ratings move on a ranked attempt, in one transaction, with the user row
locked before the puzzle row so two people solving the same puzzle at once
queue instead of deadlocking. Both updates read the ratings as they were before
the attempt: feeding the new user rating into the puzzle's update would give
the second side a different opponent than the first. Imported puzzles arrive
with a Lichess rating and a small deviation, so they barely move, which is
correct. A new player's deviation is 350, so their first result moves a lot,
which is also correct and looks alarming the first time you see it: solving one
1626 puzzle took a fresh account from 1500 to 1759.

**Explanations are derived, not written.** `internal/puzzle/explain.go` replays
the line, then reports what the board says: whether it is mate, what the moved
piece now attacks, and what material the line won. Pawns are left out of the
attack list, because a rook hitting the king and a pawn is not a fork and
listing every pawn in range turns a sentence into a dump of the board. When
nothing is quotable it falls back to the theme name in a sentence. Checked
against 25 real puzzles at a time: every one produced something true, and a row
whose moves do not replay produces nothing at all rather than a guess.

### The bot is a ladder, and weakness is sampled rather than truncated

One model, six levels. Cutting the search alone makes the bot shallower but it
still plays the best move it can see, which reads as a small engine rather than
as a weaker player. So each level carries a temperature as well: the root move
is drawn from visits^(1/T) instead of taken as the maximum. Level 6 is the
maximum and plays the search's answer; level 1 looks at eight simulations and
picks something close to at random among the moves it looked at. It is weak
without ever playing a move the search never considered.

The level moves one rung after every rated game, win or lose. That is a
separate number from the Glicko rating on purpose: the rating answers "how
strong is this player" and moves slowly by design, and the ladder answers "what
should the next opponent be", which has to be current exactly when somebody is
improving fastest.

**Rating a variable opponent honestly.** The engine has no Glicko state, so
each level needs a number to be rated against. Only one of them is measured:
level 5 is the 300-simulation configuration the engine repo evaluates against
Stockfish, and 1000 is that estimate. The rest are an assumption of 120 points
a rung. Rather than hide that, the deviation says it: 50 for the measured
level, 150 for the assumed ones, so a result against a level nobody has
calibrated moves the player's rating less. Glicko-2 already knows how to
handle an uncertain opponent; this is telling it the truth.

**Learning games are not rated**, and the flag lives on the game row rather
than being inferred, for the same reason the puzzle attempt carries its mode.

### The bot aims for a close game, but only when nothing is being measured

In a learning game the bot picks its strength per move from the position it is
looking at: well ahead it drops up to two rungs, well behind it climbs up to
two. Losing forty to nothing teaches nobody anything, and winning that way
teaches them less.

Two limits keep it honest. It never moves more than two rungs from the level
the player chose, so a level one bot cannot become a level five one by falling
behind. And it never happens in a rated game or against a friend: a rating
measures a player against a fixed opponent, and an opponent that eases off the
moment you struggle is not fixed. That would hand out the rating rather than
measure it.

The signal is the value head with no search, which is the same evaluation the
review uses and is weak for the same reasons. That is tolerable here in a way
it is not there: getting the direction right is enough to keep a game close,
and the thresholds are wide (0.3 and 0.6) so a model that is unsure does not
make the bot twitch between strengths every move.

### Hints go through the queue, not the request

A hint is "what should I play here", which is the same search as an engine
move and takes about as long. Answering it inside the HTTP request would put
inference back on the request path, which is the thing the queue exists to
prevent.

So a hint is a job with `Kind: "hint"`. The worker runs the search at full
strength, publishes `{"type":"hint","uci":...}` to the game's channel, and
never touches the game. That last part is what makes duplicate delivery
harmless here: a hint arriving twice, or late, cannot move a piece. The
browser gets it on the WebSocket it already has open.

Hints are refused on rated games. A hint on a rated game is somebody else's
move on your rating.

### Streak, and what a mode is for

Three puzzle modes now, and each one exists because it answers a different
question. Learning asks "what do I want to practise", so it has every filter
and no score. Ranked asks "how good am I", so it has no filters, no hints, and
a rating that moves. Streak asks "how long can I keep it up", so puzzles climb
by forty rating points a solve and one miss ends the run. Only the best run is
kept: a streak is a game rather than a measurement, so it does not touch a
rating.

Streak lets guests play, which ranked does not. The difference is what is
being protected. A rating on a throwaway account measures nothing, so ranked
refuses one; a streak is its own reward and there is nothing to farm.

### Reviewing a game with a model that is not very good

The post-game review scores every position with the value head and no search,
then reports what each of your moves cost. The first version compared the
position before your move with the position immediately after it, which called
hanging a queen a *gain*: the capture that punishes it has not happened yet
and a value head with no search cannot see it coming. It compares against the
position after the opponent has answered instead, which is when a blunder
shows up.

That still leaves the bigger problem, and it is worth being straight about it:
the model is rated around 1000, and its opinion is worth about that much. It
scored the position after Qxf7+ Kxf7 as good for White. So the review carries
**two** signals, the network's view and the material on the board, and flags a
move when either says so. Material is not a matter of opinion, so a queen that
went for nothing is caught whatever the value head believes. The weighting
between them (a pawn is worth a third of a value point) is the one judgement
call in the file, and it is written down as one.

### Two humans on one game

A friend game is the same Redis game with a second seat. `ColorFor(userID)`
answers which side a request may move: the creator has the colour they chose,
the second person to open the link has the other, and anybody after that is
watching. Nothing about it is rated, because a second seat you can fill from
another tab is a rating anybody could farm.

The one thing that had to change on the wire is that `player_color` names the
side of whoever *made* the game, so it cannot tell the second player which end
of the board they are on. Responses to a request now carry `you`; the
broadcasts do not, because a broadcast goes to both people and cannot be
addressed to either. The browser keeps what it was told when it joined.

### A dump to S3, not a snapshot, and a restore that was actually run

The database is backed up with `pg_dump` piped through gzip into S3 every
night, kept for 30 days.

An EBS snapshot was the other option and it is worse here. A snapshot copies
the whole volume, stays tied to this account and region, and restoring one
means building an instance around it. A 220MB gzip of SQL goes into any
Postgres, including a laptop, which is what makes it possible to check. So the
restore was run: the newest dump pulled back out of S3 into a scratch database
on the box, counted (23 users, 12 games, 3,253,092 puzzles, 120,785 cells),
and dropped. Until that has been done once, a backup is a hope.

Two details worth keeping. The script refuses to upload a dump under 100KB,
because a `pg_dump` that dies halfway still exits zero through a pipe and would
otherwise overwrite good backups with a stub. And expiry is a bucket lifecycle
rule rather than a cron job that deletes old files, because the case a backup
exists for is the one where the instance is gone.

The script and its systemd units live in the Terraform boot script rather than
having been installed by hand, so a replaced instance comes back with backups
running. A backup that exists because somebody remembered to set it up once is
not a backup.

### The launch check that said no

Before putting the site in front of anyone, the puzzle search was load tested
against production with k6 (`loadtest/puzzles.js`, random filters rather than
one repeated query, or it would only measure the page cache).

| searches per second | median | p95 | failures |
|---|---|---|---|
| 1 (single request) | 240ms | | 0% |
| 2 | 169ms | 3.5s | 0% |
| 5 | 1.1s | 19.4s | 0% |
| 10 | | 49s | 3% |

The first run failed 86% of requests, and that turned out to be the rate
limiter rather than capacity: the puzzle bucket was 30 burst and 1 per second,
keyed by address for signed-out visitors, so one office or one university would
share a single request a second between everybody in it. Now 60 and 5.

With that out of the way the real ceiling showed up, and `iostat` named it in
one line: **2,599 reads a second, 21MB/s, 92% disk utilization**, with Postgres
sitting in `DataFileRead` and the CPU almost idle. The search is I/O bound, not
CPU bound. The corpus is 1.5GB of heap on a box with 910MB of memory, and the
sampler reads random rows by design, so nearly every request is a cold read
against a gp3 volume whose baseline is 3,000 IOPS.

Worth knowing which of those numbers is the wall: it is not the instance size
in the usual sense. CPU credits were full the whole time.

The cheap fixes are all about reading fewer bytes: a smaller corpus, fewer
candidate rows per scan, and not selecting columns the drill does not show
until you ask for them. The expensive fix is a box with enough memory to cache
the working set, which is a different monthly bill.

---

## Bugs that were caught, and how

The honest answer to "tell me about a bug you found" should come from here.

**The status page could never show engine latency.** The engine histogram lives
in the worker's Prometheus registry, and the api serves `/status`. Different
processes. The page would have reported the engine target as permanently
failing. Fixed by having workers publish their histogram to a Redis hash every
15 seconds and the api merge whatever is fresh.

The detail worth knowing: **buckets travel, not percentiles.** Percentiles
cannot be averaged. Merging two workers' p95 gives a number that is nobody's
p95. Cumulative bucket counts add exactly.

**Reads were minting accounts.** `GET /api/me/profile` created a guest, and had
no rate limit, so a loop over it was an unauthenticated way to fill the users
table and Redis. Fixed by resolving identity without creating on read paths.

**That chained into a rate limit bypass.** Guests got their own bucket, so:
mint a free guest on the unlimited read route, use it for a fresh 10-game
create bucket, repeat. The limit protecting the worker fleet did not bind at
all. Two separate changes were needed, and neither alone was sufficient.

**Session fixation.** Signup issued a new token but never destroyed the one the
request arrived with. A guest token that survives the upgrade still resolves to
what is now a credentialed account. Sessions rotate on signup and login now.

**Ratings could count twice.** Both the api and the worker archive a finished
game, so `SaveFinished` runs twice for most games. The rating update is gated
on the insert actually inserting, otherwise every result counted double.

**Null username crashed the profile.** Guests have no username, and the query
scanned it into a plain `string`. Every guest got a 500. Nullable columns need
nullable scan targets, and I made the same mistake twice in one session.

**The WebSocket upgrade broke silently.** The metrics middleware wrapped the
ResponseWriter, and gorilla/websocket type-asserts to `http.Hijacker` rather
than going through `http.ResponseController`. Without an explicit `Hijack`
method, every upgrade returns 500. Found by checking the gorilla source rather
than assuming.

**Dead schema.** I added `last_seen_at` with an index commented as "what the
reaper walks", then never wrote the reaper or the column. Dropped both and
wrote a real reaper keyed on `created_at`.

**A bash 4 feature on a bash 3.2 machine.** `${user^^}` in the e2e script works
on CI's Ubuntu and fails on macOS. CI would have stayed green while the laptop
broke.

**An unset filter matched nothing instead of everything.** A nil Go slice
reaches Postgres as NULL, so `cardinality($1) = 0 OR themes @> $1` evaluated to
NULL rather than true and every row was dropped. Searching with no theme
returned an empty list. Caught by a test asking for puzzles with the zero value
filter, which is the case a person hits first.

**The rare-theme search returned nothing at all.** The sampler drew cells
weighted by total size, and for a theme covering one puzzle in 250 every draw
landed in a cell holding none of them. It gave up after twelve scans and
answered empty. Fixed by counting puzzles per theme per cell, so only cells
that can answer are ever drawn. Found by a benchmark asserting on the result,
not just on the timing.

**The importer that ran fine on a laptop and ground for 25 minutes on the
box.** The cell summary aggregates 13 million unnested theme rows. Postgres
defaults `work_mem` to 4MB, so on the deploy box that sort spilled to disk and
took 25 minutes, while the same default on a laptop finished in seconds
because everything fitted in page cache. The refresh sets its own `work_mem`
now. The lesson worth keeping: "it is fast on my machine" was a statement
about memory, not about the query.

**A "first render" flag that React spent twice.** Opening a shared puzzle link
loaded the puzzle and then immediately replaced it with a random one. The
effect that reloads when you switch drill lists was guarded by a flag meant to
skip the first run, and React runs mount effects twice in development, so the
flag was spent on the first pass and the second pass ran a search over the top
of the shared puzzle. The guard compares the value now rather than counting
renders.

**A hint that fired and did nothing.** The hint button worked and the board
never changed, because the code lighting up squares still read `hint.from`
after the field had been renamed to `hint.squares` in an unrelated pass. It
printed one line of text in the corner and looked broken. Hints are drawn on
an SVG over the board now, which is also how they got to point at things.

**A placeholder used twice in one comparison lost its type.** Reading a whole
small cell needed a predicate that always passes, and `$4 = $4` gave Postgres
nothing to infer from, so it assumed text and pgx refused to send an int into
it. The fix was to stop being clever and scan from the smallest possible
cursor.

---

## Edge cases worth having an answer for

**Two tabs, same game.** Both hold WebSockets, both subscribe to the same
pub/sub channel, both see every update. Moves from either tab go through the
same CAS, so the second one to act on a stale position loses cleanly.

**The player moves while the engine is thinking.** Cannot happen: the handler
checks it is the player's turn, and the position only advances when the engine
writes.

**A duplicate SQS delivery.** The ply check drops it. There is a test proving a
double delivery moves the engine exactly once.

**Two workers get the same job.** Both compute a move; the CAS means only one
commits, and the loser records a `conflict` outcome rather than an error.

**The game expires mid-thought.** Redis TTL is 24 hours. The worker gets
`ErrNotFound`, records `expired`, and acks the message rather than retrying
forever.

**Account deleted between the game ending and the archive write.** The game is
still archived with a null `user_id`, which is the `ON DELETE SET NULL` on the
column doing its job.

**Redis goes down.** Health check fails, `/api/status` returns 503, the uptime
probe fails after two consecutive bad responses. The rate limiter fails open so
it is not also the thing breaking.

**Everything behind one NAT.** Anonymous callers share a bucket per address, so
a university or an office gets squeezed first. This is why the limits are
configurable and why `blundernet_rate_limited_total` exists.

**Signing up in two tabs at once as the same guest.** One `UPDATE` wins on
`WHERE is_guest`, the other affects zero rows and gets a 409.

**A guest that never plays.** Reaped after 30 days if it has no games. Ones
that played are kept.

---

## What I would change

**The database dies with the instance.** `user_data_replace_on_change` is true,
so any boot script edit replaces the box. Postgres now lives on a separate EBS
volume with `prevent_destroy`, but that only landed after I had already
noticed the risk. There are no backups.

**One box, so 99% availability and no more.** Three nines needs a second
instance behind a load balancer, which is the reference stack at six times the
cost. That is a budget decision, not an engineering one, and worth saying that
way.

**The engine rating is a constant.** `EngineRating = 1000` is hardcoded. Ship a
stronger model and everyone who beats it is inflated against the old number.

**No email, so no password reset.** If you forget your password the account is
gone.

**Rate limits are guesses.** They were picked before any traffic existed. The
refusal metric exists so they can be tuned against something real.

**The frontend is one component.** `App.jsx` holds the board, the move list,
the WebSocket, and the game state. Fine at this size, wrong at twice it.
