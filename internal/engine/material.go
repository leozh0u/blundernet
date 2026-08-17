package engine

import (
	"fmt"
	"math"

	"github.com/notnil/chess"
)

// Material is the fallback engine: two-ply negamax over material count.
// It exists so the platform runs end to end without the model artifact;
// it is not meant to play well.
type Material struct{}

func NewMaterial() *Material { return &Material{} }

func (m *Material) Name() string { return "material-fallback" }

var pieceValue = map[chess.PieceType]int{
	chess.Pawn:   100,
	chess.Knight: 320,
	chess.Bishop: 330,
	chess.Rook:   500,
	chess.Queen:  900,
}

func (m *Material) BestMove(fen string) (string, error) {
	pos, err := ParseFEN(fen)
	if err != nil {
		return "", err
	}
	moves := pos.ValidMoves()
	if len(moves) == 0 {
		return "", fmt.Errorf("no legal moves in %q", fen)
	}
	best, bestScore := moves[0], math.MinInt
	for _, mv := range moves {
		score := -negamax(pos.Update(mv), 1)
		if score > bestScore {
			best, bestScore = mv, score
		}
	}
	return IndexUCI(MoveIndex(best), pos), nil
}

func negamax(pos *chess.Position, depth int) int {
	switch pos.Status() {
	case chess.Checkmate:
		return -100000
	case chess.Stalemate:
		return 0
	}
	if depth == 0 {
		return evaluate(pos)
	}
	best := math.MinInt
	for _, mv := range pos.ValidMoves() {
		if s := -negamax(pos.Update(mv), depth-1); s > best {
			best = s
		}
	}
	if best == math.MinInt { // no moves and not caught above: draw-ish
		return 0
	}
	return best
}

// evaluate scores material from the side-to-move's perspective.
func evaluate(pos *chess.Position) int {
	score := 0
	board := pos.Board()
	for sq := chess.Square(0); sq < 64; sq++ {
		p := board.Piece(sq)
		if p == chess.NoPiece {
			continue
		}
		v := pieceValue[p.Type()]
		if p.Color() == pos.Turn() {
			score += v
		} else {
			score -= v
		}
	}
	return score
}

// Score lets the fallback answer a review too, so the feature works on a
// clone with no model rather than silently doing nothing. Material only, put
// on the same [-1, 1] scale the network uses by treating a queen's worth of
// material as a won position.
func (m *Material) Score(fen string) (float64, error) {
	pos, err := ParseFEN(fen)
	if err != nil {
		return 0, err
	}
	switch pos.Status() {
	case chess.Checkmate:
		return -1, nil
	case chess.Stalemate:
		return 0, nil
	}
	return math.Max(-1, math.Min(1, float64(evaluate(pos))/900)), nil
}
