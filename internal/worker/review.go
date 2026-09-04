package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/review"
)

// budget is how long a whole review may take, and it is set by the queue
// rather than by taste: the visibility timeout is 30 seconds, so a job that
// runs longer than that is handed to a second worker while the first is still
// on it and the game gets analysed twice. Staying well inside it is what makes
// the job effectively once-only rather than merely idempotent.
const budget = 20 * time.Second

// The engine still needs enough time per position to say something useful. A
// game long enough to hit this floor is analysed a little more roughly, which
// is the right trade against being redelivered.
//
// The ceiling used to be 120ms, which left most of the budget unspent: a
// thirteen move game is fourteen positions, so pacing offered it 1.4 seconds
// each and the cap took 120ms. The engine then missed sacrifices it has the
// time to see. Four hundred keeps a normal game inside a few seconds and
// pacing still protects the long ones, since a hundred position game is back
// under the cap on its own.
const (
	maxMoveTime = 400 * time.Millisecond
	minMoveTime = 30 * time.Millisecond
)

// paced divides the budget across the positions about to be analysed.
//
// Without this a 240 ply game at 120ms is 29 seconds, which is inside the
// timeout only by luck and outside it as soon as the box is busy.
func paced(positions int) time.Duration {
	if positions < 1 {
		return maxMoveTime
	}
	each := budget / time.Duration(positions)
	if each > maxMoveTime {
		return maxMoveTime
	}
	if each < minMoveTime {
		return minMoveTime
	}
	return each
}

// pacer is implemented by an engine whose per position budget can be changed.
// Optional, so a review still works against anything that can analyse.
type pacer interface{ SetMoveTime(time.Duration) }

func (w *Worker) pace(moves int) {
	if p, ok := w.Analyser.(pacer); ok {
		p.SetMoveTime(paced(moves + 1)) // one more position than moves
	}
}

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

	w.pace(len(g.Moves))
	ctx, cancel := context.WithTimeout(ctx, budget+10*time.Second)
	defer cancel()

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
	w.pace(len(moves))
	ctx, cancel := context.WithTimeout(ctx, budget+10*time.Second)
	defer cancel()

	out, err := review.Game(ctx, w.Analyser, moves)
	if err != nil {
		return err
	}
	return w.Imports.SaveReview(ctx, id, out)
}
