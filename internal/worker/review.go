package worker

import (
	"context"
	"fmt"
	"sort"

	"github.com/notnil/chess"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/store"
)

// Reviewing a finished game is one evaluation per position and no search. The
// question is not "what should you have played", which needs a search per
// move and a claim the engine is not strong enough to make. It is "what did
// the network think of the position before your move, and after it", which is
// a number this model already produces and can be checked against the board.
//
// The two biggest drops are the ones worth showing. Every game has a worst
// move; not every game has three interesting ones.

const worstToKeep = 3

// review scores every position in a finished game and stores the result.
func (w *Worker) review(ctx context.Context, g *game.Game) error {
	scorer, ok := w.Engine.(engine.Scorer)
	if !ok {
		return fmt.Errorf("engine %s cannot score positions", w.Engine.Name())
	}
	if w.Archive == nil {
		return fmt.Errorf("no archive to store a review in")
	}

	out, err := scoreGame(scorer, g)
	if err != nil {
		return err
	}
	return w.Archive.SaveReview(ctx, g.ID, out)
}

func scoreGame(scorer engine.Scorer, g *game.Game) (store.Review, error) {
	player := chess.White
	if g.PlayerColor == "black" {
		player = chess.Black
	}

	// Replay first, keeping every position, because a move is judged against
	// where it left you rather than against the instant after it was played.
	positions := []*chess.Position{chess.NewGame().Position()}
	sans := make([]string, 0, len(g.Moves))
	for i, uci := range g.Moves {
		pos := positions[len(positions)-1]
		mv := findMove(pos, uci)
		if mv == nil {
			return store.Review{}, fmt.Errorf("move %d (%s) does not play", i, uci)
		}
		sans = append(sans, chess.AlgebraicNotation{}.Encode(pos, mv))
		positions = append(positions, pos.Update(mv))
	}

	var out store.Review
	for i, uci := range g.Moves {
		if positions[i].Turn() != player {
			continue
		}
		// After the opponent has answered, which is when a blunder shows up.
		// Comparing the instant after your own move instead would call
		// hanging a queen a gain, because the capture that punishes it has
		// not happened yet and a value head with no search cannot see it.
		end := i + 2
		if end >= len(positions) {
			end = len(positions) - 1
		}
		before, err := scoreFor(scorer, positions[i], player)
		if err != nil {
			return store.Review{}, err
		}
		after, err := scoreFor(scorer, positions[end], player)
		if err != nil {
			return store.Review{}, err
		}
		out.Moves = append(out.Moves, store.ReviewMove{
			Ply:    i + 1,
			UCI:    uci,
			SAN:    sans[i],
			Before: round(before),
			After:  round(after),
			Loss:   round(before - after),
			// Material is counted alongside the network's opinion because the
			// network is a 1000 rated model and its opinion is worth about
			// that much. It rates the position after Qxf7 Kxf7 as good for
			// white. Pieces on the board are not a matter of opinion, so a
			// queen going for nothing is caught here whatever the value head
			// believes.
			Material: material(positions[end], player) - material(positions[i], player),
			FEN:      positions[i+1].String(),
		})
	}

	// Worst first, and only the few worth talking about. A move counts as bad
	// if either signal says so: the network's view fell, or material went and
	// did not come back.
	worst := append([]store.ReviewMove(nil), out.Moves...)
	sort.Slice(worst, func(i, j int) bool { return badness(worst[i]) > badness(worst[j]) })
	for _, m := range worst {
		if len(out.Worst) >= worstToKeep || badness(m) < 0.5 {
			break
		}
		out.Worst = append(out.Worst, m)
	}
	return out, nil
}

// badness puts the two signals on one scale so they can be ranked together.
// A pawn of material and a 0.33 swing in the value head are treated as worth
// the same, which is a judgement call and the only one in here.
func badness(m store.ReviewMove) float64 {
	lost := -m.Material
	if v := m.Loss * 3; v > lost {
		lost = v
	}
	return lost
}

var pieceValue = map[chess.PieceType]float64{
	chess.Pawn: 1, chess.Knight: 3, chess.Bishop: 3, chess.Rook: 5, chess.Queen: 9,
}

// material is the player's material lead in pawns.
func material(pos *chess.Position, player chess.Color) float64 {
	total := 0.0
	for _, piece := range pos.Board().SquareMap() {
		v := pieceValue[piece.Type()]
		if piece.Color() == player {
			total += v
		} else {
			total -= v
		}
	}
	return total
}

func scoreFor(scorer engine.Scorer, pos *chess.Position, player chess.Color) (float64, error) {
	v, err := scorer.Score(pos.String())
	if err != nil {
		return 0, err
	}
	if pos.Turn() != player {
		return -v, nil
	}
	return v, nil
}

func findMove(pos *chess.Position, uci string) *chess.Move {
	for _, m := range pos.ValidMoves() {
		if (chess.UCINotation{}).Encode(pos, m) == uci {
			return m
		}
	}
	return nil
}

// round keeps the wire small and says what the number is worth: a value head
// is not precise to four decimal places and printing it that way implies it
// is.
func round(v float64) float64 {
	return float64(int(v*100+sign(v)*0.5)) / 100
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
