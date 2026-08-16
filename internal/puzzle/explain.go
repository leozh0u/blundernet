package puzzle

import (
	"fmt"
	"strings"

	"github.com/notnil/chess"
)

// Explanations are derived from the tags and the position the solution
// reaches, never written by a model. A model asked to explain chess is a model
// being confidently wrong next to a solution that is provably right, which is
// the worst failure available here. Everything below is a fact about the
// board: what the moved piece attacks, what material changed hands, whether it
// was mate. When the board has nothing to say, the honest answer is the theme
// name and nothing more.
type Explanation struct {
	Headline string   `json:"headline"`
	Points   []string `json:"points,omitempty"`
	Themes   []string `json:"themes,omitempty"`
}

// Centipawns are overkill here. These are the values everyone learns, and they
// are only used to say "wins a rook" rather than to evaluate anything.
var pieceValue = map[chess.PieceType]int{
	chess.Pawn: 1, chess.Knight: 3, chess.Bishop: 3,
	chess.Rook: 5, chess.Queen: 9, chess.King: 0,
}

var pieceName = map[chess.PieceType]string{
	chess.Pawn: "pawn", chess.Knight: "knight", chess.Bishop: "bishop",
	chess.Rook: "rook", chess.Queen: "queen", chess.King: "king",
}

// Explain replays the solution and reports what it did.
//
// It returns ok=false when the moves do not play out from the FEN, which for
// imported puzzles means the row is broken. Saying nothing is the only
// acceptable failure: a wrong explanation next to a correct solution is worse
// than none.
func Explain(p Puzzle) (Explanation, bool) {
	pos, moves, ok := replay(p)
	if !ok {
		return Explanation{}, false
	}

	e := Explanation{Themes: displayThemes(p.Themes)}

	// The key move is the solver's first, which is the one being asked for.
	// Index 0 is the opponent's blunder.
	if len(moves) < 2 {
		return Explanation{}, false
	}
	beforeKey := moves[1].before
	key := moves[1].move
	afterKey := moves[1].after
	solver := beforeKey.Turn()

	mate := pos.Status() == chess.Checkmate
	if mate {
		e.Headline = fmt.Sprintf("Checkmate in %d.", p.UserMoves())
	}

	// What the piece attacks from where it landed. This is the fork, the
	// double attack and the discovered hit, all of which are the same fact
	// stated about the board rather than a tag looked up.
	moved := afterKey.Board().Piece(key.S2())
	targets := attacked(afterKey.Board(), key.S2(), moved)
	if len(targets) >= 2 {
		if e.Headline == "" {
			e.Headline = fmt.Sprintf("The %s forks %s.",
				pieceName[moved.Type()], nameList(targets, 2))
		}
		e.Points = append(e.Points, fmt.Sprintf("The %s on %s attacks %s.",
			pieceName[moved.Type()], key.S2(), nameList(targets, len(targets))))
	}

	// Material, counted from the position the solver was handed to the end of
	// the line, so a sacrifice that wins more back reads as a gain. Skipped
	// after mate: nobody cares who was a pawn up at the end of a mate.
	swing := material(pos.Board(), solver) - material(beforeKey.Board(), solver)
	if swing > 0 && !mate {
		gain := describeGain(swing)
		if e.Headline == "" {
			e.Headline = "Wins " + gain + "."
		} else {
			e.Points = append(e.Points, "The line ends "+gain+" up.")
		}
	}

	if e.Headline == "" {
		e.Headline = headlineFromThemes(p.Themes)
	}
	return e, true
}

type played struct {
	before *chess.Position
	move   *chess.Move
	after  *chess.Position
}

// replay walks the move list through a real position, which both produces the
// positions the explanation reads and proves the row is playable.
func replay(p Puzzle) (*chess.Position, []played, bool) {
	fen, err := chess.FEN(p.FEN)
	if err != nil {
		return nil, nil, false
	}
	game := chess.NewGame(fen)
	pos := game.Position()

	var out []played
	for _, uci := range p.Moves {
		mv := findMove(pos, uci)
		if mv == nil {
			return nil, nil, false
		}
		next := pos.Update(mv)
		out = append(out, played{before: pos, move: mv, after: next})
		pos = next
	}
	return pos, out, true
}

// findMove matches a UCI string against the legal moves of the position. It
// matches rather than parses so a move that is not legal here is rejected
// instead of being applied to a board it does not fit.
func findMove(pos *chess.Position, uci string) *chess.Move {
	for _, m := range pos.ValidMoves() {
		if strings.EqualFold(chess.UCINotation{}.Encode(pos, m), uci) {
			return m
		}
	}
	return nil
}

type target struct {
	piece  chess.Piece
	square chess.Square
}

