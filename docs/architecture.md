# Architecture

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

**The database outlives the box it runs on.** Editing the boot script replaces the instance, because `user_data_replace_on_change` is what makes the machine match the code. The boot script is also where the Postgres tuning, the Caddyfile, the systemd units and the backup timer live, so those edits happen. Postgres therefore writes to a separate EBS volume with `prevent_destroy`, mounted by a boot script that formats the device **only when `blkid` says it has never been used**: a first boot makes a filesystem, every replacement after that remounts the data. Without that, one config change deletes every account on the site. Getting there cost one planned outage, since the new volume starts empty: dump to S3, replace the instance, restore.

**A game id is not a credential.** Moves checked that the caller held a seat at the game; resignation did not, and `ColorFor` returned true for anybody on a game against the bot. So anyone holding an id could end somebody else's game as a loss, and a rated loss moves a real rating: a test session took a player from 1000 to 686 without ever touching the game. Ids are UUIDs, but friend game ids travel in links by design and every id sits in the address bar, so unguessability was never the protection. The seat check now lives in `ColorFor` itself, which covers both routes, and resignation resigns the caller's own colour rather than the creator's, which separately fixed a friend game bug where pressing resign handed you the win.

**The api servers hold no state.** Live games exist in Redis with a 24-hour TTL, finished games in Postgres, and the servers themselves only hold WebSocket connections. Any instance can serve any request, which is what lets the fleet scale horizontally and lets a task die mid-game without the player noticing. Cross-instance WebSocket delivery works because every instance subscribes to game events over Redis pub/sub rather than keeping per-game connection registries.

**Inference runs behind a queue on purpose.** An engine move costs real CPU while an HTTP request costs almost none, and the two should not compete for the same cores or scale on the same signal. The api fleet scales on CPU, the worker fleet scales on queue backlog, and a burst of new games turns into queue depth instead of timeouts.

**SQS delivers at least once, so the worker is idempotent.** Each job carries the ply it was created for. A worker that receives a stale or duplicated job (the game moved on, the game ended, a second delivery of the same ply) drops it without side effects. The Redis write is a Lua compare-and-set on the ply, so even two workers racing on the same job cannot both win. There is a test that proves a double delivery moves the engine exactly once.

**The worker searches; the network guides.** A raw policy network plays plausible openings and then hangs pieces, because a single forward pass calculates nothing. The worker instead runs PUCT Monte-Carlo Tree Search (the same algorithm the engine trains with): the policy head supplies move priors, the value head scores leaf positions, and a few hundred simulations turn intuition into calculation. `ENGINE_SIMS` sets the strength knob (default 300, about a quarter second per move; 1 disables search entirely). Search also papers over a measured blind spot: in positions unlike the training data the policy can assign a mating move a near-zero prior and starve it of visits, so the worker probes one ply for immediate mates before trusting the tree.

**Underpromotions are folded into queen promotions.** The policy head indexes moves as from-square times 64 plus to-square, which cannot distinguish promotion pieces. The training pipeline made that tradeoff (it costs well under 1% of moves), so the serving path mirrors it exactly. The board encoding in Go reproduces the Python training encoder plane for plane, and the parity is pinned by tests on both sides of the export.

**Both binaries log JSON and expose metrics.** Logs go through `slog` with a `service` field, including the startup failures, since an unstructured line is the one entry that most needs to survive the pipeline. Metrics are Prometheus, on port 9090 on a listener of their own so scraping never crosses the load balancer and `/metrics` is not reachable from outside. HTTP series are labelled by the ServeMux pattern rather than the path, so `/api/games/{id}` stays one series instead of one per game. WebSocket requests are counted but left out of the latency histogram, because a connection lives as long as the game and timing it would measure session length. The worker counts job outcomes separately for played, expired, stale, conflict and error, which is the idempotency logic made visible: the last three are it working, not failing.

**No NAT gateway.** The VPC has public subnets only, with isolation done by security groups. Fargate tasks get public IPs so they can pull images, and a NAT gateway would add about $32 a month to serve no traffic. The stack is built to be stood up for a demo and torn down after: `make deploy`, play, `make destroy`.

