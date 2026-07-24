# Load test

`game_flow.js` plays real games against the deployed stack. Each iteration
creates a game, subscribes over the same WebSocket a browser uses, posts a
move, waits for the engine's reply on the socket, and resigns. Measuring
the reply on the socket rather than by polling means the number covers the
whole path: api, redis, sqs, worker, mcts, redis, pub/sub, socket.

```
k6 run loadtest/game_flow.js                                   # local
k6 run -e BASE=http://<alb-dns> -e SCENARIO=steady -e RATE=1.5 loadtest/game_flow.js
k6 run -e BASE=http://<alb-dns> -e SCENARIO=ramp   -e PEAK=2   loadtest/game_flow.js
```

`steady` holds a constant arrival rate, for latency at a load the fleet can
carry. `ramp` climbs past saturation, to find the ceiling and to give the
worker autoscaling policy something to react to.

`results-autoscaling.csv` is the recording from the ramp run described in
the top-level README: queue depth and worker count sampled every twenty
seconds. The backlog builds from t=143s, peaks at 113 messages, and drains
to zero about ninety seconds after the desired worker count goes from one
to four at t=467s.