// attacked lists the enemy pieces a piece on sq hits, ignoring pins and
// defenders. That is deliberate: the claim being made is "this piece attacks
// those", which is true whether or not taking is good.
//
// Pawns are left out. A rook that hits the king and a pawn is not forking
// anything, and listing every pawn in range turns a sentence somebody would
// say into a dump of the board.
func attacked(b *chess.Board, sq chess.Square, p chess.Piece) []target {
	var out []target
	for _, to := range reaches(b, sq, p) {
		other := b.Piece(to)
		if other == chess.NoPiece || other.Color() != p.Color().Other() {
			continue
		}
		if other.Type() != chess.King && pieceValue[other.Type()] < pieceValue[chess.Knight] {
			continue
		}
		out = append(out, target{piece: other, square: to})
	}
	// Most valuable first, so "the king and the rook" reads in the order
	// somebody would say it.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && rank(out[j]) > rank(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// rank orders targets for the sentence: the king first, then by value.
func rank(t target) int {
	if t.piece.Type() == chess.King {
		return 100
	}
	return pieceValue[t.piece.Type()]
}

var (
	knightHops = [][2]int{{1, 2}, {2, 1}, {2, -1}, {1, -2}, {-1, -2}, {-2, -1}, {-2, 1}, {-1, 2}}
	kingSteps  = [][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}
	rookRays   = [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	bishopRays = [][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}}
)

// reaches returns every square the piece covers, generated here rather than
// through the library because the library answers "what may the side to move
// play", and after the key move it is the other side's turn.
func reaches(b *chess.Board, sq chess.Square, p chess.Piece) []chess.Square {
	file, rk := int(sq.File()), int(sq.Rank())
	var out []chess.Square

	step := func(offsets [][2]int) {
		for _, o := range offsets {
			if s, ok := square(file+o[0], rk+o[1]); ok {
				out = append(out, s)
			}
		}
	}
	slide := func(rays [][2]int) {
		for _, r := range rays {
			for i := 1; i < 8; i++ {
				s, ok := square(file+r[0]*i, rk+r[1]*i)
				if !ok {
					break
				}
				out = append(out, s)
				if b.Piece(s) != chess.NoPiece {
					break // a ray stops at the first piece, which it attacks
				}
			}
		}
	}

	switch p.Type() {
	case chess.Knight:
		step(knightHops)
	case chess.King:
		step(kingSteps)
	case chess.Rook:
		slide(rookRays)
	case chess.Bishop:
		slide(bishopRays)
	case chess.Queen:
		slide(rookRays)
		slide(bishopRays)
	case chess.Pawn:
		dir := 1
		if p.Color() == chess.Black {
			dir = -1
		}
		// Only the captures. A pawn does not attack the square in front of it.
		step([][2]int{{1, dir}, {-1, dir}})
	}
	return out
}

func square(file, rank int) (chess.Square, bool) {
	if file < 0 || file > 7 || rank < 0 || rank > 7 {
		return chess.NoSquare, false
	}
	return chess.NewSquare(chess.File(file), chess.Rank(rank)), true
}

// material is the side's total, so a difference across the line is what the
// solver won or gave up.
func material(b *chess.Board, side chess.Color) int {
	mine, theirs := 0, 0
	for sq, p := range b.SquareMap() {
		_ = sq
		if p.Color() == side {
			mine += pieceValue[p.Type()]
		} else {
			theirs += pieceValue[p.Type()]
		}
	}
	return mine - theirs
}

func describeGain(swing int) string {
	switch {
	case swing >= 9:
		return "a queen"
	case swing >= 5:
		return "a rook"
	case swing >= 3:
		return "a piece"
	case swing == 2:
		return "two pawns"
	default:
		return "a pawn"
	}
}

func nameList(targets []target, n int) string {
	if n > len(targets) {
		n = len(targets)
	}
	parts := make([]string, 0, n)
	for _, t := range targets[:n] {
		parts = append(parts, fmt.Sprintf("the %s on %s", pieceName[t.piece.Type()], t.square))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// headlineFromThemes is the fallback when the board says nothing quotable. It
// is the theme name in a sentence rather than an invented claim.
func headlineFromThemes(themes []string) string {
	for _, t := range themes {
		if name, ok := themeSentence[t]; ok {
			return name
		}
	}
	return "The only move that keeps the advantage."
}

var themeSentence = map[string]string{
	"backRankMate":      "A back rank mate.",
	"smotheredMate":     "A smothered mate.",
	"pin":               "A pin: the piece cannot move without losing what is behind it.",
	"skewer":            "A skewer: the valuable piece has to move and the one behind it falls.",
	"deflection":        "A deflection: the defender is pulled away from what it was holding.",
	"attraction":        "An attraction: the piece is dragged onto a square where it can be hit.",
	"discoveredAttack":  "A discovered attack: moving one piece opens the line of another.",
	"clearance":         "A clearance: the square or line is vacated for another piece.",
	"interference":      "An interference: the defending line is blocked.",
	"zugzwang":          "Zugzwang: every move makes the position worse.",
	"trappedPiece":      "The piece is trapped and cannot get out.",
	"hangingPiece":      "A piece was left undefended.",
	"capturingDefender": "Take the defender, and what it defended falls.",
	"quietMove":         "A quiet move: no check, no capture, and nothing can be done about it.",
	"defensiveMove":     "The only move that holds.",
	"promotion":         "The pawn goes through.",
	"underPromotion":    "An underpromotion, because a queen would not do the job.",
	"sacrifice":         "A sacrifice: material now for more back.",
	"fork":              "A fork: two things attacked at once.",
}

// displayThemes drops the tags that describe the source rather than the
// tactic. Nobody drilling wants to be told the puzzle is "long".
func displayThemes(themes []string) []string {
	skip := map[string]bool{
		"short": true, "long": true, "veryLong": true, "oneMove": true,
		"master": true, "masterVsMaster": true, "superGM": true,
		"opening": true, "middlegame": true, "endgame": true,
		"crushing": true, "advantage": true, "equality": true,
	}
	var out []string
	for _, t := range themes {
		if !skip[t] {
			out = append(out, t)
		}
	}
	return out
}
