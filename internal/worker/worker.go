// Package worker consumes move-evaluation jobs and plays the engine's
// reply. Every step tolerates duplicate and stale deliveries: SQS is
// at-least-once, so idempotency lives here, not in the queue.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/httpapi"
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
	log.Printf("worker running with engine %s", w.Engine.Name())
	for ctx.Err() == nil {
		msgs, err := w.Jobs.Receive(ctx, 5)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("receive: %v", err)
			}
			continue
		}
		for _, m := range msgs {
			if err := w.Process(ctx, m.Job); err != nil {
				// Leave the message for redelivery after the
				// visibility timeout.
				log.Printf("process %s ply %d: %v", m.Job.GameID, m.Job.Ply, err)
				continue
			}
			if err := w.Jobs.Ack(ctx, m); err != nil {
				log.Printf("ack %s: %v", m.Job.GameID, err)
			}
		}
	}
}

// Process plays the engine move for one job. Returning nil means the job
// is finished with (including "safely ignored"); an error means retry.
func (w *Worker) Process(ctx context.Context, j queue.Job) error {
	g, err := w.Games.Get(ctx, j.GameID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // game expired; nothing to do
	}
	if err != nil {
		return err
	}
	// Stale or duplicate delivery: the position has moved past this job.
	if g.Ply != j.Ply || g.Status != game.StatusOngoing || g.Turn() != g.EngineColor() {
		return nil
	}

	uci, err := w.Engine.BestMove(g.FEN())
	if err != nil {
		return err
	}
	prevPly := g.Ply
	if err := g.ApplyMove(g.EngineColor(), uci); err != nil {
		return err
	}
	if err := w.Games.Update(ctx, g, prevPly); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil // someone else already advanced the game
		}
		return err
	}
	if raw, err := json.Marshal(httpapi.ToState(g)); err == nil {
		if err := w.Games.Publish(ctx, g.ID, raw); err != nil {
			log.Printf("publish %s: %v", g.ID, err)
		}
	}
	if g.Status == game.StatusFinished && w.Archive != nil {
		if err := w.Archive.SaveFinished(ctx, g); err != nil {
			log.Printf("archive %s: %v", g.ID, err)
		}
	}
	return nil
}
