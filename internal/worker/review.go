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

// reviewImport reviews a game somebody pasted in.
//
// Same engine and same arithmetic as a game played here; only where the moves
// come from and where the answer goes are different. Doing it on the queue
// rather than in the request is the point: a sixty move game is several
// seconds of engine time, and an HTTP handler holding a connection open that
// long on a two core box is how one paste slows the site down for everybody.
func (w *Worker) reviewImport(ctx context.Context, id string) error {
	if w.Analyser == nil || w.Imports == nil {
		return fmt.Errorf("imports are not configured")
	}
	moves, err := w.Imports.Moves(ctx, id)
	if err != nil {
		return err
	}
	out, err := review.Game(ctx, w.Analyser, moves)
	if err != nil {
		return err
	}
	return w.Imports.SaveReview(ctx, id, out)
}
