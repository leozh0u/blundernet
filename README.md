# BlunderNet

[![ci](https://github.com/leozh0u/blundernet/actions/workflows/ci.yml/badge.svg)](https://github.com/leozh0u/blundernet/actions/workflows/ci.yml)

A chess site with two halves: **3,253,092 puzzles you can actually filter**, and games against [the BlunderNet engine](https://github.com/leozh0u/blundernet-engine), a neural network I trained from scratch.

Live at **https://blundernet.com**.

The puzzles are the part I think is worth something. Chess.com and Lichess both serve them the same way: here is one at roughly your rating, next. Neither lets you say "give me twenty three-move knight forks in an endgame at 1600." The corpus is Lichess's CC0 set, which cost them over a hundred years of CPU time to generate and which they gave away, so generating puzzles is not the interesting problem. Making them searchable is. The LeetCode comparison is the honest one: LeetCode did not invent the problems, it made them filterable, rated, and trackable.

The engine repo answers "can I train a model?" This repo answers a different question: can I serve one? Stateless Go API instances with game state in Redis, engine inference decoupled onto queue-fed workers, a puzzle sampler that draws uniformly from three million rows without sorting them, and the whole thing defined in Terraform.

**Stack:** Go, React, PostgreSQL, Redis, SQS, ONNX Runtime, Docker, Terraform, AWS (ALB, ECS Fargate, ElastiCache, RDS)

## What is on the site

**Puzzles.** Filter by rating, solution length, game phase, theme and opening, then drill. Hints glow the piece, then draw an arrow, then play the move. Wrong answers go on a list you can drill again later, and there is a "another like this" link that is just the filter set as a URL.

**Ranked puzzles.** The solution stays on the server. Both you and the puzzle carry a Glicko-2 rating, and a miss costs more the higher you climb: 1x at 1200, rising to 2.5x. Needs an account, which is the honest consequence of a rating meaning anything.

**Streak.** Puzzles climb 40 rating points per solve. One miss ends the run.

**Play the engine.** Six levels off one model. In learning games the bot adapts to you mid-game; in rated and friend games it never does.

**Play a friend** over a link, unrated. **Post-game review** flags the worst moves using the value head plus material.

**Accounts recover without email.** The site collects no email address, so there is no reset link to send. Every account gets one recovery code at signup instead, shown once and stored as an Argon2id hash, in the same format and with the same parameters as a password. Spending a code retires it and signs out every other session, because recovery is what somebody reaches for when they think another person has their password.

**The board has sound**, synthesised with the Web Audio API rather than sampled. Recorded clicks would mean a licence that permits commercial redistribution, hosting, and a few hundred kilobytes on a bundle that is 100KB gzipped. A move is a short pitched knock; a capture is lower and longer; check is two rising notes. It remembers being turned off.

## Architecture

```
 Browser (React) ──HTTP/WebSocket──▶ ALB ──▶ api fleet (ECS Fargate, autoscaled on CPU)
                                              │        │
                                    live game state,   │ move jobs
                                    pub/sub fanout     ▼
                                              │      SQS ──▶ worker fleet (autoscaled on
                                              ▼                queue depth, ONNX Runtime)
                                            Redis              │
                                              ▲────────────────┘
                                            engine reply, pub/sub
                                              │
                                              ▼
                                          Postgres (finished games, puzzles, stats)
```

A move makes the following trip. The api validates it against the chess rules, writes the new state to Redis with a compare-and-set, publishes the update, and enqueues a job. A worker picks the job up, runs the position through the network, plays the reply through the same compare-and-set path, and publishes again. Every browser watching the game gets both updates pushed over its WebSocket, whichever api instance it happens to be connected to.

A puzzle search does not touch that path at all. It is one Postgres read, described below.

## Design notes

**Sampling three million rows without sorting them.** The obvious query for "a random puzzle matching this filter" is `ORDER BY random()`, and over six million rows it took 1.4 seconds, because it sorts the whole matching set to take one row off the top. The fix has two parts. Every puzzle gets a stored `sample_key`, the first four bytes of the md5 of its id read as an integer, which is a fixed random shuffle computed once at import. And the filter grid is precomputed into a summary table, `puzzle_cells`, one row per (rating band, phase, solution length, theme) with a count.

A search then draws a cell in proportion to how many puzzles it holds, and range scans that cell from a random cursor in shuffle order. Every column before `sample_key` in the index is an equality, so the cursor is a seek rather than a sort. 0.9 ms. Drawing cells in proportion to size is what keeps the result uniform over the whole matching set rather than uniform over cells, which would otherwise make thirteen-move puzzles as common as three-move ones.

Two bugs in that came out of running it rather than reading it. A nil Go slice reaches Postgres as NULL, and every comparison against NULL is NULL rather than false, so an unset filter silently matched nothing instead of everything. And rare themes returned empty because the sampler was drawing cells that held none of them, which is why the cell counts are keyed by theme and the weights come from the rarest theme asked for.

**The puzzle search was I/O bound, and the fix was half code and half money.** Load tested against production with k6. The corpus heap is 1,095MB with 270MB of indexes, and the box had 910MB of RAM. The sampler reads random rows by design, so the working set did not fit and most reads were cold: 2,599 reads a second at 92% disk utilization, Postgres sitting in `DataFileRead`, CPU almost idle.

The first thing the load test measured, though, was the load test. It sent a theme on seven requests in eight, and the filter panel on the site opens empty, so the ordinary request carries no filter at all. Same code, two searches a second: 1.32 s median on that mix, 92 ms on a realistic one. The ceiling was a property of the test.

The expensive request is a themed one. A theme is a recheck against the heap rather than an index condition, and the common themes are not that common inside a cell: `skewer` is one row in 36, `backRankMate` one in 34, `mateIn2` one in 3.5. Because the scan walks the shuffle, those 36 rows are 36 random pages. Past a point the planner abandons the ordered scan for a `BitmapAnd` of the sample and theme indexes and then sorts everything to apply the limit, which measured 5,154 pages and 2.3 seconds for 32 rows. Forcing the ordered scan back on is worse, at 9,399 pages.

So the scan asks for fewer rows. It used to take a fixed 32; it now asks for what the caller still needs plus two. Measured over twelve real cells for each of seven themes, total pages touched went from 55,821 to 20,602.

The fixed number was tried first and was wrong in a way worth keeping here, because it looked right. A constant smaller than the batch means one scan cannot fill the request, so the sampler draws a second cell, and cells are taken out of the pool as they are drawn. The second draw therefore comes from what is left rather than from the real distribution. Over a corpus of 1000 three-ply and 100 five-ply puzzles, asking for 25 returned 79% three-ply where the population is 91%. Deriving the limit from the outstanding request keeps the common case at one cell, drawn in proportion, which is the entire point of the design.

That is the cheap lever, and it does not fix the cause. The fix that would is an index the theme can be tested against without touching the heap, and it does not fit: 73 themes over 14.8M theme-rows is the size of the corpus again. What was left was memory, which is a monthly bill rather than an engineering decision, so the box went from a t4g.micro to a t4g.small: 2GB instead of 1GB, $12.26 a month instead of $6.13. `shared_buffers` and `effective_cache_size` moved with it, since an instance size and a planner told the wrong size about it are the same change.

Both levers, at two searches a second, and a third column for the mix the site is actually asked for:

| | stress median | stress p95 | real median | real p95 |
|---|---|---|---|---|
| t4g.micro, 32 candidates | 1.32 s | 9.49 s | 92 ms | 1.0 s |
| t4g.micro, 12 candidates | 158 ms | 2.99 s | 92 ms | 342 ms |
| t4g.small, 12 candidates | 50 ms | 1.01 s | 23 ms | 369 ms |

Disk went from a flat 92% utilization to bursts between 11% and 86%, and `buff/cache` now sits at 1,426MB against a 1,365MB working set, which is the whole point: it fits. The realistic mix holds a 22 ms median at five searches a second, and one search is a batch of ten puzzles.

Two honest caveats. The first row was measured on a colder cache than the last, so some of that improvement is free. And the numbers immediately after the resize were worse in the tail than before it, because a rebooted box has an empty `shared_buffers`; these are from the second pass, once it warmed.

**A game id is not a credential.** Moves checked that the caller held a seat at the game; resignation did not, and `ColorFor` returned true for anybody on a game against the bot. So anyone holding an id could end somebody else's game as a loss, and a rated loss moves a real rating: a test session took a player from 1000 to 686 without ever touching the game. Ids are UUIDs, but friend game ids travel in links by design and every id sits in the address bar, so unguessability was never the protection. The seat check now lives in `ColorFor` itself, which covers both routes, and resignation resigns the caller's own colour rather than the creator's, which separately fixed a friend game bug where pressing resign handed you the win.

**The api servers hold no state.** Live games exist in Redis with a 24-hour TTL, finished games in Postgres, and the servers themselves only hold WebSocket connections. Any instance can serve any request, which is what lets the fleet scale horizontally and lets a task die mid-game without the player noticing. Cross-instance WebSocket delivery works because every instance subscribes to game events over Redis pub/sub rather than keeping per-game connection registries.

**Inference runs behind a queue on purpose.** An engine move costs real CPU while an HTTP request costs almost none, and the two should not compete for the same cores or scale on the same signal. The api fleet scales on CPU, the worker fleet scales on queue backlog, and a burst of new games turns into queue depth instead of timeouts.

**SQS delivers at least once, so the worker is idempotent.** Each job carries the ply it was created for. A worker that receives a stale or duplicated job (the game moved on, the game ended, a second delivery of the same ply) drops it without side effects. The Redis write is a Lua compare-and-set on the ply, so even two workers racing on the same job cannot both win. There is a test that proves a double delivery moves the engine exactly once.

**The worker searches; the network guides.** A raw policy network plays plausible openings and then hangs pieces, because a single forward pass calculates nothing. The worker instead runs PUCT Monte-Carlo Tree Search (the same algorithm the engine trains with): the policy head supplies move priors, the value head scores leaf positions, and a few hundred simulations turn intuition into calculation. `ENGINE_SIMS` sets the strength knob (default 300, about a quarter second per move; 1 disables search entirely). Search also papers over a measured blind spot: in positions unlike the training data the policy can assign a mating move a near-zero prior and starve it of visits, so the worker probes one ply for immediate mates before trusting the tree.

**Underpromotions are folded into queen promotions.** The policy head indexes moves as from-square times 64 plus to-square, which cannot distinguish promotion pieces. The training pipeline made that tradeoff (it costs well under 1% of moves), so the serving path mirrors it exactly. The board encoding in Go reproduces the Python training encoder plane for plane, and the parity is pinned by tests on both sides of the export.

**Both binaries log JSON and expose metrics.** Logs go through `slog` with a `service` field, including the startup failures, since an unstructured line is the one entry that most needs to survive the pipeline. Metrics are Prometheus, on port 9090 on a listener of their own so scraping never crosses the load balancer and `/metrics` is not reachable from outside. HTTP series are labelled by the ServeMux pattern rather than the path, so `/api/games/{id}` stays one series instead of one per game. WebSocket requests are counted but left out of the latency histogram, because a connection lives as long as the game and timing it would measure session length. The worker counts job outcomes separately for played, expired, stale, conflict and error, which is the idempotency logic made visible: the last three are it working, not failing.

**No NAT gateway.** The VPC has public subnets only, with isolation done by security groups. Fargate tasks get public IPs so they can pull images, and a NAT gateway would add about $32 a month to serve no traffic. The stack is built to be stood up for a demo and torn down after: `make deploy`, play, `make destroy`.

## Running it locally

Requires Docker. The model artifact is optional; without it the worker uses a small material searcher instead of the network.

```
docker compose up -d --build     # postgres, redis, elasticmq, api, worker
open http://localhost:8080
./scripts/e2e.sh                 # scripted game against the engine
```

Puzzles are a separate import, streamed from Lichess rather than kept as a file. The full set is 6.1 million rows and about 2.5GB with indexes, so pass a limit for a local set:

```
make puzzles PUZZLE_ARGS="-limit 100000"
make puzzles                     # the whole thing, ~100 seconds on a laptop
```

Re-running it monthly is how puzzle ratings stay current. Production carries the `popularity >= 90` subset, 3,253,092 of the 6.1M: the ones below that bar are puzzles Lichess users downvoted, usually for having a second decent move, so the filter is a quality one as much as a size one.

To regenerate the model from the engine repo:

```
python scripts/export_onnx.py --repo ../blundernet-engine --out models/blundernet.onnx
```

The export script checks that ONNX Runtime and PyTorch produce identical outputs before it succeeds. On this laptop a single position evaluates in 0.64 ms on CPU, which is why the workers do not need GPUs.

### Multiple instances

The statelessness claim is testable locally. The `scale` profile starts an nginx load balancer in front of however many api replicas you ask for:

```
docker compose -f compose.yaml -f compose.scale.yaml up -d --build --scale api=3
BASE=http://localhost:8090 ./scripts/e2e.sh   # requests spread across replicas
docker compose ps -q api | head -1 | xargs docker kill   # kill one mid-game
BASE=http://localhost:8090 ./scripts/e2e.sh   # still passes
```

Games survive instance death because no instance owns a game: state lives in Redis, and move events reach every browser through pub/sub regardless of which replica holds its WebSocket.

## Load tests

Two, because the site has two halves and they break for different reasons.

`loadtest/game_flow.js` drives real games: create, subscribe over the same WebSocket a browser uses, play a move, wait for the engine's reply, resign. Move latency is measured from the move request to the reply arriving on the socket, so it covers the whole path rather than a polling interval. `steady` holds a constant arrival rate; `ramp` climbs past saturation to find the ceiling and give the worker autoscaling policy something to react to.

```
k6 run -e BASE=http://<alb-dns> -e SCENARIO=steady -e RATE=1.5 loadtest/game_flow.js
k6 run -e BASE=http://<alb-dns> -e SCENARIO=ramp   -e PEAK=2   loadtest/game_flow.js
```

`loadtest/puzzles.js` measures the search. `MIX` picks which population of filters to send, and the two answer different questions. `real` is what the site is asked for: the filter panel opens empty, so most fetches carry no filter at all. `stress` sends a theme on nearly every request, which is the expensive path described above. Reporting only the stress number understates the site and reporting only the real number hides the cliff, so run both.

```
k6 run -e BASE=https://blundernet.com -e MIX=real   -e RATE=5 -e DURATION=45s loadtest/puzzles.js
k6 run -e BASE=https://blundernet.com -e MIX=stress -e RATE=2 -e DURATION=45s loadtest/puzzles.js
```

One search returns a batch of ten that the browser queues and works through, so five searches a second is fifty puzzles a second of solving.

### Results

Measured against the Fargate stack in us-east-1: two api tasks, workers on 0.5 vCPU running 300-simulation MCTS, autoscaling from one to four tasks on queue depth.

Steady, 1.5 games/s for three minutes, four workers:

```
engine move latency   p50 1.12 s   p95 1.25 s   p99 1.45 s
moves answered        270 / 270
http requests         810, zero failures
```

API latency, measured at the load balancer so it excludes client network time:

```
TargetResponseTime    p50 3.4 ms   p95 9.1 ms   p99 21-31 ms
```

Ramp to 2 games/s, roughly three times what a single worker sustains:

```
http requests         2700, zero failures, p99 496 ms (includes ~330 ms client RTT)
engine move latency   p50 12 s, p99 58 s
backlog               grew to 113 messages, drained to zero in ~90 s after scale-out
workers               1 -> 4, triggered at t+467 s
```

Three things worth reading out of that. The API never failed or slowed under three times the load the engine could absorb, because nothing in the request path waits on inference: the queue takes the overflow and the cost lands on move latency instead of errors. Autoscaling did resolve the backlog, clearing 113 queued moves within about ninety seconds of the new tasks starting. But it took roughly five minutes to react at all, because SQS publishes queue depth once a minute and target tracking wants several breaching points before it moves. For a workload where a player is watching the board, that is too slow to be the only defence; provisioning closer to peak, or stepping on a shorter metric, would matter more than the scaling policy itself.

The bottleneck for games is the engine, not the platform. Each move costs about 1.6 s of CPU on a 0.5 vCPU task, so one worker sustains roughly 0.6 moves/s and four sustain about 2.4. Raising simulations, task CPU, or batching leaf evaluations inside a search all move that number; none of them touch the API.

The bottleneck for puzzles is the disk on the single box, which is the section above.

## What it promises

Four targets, measured over a calendar month. They are deliberately set where the measurements above say the system already sits, so missing one means something changed rather than that the target was always fiction.

| | Target | Where it comes from |
|---|---|---|
| Site reachable | 99% | One box, no redundancy. See below. |
| Requests not failing | 99.9% non-5xx | Load tests ran 3,510 requests with zero failures. |
| API latency | p99 under 50 ms | Measured 21 to 31 ms at the load balancer. |
| Engine reply | p95 under 3 s | Measured 1.25 s with four workers. |

99% allows about seven hours of downtime a month, which is a weak number and an honest one. The always-on deployment is a single instance, so a reboot, a bad deploy, or the host going away is a full outage with nothing to fail over to. Promising 99.9% would need a second instance and a load balancer, which is the reference stack, which is the thing that costs $60 a month rather than $10. Availability here is a budget decision, not an engineering one, and quoting three nines off a single box would be the kind of number that falls apart the first time someone asks how.

There is deliberately no puzzle search target yet. The number would be honest only for the filter mix I chose to measure, and the spread between the two mixes is wide enough that one figure would hide more than it told.

The latency target covers `/api/` only. Serving the frontend bundle and holding WebSocket connections are different jobs: a WebSocket lives as long as the game, so timing it measures how long someone played rather than how fast the service answered.

The engine target is the one under real pressure. A move costs about 1.6 s of CPU, so a single worker sustains roughly 0.6 moves a second. Past that, the queue absorbs the overflow and the cost lands on move latency instead of errors, which is the tradeoff the architecture was built to make. It also means the engine target breaks well before the API one does, and it breaks first for whoever is unlucky enough to be playing at the time.

`/status` reports current numbers against these. It is server-rendered with no scripts and no stylesheet request, because a status page that depends on the app it reports on cannot tell you the app is down. `/api/status` is the same data as JSON and answers 503 when a dependency is failing.

Availability is the one target the service cannot measure about itself, since a process that is down cannot report that it is down. `.github/workflows/uptime.yml` probes `/api/status` from outside AWS every ten minutes and fails the run after two consecutive bad responses. It stays inert until the `SITE_URL` repository variable is set. GitHub's scheduled runners are best effort, though: runs queue, arrive late, and stop entirely after 60 days of repo inactivity, so this is a backstop with a record in the Actions log rather than a pager.

## Backups

`pg_dump` piped through gzip to S3 on a systemd timer at 03:15 UTC, 30 day lifecycle, bucket private and encrypted, instance role scoped to that one bucket. A dump rather than an EBS snapshot because a 220MB gzip of SQL restores into any Postgres, including a laptop, which is what makes it testable.

It was tested rather than assumed: the latest dump was pulled from S3 and restored into a scratch database on the box, coming back with the right user, game, puzzle and cell counts, then dropped. The script and the systemd units live in the Terraform boot script rather than having been installed by hand, so a replaced instance comes back with its backups running. A backup that exists because somebody remembered to set it up once is not a backup.

## Two deployments, on purpose

The repo ships two stacks, because the architecture worth designing and the architecture worth paying for every month are not the same thing.

`deploy/terraform` is the reference design: an autoscaling api fleet behind an ALB, workers scaled on queue depth, ElastiCache, RDS. It is what the system should look like under load, it has been deployed and exercised end to end, and it costs roughly $60 a month to leave running. So it goes up on demand and comes down after.

`deploy/demo` is the version cheap enough to leave running: one t4g.small with the same two container images against a real SQS queue, Postgres and Redis alongside, Caddy in front, about $17 a month. Same code, same queue semantics, under a third of the bill. A demo link does not need six tasks and a load balancer, and pretending otherwise would be an expensive way to make a point. `deploy/demo` is what serves blundernet.com today.

```
make demo-deploy    # build arm64 images, push, stand up the box
make demo-update    # ship new code to the running box
make demo-destroy   # take it down
```

Point a domain's A record at the instance IP and set `domain` in `deploy/demo`, and Caddy issues a certificate automatically on the next apply.

## Deploying the full stack

```
cd deploy/terraform
export TF_VAR_db_password=...
make -C ../.. deploy    # terraform apply, build and push images, roll services
make -C ../.. destroy   # tear it all down
```

Terraform creates the VPC, ALB, ECS cluster and services, ElastiCache, RDS, the SQS queue with a dead-letter queue, ECR repositories, IAM roles scoped so the api can only send to the queue and the worker can only consume from it, and CloudWatch log groups. CI validates the configuration on every push.

## Layout

```
cmd/api, cmd/worker      the two binaries
cmd/puzzleload           streaming importer for the Lichess CC0 dump
internal/game            chess domain: move lists, legality, outcomes
internal/engine          board encoding, ONNX inference, fallback searcher
internal/puzzle          puzzle domain: themes, phases, solution lengths
internal/rating          Glicko-2, for players and puzzles alike
internal/store           Redis (live state, CAS, pub/sub) and Postgres (archive, puzzles)
internal/queue           SQS client, ElasticMQ-compatible for local dev
internal/httpapi         REST + WebSocket handlers, embedded frontend
internal/obs             JSON logging, Prometheus metrics, HTTP middleware
internal/testdb          a schema per test package, so packages stop truncating each other
web/                     React frontend, built into the api binary
deploy/terraform         the AWS stack
deploy/demo              the one-box stack that serves the site
loadtest/                k6 scenarios: game flow and puzzle search
```

The puzzle data is Lichess's, used under CC0.
