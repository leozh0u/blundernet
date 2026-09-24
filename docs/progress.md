
## The five hour plan (2026-09-09)

Leo set a hard budget of about five hours to learn BlunderNet end to end, with a
RiceApps interview as the deadline. Ordered by what an interviewer can ask, not
by what is architecturally tidy.

1. **Request path, 60 min.** `cmd/api/main.go`, the route table in
   `internal/httpapi/server.go`, one handler followed all the way down, and the
   compare-and-set in `internal/store/redis.go`. Exit test: narrate a move from
   the drag to the WebSocket push without notes.
2. **Queue and worker, 60 min.** `internal/queue/queue.go`, `internal/worker/worker.go`,
   then break the `Ply` check, predict which test fails, run it. Exit test: the
   at-least-once delivery answer.
3. **Sampler, 60 min.** `internal/store/puzzleselect.go` with migration 0009 open
   beside it. Exit test: 1372ms to 0.9ms with the mechanism, including why cells
   are drawn in proportion.
4. **Authorization, 45 min.** `internal/store/classrooms.go` and the `ColorFor`
   bug in `internal/game/game.go`. Exit test: why the fix belonged in `ColorFor`
   rather than the handler.
5. **Operations, 45 min.** `scripts/ship.sh` and the digest check, the data volume
   in `deploy/demo/main.tf`, `.github/workflows/ci.yml`. Exit test: why a deploy
   pipeline that reports step success rather than target state is lying.
6. **Out loud, 30 min.** The five stories on a timer, no notes.

Interview prep written to `~/Downloads/Me/riceapps-interview.md`: six behavioural
answers in spoken register, the technical deep dive, and five questions to ask
them. Three gaps in it need Leo, marked in the file.
