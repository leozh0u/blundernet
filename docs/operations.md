# Operations

## Load tests

Two, because the site has two halves and they break for different reasons.

`loadtest/game_flow.js` drives real games: create, subscribe over the same WebSocket a browser uses, play a move, wait for the engine's reply, resign. Move latency is measured from the move request to the reply arriving on the socket, so it covers the whole path rather than a polling interval. `steady` holds a constant arrival rate; `ramp` climbs past saturation to find the ceiling and give the worker autoscaling policy something to react to.

```
k6 run -e BASE=http://<alb-dns> -e SCENARIO=steady -e RATE=1.5 loadtest/game_flow.js
k6 run -e BASE=http://<alb-dns> -e SCENARIO=ramp   -e PEAK=2   loadtest/game_flow.js
```

`loadtest/puzzles.js` measures the search. `MIX` picks which population of filters to send, and the two answer different questions. `real` is what the site is asked for: the filter panel opens empty, so most fetches carry no filter at all. `stress` sends a theme on nearly every request, which is the expensive path described in [the architecture notes](architecture.md). Reporting only the stress number understates the site and reporting only the real number hides the cliff, so run both.

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

The bottleneck for puzzles is the disk on the single box, described in [the architecture notes](architecture.md).

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

