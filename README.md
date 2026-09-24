# BlunderNet

[![ci](https://github.com/leozh0u/blundernet/actions/workflows/ci.yml/badge.svg)](https://github.com/leozh0u/blundernet/actions/workflows/ci.yml)

A chess site with two halves: **3,253,092 puzzles you can actually filter**, and games against [the BlunderNet engine](https://github.com/leozh0u/blundernet-engine), a neural network I trained from scratch.

Live at **https://blundernet.com**.

The puzzles are the part I think is worth something. Chess.com and Lichess both serve them the same way: here is one at roughly your rating, next. Neither lets you say "give me twenty three-move knight forks in an endgame at 1600." The corpus is Lichess's CC0 set, which cost them over a hundred years of CPU time to generate and which they gave away, so generating puzzles is not the interesting problem. Making them searchable is. The LeetCode comparison is the honest one: LeetCode did not invent the problems, it made them filterable, rated, and trackable.

The engine repo answers "can I train a model?" This repo answers a different question: can I serve one? Stateless Go API instances with game state in Redis, engine inference decoupled onto queue-fed workers, a puzzle sampler that draws uniformly from three million rows without sorting them, and the whole thing defined in Terraform.

**Stack:** Go, React, PostgreSQL, Redis, SQS, ONNX Runtime, Docker, Terraform, AWS (ALB, ECS Fargate, ElastiCache, RDS)

## Documentation

- [Features](docs/features.md)
- [Architecture and design decisions](docs/architecture.md)
- [Operations, deployment and load tests](docs/operations.md)
- [Puzzle design](docs/puzzles.md)
- [Project progress](docs/progress.md)

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
web/src/features/        account, classrooms, puzzles and analysis
web/src/components/      shared UI components
web/src/lib/             board hooks and sound
web/src/pages/           standalone pages
docs/                    design notes, features and operations
deploy/terraform         the AWS stack
deploy/demo              the one-box stack that serves the site
loadtest/                k6 scenarios: game flow and puzzle search
```

The puzzle data is Lichess's, used under CC0.
