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

**Alternatives:** JWTs genuinely win when you have many services that must
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
