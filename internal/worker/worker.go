// Package worker consumes move-evaluation jobs and plays the engine's
// reply. Every step tolerates duplicate and stale deliveries: SQS is
// at-least-once, so idempotency lives here, not in the queue.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/httpapi"
	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
)

type Worker struct {
	Games   *store.Games
	Archive *store.Archive
	Jobs    *queue.Client
	Engine  engine.Engine
}

// Run polls until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
	slog.Info("worker running", "engine", w.Engine.Name())
	for ctx.Err() == nil {
		msgs, err := w.Jobs.Receive(ctx, 5)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("receive", "err", err)
			}
			continue
		}
		for _, m := range msgs {
			if err := w.Process(ctx, m.Job); err != nil {
				// Leave the message for redelivery after the
				// visibility timeout.
				slog.Error("process", "game", m.Job.GameID, "ply", m.Job.Ply, "err", err)
				continue
			}
			if err := w.Jobs.Ack(ctx, m); err != nil {
				slog.Error("ack", "game", m.Job.GameID, "err", err)
			}
		}
	}
}

// Process plays the engine move for one job. Returning nil means the job
// is finished with (including "safely ignored"); an error means retry.
// hint answers "what should I play here" for the player's own side. It runs
// the strongest search rather than the game's level, because a hint from the
// bot you are beating is worth nothing, and it publishes rather than plays:
// the game state is never touched, so a hint arriving late or twice cannot
// move a piece.
func (w *Worker) hint(ctx context.Context, g *game.Game, j queue.Job) error {
	if g.Ply != j.Ply || g.Status != game.StatusOngoing || g.Turn() != g.PlayerColor {
		obs.JobOutcome(obs.JobStale)
		return nil
	}
	uci, err := w.hintMove(g)
	if err != nil {
		obs.JobOutcome(obs.JobError)
		return err
	}
	raw, err := json.Marshal(map[string]any{
		"type": "hint", "id": g.ID, "ply": g.Ply, "uci": uci,
	})
	if err != nil {
		obs.JobOutcome(obs.JobError)
		return err
	}
	if err := w.Games.Publish(ctx, g.ID, raw); err != nil {
		obs.JobOutcome(obs.JobError)
		return err
	}
	obs.JobOutcome(obs.JobPlayed)
	return nil
}

func (w *Worker) hintMove(g *game.Game) (string, error) {
	if leveled, ok := w.Engine.(engine.Leveled); ok {
		return leveled.BestMoveAt(g.FEN(), engine.MaxLevel)
	}
	return w.Engine.BestMove(g.FEN())
}

// bestMove plays at the game's level when the engine can, and at its built-in
// strength when it cannot. The material fallback has one setting, and the
// stack has to keep working without the model.
func (w *Worker) bestMove(g *game.Game) (string, error) {
	if leveled, ok := w.Engine.(engine.Leveled); ok && g.Level > 0 {
		return leveled.BestMoveAt(g.FEN(), g.Level)
	}
	return w.Engine.BestMove(g.FEN())
}

func (w *Worker) Process(ctx context.Context, j queue.Job) error {
	g, err := w.Games.Get(ctx, j.GameID)
	if errors.Is(err, store.ErrNotFound) {
		obs.JobOutcome(obs.JobExpired)
		return nil // game expired; nothing to do
	}
	if err != nil {
		obs.JobOutcome(obs.JobError)
		return err
	}
	if j.Kind == queue.KindHint {
		return w.hint(ctx, g, j)
	}
	if j.Kind == queue.KindReview {
		if g.Status != game.StatusFinished {
			obs.JobOutcome(obs.JobStale)
			return nil
		}
		if err := w.review(ctx, g); err != nil {
			obs.JobOutcome(obs.JobError)
			return err
		}
		obs.JobOutcome(obs.JobPlayed)
		return nil
	}

	// Stale or duplicate delivery: the position has moved past this job.
	if g.Ply != j.Ply || g.Status != game.StatusOngoing || g.Turn() != g.EngineColor() {
		obs.JobOutcome(obs.JobStale)
		return nil
	}

	start := time.Now()
	uci, err := w.bestMove(g)
	if err != nil {
		obs.JobOutcome(obs.JobError)
		return err
	}
	obs.EngineMove(time.Since(start))

	prevPly := g.Ply
	if err := g.ApplyMove(g.EngineColor(), uci); err != nil {
		obs.JobOutcome(obs.JobError)
		return err
	}
	if err := w.Games.Update(ctx, g, prevPly); err != nil {
		if errors.Is(err, store.ErrConflict) {
			obs.JobOutcome(obs.JobConflict)
			return nil // someone else already advanced the game
		}
		obs.JobOutcome(obs.JobError)
		return err
	}
	obs.JobOutcome(obs.JobPlayed)

	if raw, err := json.Marshal(httpapi.ToState(g)); err == nil {
		if err := w.Games.Publish(ctx, g.ID, raw); err != nil {
			slog.Error("publish", "game", g.ID, "err", err)
		}
	}
	if g.Status == game.StatusFinished && w.Archive != nil {
		obs.GameFinished(g.Result)
		if err := w.Archive.SaveFinished(ctx, g); err != nil {
			slog.Error("archive", "game", g.ID, "err", err)
		}
	}
	return nil
}
