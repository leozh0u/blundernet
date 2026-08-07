package engine

import (
	"fmt"

	"github.com/notnil/chess"
)

// Board encoding, mirroring the training pipeline exactly:
// 18 planes of 8x8: 12 piece planes (white PNBRQK, then black), side to
// move, 4 castling rights, en-passant file. Policy is 64*64 from/to pairs
// with underpromotions folded into the queen promotion.
const (
	Planes     = 18
	PolicySize = 4096
)

// python-chess orders piece types P,N,B,R,Q,K = 1..6; notnil/chess uses its
// own ordering, so map explicitly instead of doing enum arithmetic.
var planeOf = map[chess.PieceType]int{
	chess.Pawn:   0,
	chess.Knight: 1,
	chess.Bishop: 2,
	chess.Rook:   3,
	chess.Queen:  4,
	chess.King:   5,
}

// Encode converts a position into the flat NCHW tensor the network expects.
func Encode(pos *chess.Position) []float32 {
	x := make([]float32, Planes*64)
	fill := func(plane int) {
		for i := 0; i < 64; i++ {
			x[plane*64+i] = 1
		}
	}
	board := pos.Board()
	for sq := chess.Square(0); sq < 64; sq++ {
		p := board.Piece(sq)
		if p == chess.NoPiece {
			continue
		}
		plane := planeOf[p.Type()]
		if p.Color() == chess.Black {
			plane += 6
		}
		x[plane*64+int(sq)] = 1
	}
	if pos.Turn() == chess.White {
		fill(12)
	}
	cr := pos.CastleRights()
	rights := []bool{
		cr.CanCastle(chess.White, chess.KingSide),
		cr.CanCastle(chess.White, chess.QueenSide),
		cr.CanCastle(chess.Black, chess.KingSide),
		cr.CanCastle(chess.Black, chess.QueenSide),
	}
	for i, ok := range rights {
		if ok {
			fill(13 + i)
		}
	}
	if ep := pos.EnPassantSquare(); ep != chess.NoSquare {
		file := int(ep) % 8
		for rank := 0; rank < 8; rank++ {
			x[17*64+rank*8+file] = 1
		}
	}
	return x
}

// MoveIndex maps a move to its policy-head index.
func MoveIndex(m *chess.Move) int {
	return int(m.S1())*64 + int(m.S2())
}

// IndexUCI converts a policy index back to a UCI string, restoring the
// queen promotion when a pawn lands on the last rank.
func IndexUCI(idx int, pos *chess.Position) string {
	from := chess.Square(idx / 64)
	to := chess.Square(idx % 64)
	uci := from.String() + to.String()
	p := pos.Board().Piece(from)
	toRank := int(to) / 8
	if p != chess.NoPiece && p.Type() == chess.Pawn && (toRank == 0 || toRank == 7) {
		uci += "q"
	}
	return uci
}

// ParseFEN builds a position from a FEN string.
func ParseFEN(fen string) (*chess.Position, error) {
	opt, err := chess.FEN(fen)
	if err != nil {
		return nil, fmt.Errorf("bad fen %q: %w", fen, err)
	}
	return chess.NewGame(opt).Position(), nil
}
