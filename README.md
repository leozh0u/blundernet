# BlunderNet

[![ci](https://github.com/leozh0u/blundernet/actions/workflows/ci.yml/badge.svg)](https://github.com/leozh0u/blundernet/actions/workflows/ci.yml)

Play chess against [the BlunderNet engine](https://github.com/leozh0u/blundernet-engine), a neural network I trained from scratch, at a site built to hold up when many people play at once.

The engine repo answers "can I train a model?" This repo answers a different question: can I serve one? The answer here is a Go service fleet behind a load balancer, with game state in Redis, engine inference decoupled onto queue-fed workers, finished games archived in Postgres, and the whole thing defined in Terraform.

**Stack:** Go, React, PostgreSQL, Redis, SQS, ONNX Runtime, Docker, Terraform, AWS (ALB, ECS Fargate, ElastiCache, RDS)

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
                                          Postgres (finished games, stats)
```

A move makes the following trip. The api validates it against the chess rules, writes the new state to Redis with a compare-and-set, publishes the update, and enqueues a job. A worker picks the job up, runs the position through the network, plays the reply through the same compare-and-set path, and publishes again. Every browser watching the game gets both updates pushed over its WebSocket, whichever api instance it happens to be connected to.

## Design notes

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

## Load test

`loadtest/game_flow.js` drives real games: create, subscribe over the same WebSocket a browser uses, play a move, wait for the engine's reply, resign. Move latency is measured from the move request to the reply arriving on the socket, so it covers the whole path rather than a polling interval.

Two scenarios. `steady` holds a constant arrival rate to measure latency at a load the fleet can carry; `ramp` climbs past saturation to find the ceiling and give the worker autoscaling policy something to react to.

```
k6 run -e BASE=http://<alb-dns> -e SCENARIO=steady -e RATE=1.5 loadtest/game_flow.js
k6 run -e BASE=http://<alb-dns> -e SCENARIO=ramp   -e PEAK=2   loadtest/game_flow.js
```

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

The bottleneck is the engine, not the platform. Each move costs about 1.6 s of CPU on a 0.5 vCPU task, so one worker sustains roughly 0.6 moves/s and four sustain about 2.4. Raising simulations, task CPU, or batching leaf evaluations inside a search all move that number; none of them touch the API.

## What it promises

Four targets, measured over a calendar month. They are deliberately set where the measurements above say the system already sits, so missing one means something changed rather than that the target was always fiction.

| | Target | Where it comes from |
|---|---|---|
| Site reachable | 99% | One box, no redundancy. See below. |
| Requests not failing | 99.9% non-5xx | Load tests ran 3,510 requests with zero failures. |
| API latency | p99 under 50 ms | Measured 21 to 31 ms at the load balancer. |
| Engine reply | p95 under 3 s | Measured 1.25 s with four workers. |

99% allows about seven hours of downtime a month, which is a weak number and an honest one. The always-on deployment is a single instance, so a reboot, a bad deploy, or the host going away is a full outage with nothing to fail over to. Promising 99.9% would need a second instance and a load balancer, which is the reference stack, which is the thing that costs $60 a month rather than $10. Availability here is a budget decision, not an engineering one, and quoting three nines off a single box would be the kind of number that falls apart the first time someone asks how.

The latency target covers `/api/` only. Serving the frontend bundle and holding WebSocket connections are different jobs: a WebSocket lives as long as the game, so timing it measures how long someone played rather than how fast the service answered.

The engine target is the one under real pressure. A move costs about 1.6 s of CPU, so a single worker sustains roughly 0.6 moves a second. Past that, the queue absorbs the overflow and the cost lands on move latency instead of errors, which is the tradeoff the architecture was built to make. It also means the engine target breaks well before the API one does, and it breaks first for whoever is unlucky enough to be playing at the time.

`/status` reports current numbers against these.

## Two deployments, on purpose

The repo ships two stacks, because the architecture worth designing and the architecture worth paying for every month are not the same thing.

`deploy/terraform` is the reference design: an autoscaling api fleet behind an ALB, workers scaled on queue depth, ElastiCache, RDS. It is what the system should look like under load, it has been deployed and exercised end to end, and it costs roughly $60 a month to leave running. So it goes up on demand and comes down after.

`deploy/demo` is the version cheap enough to leave running: one t4g.micro with the same two container images against a real SQS queue, Postgres and Redis alongside, Caddy in front, about $10 a month. Same code, same queue semantics, a tenth of the bill. A demo link does not need six tasks and a load balancer, and pretending otherwise would be an expensive way to make a point. Both stacks are torn down between demos, so there is no public URL to click right now; `make demo-deploy` prints one in a couple of minutes.

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
internal/game            chess domain: move lists, legality, outcomes
internal/engine          board encoding, ONNX inference, fallback searcher
internal/store           Redis (live state, CAS, pub/sub) and Postgres (archive)
internal/queue           SQS client, ElasticMQ-compatible for local dev
internal/httpapi         REST + WebSocket handlers, embedded frontend
internal/obs             JSON logging, Prometheus metrics, HTTP middleware
web/                     React frontend, built into the api binary
deploy/terraform         the AWS stack
loadtest/                k6 scenario
```
