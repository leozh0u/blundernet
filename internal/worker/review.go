package worker

import (
	"context"
	"fmt"

	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/review"
)

// Reviewing a finished game.
//
// This used to score positions with the playing network's value head, because
// that was the only engine here. It was the wrong tool and the code said so:
// it could report what the network thought of a position, but not what you
// should have played, because a roughly 1000 rated model recommending moves to
// a 1400 player teaches them the wrong things.
//
// So the two jobs are split. The network plays you, because a weak network
// plays like a weak human. Stockfish reviews you, because a review is a claim
// about what is true and that claim should be right.
func (w *Worker) review(ctx context.Context, g *game.Game) error {
	if w.Analyser == nil {
		return fmt.Errorf("no analyser configured")
	}
	if w.Archive == nil {
		return fmt.Errorf("no archive to store a review in")
	}

	out, err := review.Game(ctx, w.Analyser, g.Moves)
	if err != nil {
		return err
	}
	return w.Archive.SaveReview(ctx, g.ID, out)
}
