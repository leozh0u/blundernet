// Package review turns a game into an assessment of every move.
//
// The whole thing rests on one idea: a move is judged by how much it changed
// your chances of winning, not by how many centipawns it cost.
//
// Centipawns are the natural output of an engine and the wrong unit for
// teaching. Going from +9 to +6 is three hundred centipawns and means nothing,
// because both positions are completely winning. Going from +0.2 to -0.8 is a
// hundred centipawns and is the whole game. Converting to a win percentage
// first makes those two comparable, and it is what a player actually feels.
//
// The conversion and the accuracy formula are Lichess's, used because they are
// published, derived from real games rather than invented, and because a
// number a player can compare against the site they already use is worth more
// than a number only this site produces.
package review

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/notnil/chess"

	"github.com/leozh0u/blundernet/internal/engine"
)

// Analyser is the part of an engine this package needs, which is one question
// asked of one position. Narrow on purpose: it makes the tests able to answer
// with fixed evaluations instead of running a real search, so what is being
// tested is the arithmetic and the judgement rather than Stockfish.
type Analyser interface {
	Analyse(ctx context.Context, fen string) (engine.Analysis, error)
}

// Judgement is what a move gets called.
type Judgement string

const (
	// Brilliant and Great sit above Best, and both are claims about the
	// position rather than about the size of a mistake, which is why they need
	// more than the win percentage lost.
	//
	// Brilliant is a sacrifice that works: you gave up material, the engine
	// agrees with the move anyway, and you are not losing afterwards.
	//
	// Great is the only move that held. Every other move available drops a
	// meaningful share of your chances, so finding this one was the game.
	Brilliant  Judgement = "brilliant"
	Great      Judgement = "great"
	Best       Judgement = "best"
	Excellent  Judgement = "excellent"
	Good       Judgement = "good"
	Inaccuracy Judgement = "inaccuracy"
	Mistake    Judgement = "mistake"
	Blunder    Judgement = "blunder"
)

// The thresholds, in win percentage lost. Lichess publishes its cutoffs in
// centipawns; these are the same idea moved into the unit that survives being
// winning or lost, and the numbers are chosen so a move that hands over a fifth
// of your winning chances is a blunder.
const (
	excellentLoss  = 2.0
	goodLoss       = 5.0
	inaccuracyLoss = 10.0
	mistakeLoss    = 20.0
)

// What Brilliant and Great require, beyond being the engine's move.
const (
	// Material given up, in pawns, counted after the opponent has played their
	// best reply, so an ordinary trade nets to nothing and only a real
	// sacrifice clears the bar. Three is a minor piece: the exchange alone is
	// not brilliant, it is just an exchange sacrifice, and calling those
	// brilliant is how the label stops meaning anything.
	sacrificeMin = 3.0
	// Sacrificing into a lost position is not brilliant, it is losing. Fifty
	// is level.
	brilliantFloor = 45.0
	// How much worse the second best move has to be before the move played
	// counts as the only one. Twenty points of win percentage is the mistake
	// threshold, so the claim is exact: every other move on the board would
	// have been at least a mistake. Ten was tried first and handed out six of
	// these in a single game, which is how a label stops meaning anything.
	onlyMoveGap = 20.0
	// Great is about holding a game together, so it is only offered while
	// there is a game to hold. In a position already winning by this much the
	// second best move is worse than the best one and both still win, and
	// calling that the only move would be a lie about the stakes.
	liveMax = 90.0
)

// Move is one move, judged.
type Move struct {
	Ply       int       `json:"ply"`
	SAN       string    `json:"san"`
	UCI       string    `json:"uci"`
	White     bool      `json:"white"`
	Judgement Judgement `json:"judgement"`
	// WinBefore and WinAfter are the mover's chances, as percentages.
	WinBefore float64 `json:"win_before"`
	WinAfter  float64 `json:"win_after"`
	// Better is what the engine would have played, empty when the move played
	// was already the engine's choice.
	Better    string  `json:"better,omitempty"`
	BetterSAN string  `json:"better_san,omitempty"`
	Accuracy  float64 `json:"accuracy"`
	// FEN after the move, so the front end can jump to it.
	FEN string `json:"fen"`
}

// Result is the finished review.
type Result struct {
	Moves []Move `json:"moves"`
	// Accuracy for each side, on the same 0 to 100 scale as the per move one.
	WhiteAccuracy float64 `json:"white_accuracy"`
	BlackAccuracy float64 `json:"black_accuracy"`
	// Counts per judgement per side, which is the summary line a player reads
	// before anything else.
	White Counts `json:"white"`
	Black Counts `json:"black"`
	// Worst is the handful of moves worth talking about, biggest drop first.
	Worst []Move `json:"worst"`
}

type Counts struct {
	Brilliant  int `json:"brilliant"`
	Great      int `json:"great"`
	Best       int `json:"best"`
	Excellent  int `json:"excellent"`
	Good       int `json:"good"`
	Inaccuracy int `json:"inaccuracy"`
	Mistake    int `json:"mistake"`
	Blunder    int `json:"blunder"`
}

func (c *Counts) add(j Judgement) {
	switch j {
	case Brilliant:
		c.Brilliant++
	case Great:
		c.Great++
	case Best:
		c.Best++
	case Excellent:
		c.Excellent++
	case Good:
		c.Good++
	case Inaccuracy:
		c.Inaccuracy++
	case Mistake:
		c.Mistake++
	case Blunder:
		c.Blunder++
	}
}

const worstToKeep = 4

// WinPercent converts a score into the chance of winning, from the point of
// view of whoever the score belongs to.
//
// The constant is Lichess's, fitted against real games. A forced mate is not
// on that curve, so it is treated as the certainty it is.
func WinPercent(a engine.Analysis) float64 {
	if a.Mate != 0 {
		if a.Mate > 0 {
			return 100
		}
		return 0
	}
	return 50 + 50*(2/(1+math.Exp(-0.00368208*float64(a.CP)))-1)
}

// accuracyOf is Lichess's per move accuracy from the win percentage given up.
// Clamped because the formula can exceed 100 for a move that improves your
// position, and "112% accurate" is not a thing.
func accuracyOf(lost float64) float64 {
	if lost < 0 {
		lost = 0
	}
	v := 103.1668*math.Exp(-0.04354*lost) - 3.1669
	return math.Min(100, math.Max(0, v))
}

func judge(lost float64, played, best string) Judgement {
	if played == best {
		return Best
	}
	switch {
	case lost < excellentLoss:
		return Excellent
	case lost < goodLoss:
		return Good
	case lost < inaccuracyLoss:
		return Inaccuracy
	case lost < mistakeLoss:
		return Mistake
	default:
		return Blunder
	}
}

// pieceValue is the ordinary count, used only to decide whether material was
// given up. The king is left out because it is never captured, so it cannot be
// part of a sacrifice.
var pieceValue = map[chess.PieceType]float64{
	chess.Pawn: 1, chess.Knight: 3, chess.Bishop: 3, chess.Rook: 5, chess.Queen: 9,
}

// material counts what one colour has on the board.
func material(pos *chess.Position, c chess.Color) float64 {
	var total float64
	board := pos.Board()
	for sq := chess.A1; sq <= chess.H8; sq++ {
		p := board.Piece(sq)
		if p == chess.NoPiece || p.Color() != c {
			continue
		}
		total += pieceValue[p.Type()]
	}
	return total
}

// exchangeOn is a static exchange evaluation: if the side to move starts
// taking on this square and both sides keep taking with their cheapest
// available piece, how much material does the side to move come out ahead?
//
// Written rather than borrowed because it answers the one question the review
// needs and nothing else: was material actually on offer. Counting attackers
// is not enough, because a piece attacked twice and defended twice is not
// hanging, and only playing the exchange out tells you which.
//
// The max with zero at each step is the part that makes it correct: either
// side can decline to continue the exchange, so nobody is forced into a
// losing recapture just because a capture was available.
func exchangeOn(pos *chess.Position, sq chess.Square, depth int) float64 {
	// Bounded because each level plays a real move, and a pathological
	// position should not turn one judged move into an unbounded search.
	if depth <= 0 {
		return 0
	}
	target := pos.Board().Piece(sq)
	if target == chess.NoPiece {
		return 0
	}
	var best *chess.Move
	cheapest := math.Inf(1)
	for _, mv := range pos.ValidMoves() {
		if mv.S2() != sq {
			continue
		}
		v := pieceValue[pos.Board().Piece(mv.S1()).Type()]
		if pos.Board().Piece(mv.S1()).Type() == chess.King {
			// The king can only take last, and only when nothing defends.
			v = 100
		}
		if v < cheapest {
			cheapest, best = v, mv
		}
	}
	if best == nil {
		return 0
	}
	gain := pieceValue[target.Type()] - exchangeOn(pos.Update(best), sq, depth-1)
	return math.Max(0, gain)
}

// onOffer is the most material the given colour could win by capturing, if it
// were their turn.
//
// The null move is the only way to ask this: the question is what the opponent
// can take in reply to a move, which means looking at the position as though
// it were already theirs. A position where the side being asked about has the
// other king in check is not a legal null move and is skipped rather than
// guessed at.
func onOffer(pos *chess.Position, side chess.Color) float64 {
	if pos.Turn() != side {
		flipped, err := chess.FEN(nullMoveFEN(pos, side))
		if err != nil {
			return 0
		}
		g := chess.NewGame(flipped)
		pos = g.Position()
	}
	var most float64
	board := pos.Board()
	for sq := chess.A1; sq <= chess.H8; sq++ {
		p := board.Piece(sq)
		if p == chess.NoPiece || p.Color() == side {
			continue
		}
		if v := exchangeOn(pos, sq, 8); v > most {
			most = v
		}
	}
	return most
}

// nullMoveFEN rewrites a position with the other side to move. En passant is
// dropped because the capture it records is no longer available to the side
// now moving, and keeping it would generate a move that is not there.
func nullMoveFEN(pos *chess.Position, side chess.Color) string {
	f := strings.Fields(pos.String())
	if len(f) < 6 {
		return pos.String()
	}
	if side == chess.White {
		f[1] = "w"
	} else {
		f[1] = "b"
	}
	f[3] = "-"
	return strings.Join(f, " ")
}

// sacrificed reports how much material the mover left on the table.
//
// A sacrifice is the offer, not the acceptance. The first version of this
// measured material after the opponent's best reply, and on Legal's Mate it
// found nothing: the engine's best reply is to decline the queen, so by that
// measure nothing was ever given. That is backwards. The queen being takeable
// and it not mattering is the whole point of the move.
//
// So this asks what the opponent could win if they took, anywhere on the
// board, and subtracts whatever the move itself captured, which is what stops
// an ordinary recapture from reading as a gift.
func sacrificed(before, after *chess.Position, mover chess.Color) float64 {
	gained := material(before, mover.Other()) - material(after, mover.Other())
	return onOffer(after, mover.Other()) - gained
}

func Game(ctx context.Context, a Analyser, moves []string) (*Result, error) {
	positions := []*chess.Position{chess.NewGame().Position()}
	sans := make([]string, 0, len(moves))
	ucis := make([]string, 0, len(moves))

	for i, uci := range moves {
		pos := positions[len(positions)-1]
		mv := findMove(pos, uci)
		if mv == nil {
			// A game that will not replay is a corpus or import problem. What
			// has been read so far is still worth reviewing.
			if i == 0 {
				return nil, errIllegal{uci}
			}
			break
		}
		sans = append(sans, chess.AlgebraicNotation{}.Encode(pos, mv))
		ucis = append(ucis, uci)
		positions = append(positions, pos.Update(mv))
	}

	evals := make([]engine.Analysis, len(positions))
	for i, pos := range positions {
		e, err := a.Analyse(ctx, pos.String())
		if err != nil {
			return nil, err
		}
		evals[i] = e
	}

	out := &Result{}
	for i := range ucis {
		before := evals[i]
		after := evals[i+1]

		// Both are read from the mover's point of view. The engine always
		// scores for whoever is to move, so the evaluation after the move
		// belongs to the opponent and has to be turned around. Getting this
		// wrong makes every good move look like a blunder, which is the one
		// mistake in this file that would be obvious in the output.
		winBefore := WinPercent(before)
		winAfter := 100 - WinPercent(after)

		// A finished position has no side to move and therefore no evaluation
		// to turn around. Asked about one, the engine returns a score of zero,
		// which read as an opinion says the game is level: delivering
		// checkmate then measures as a fall from certainty to a coin flip, and
		// the mating move is scored as the worst move of the game. The result
		// is not the engine's to judge, it is on the board.
		switch positions[i+1].Status() {
		case chess.Checkmate:
			winAfter = 100 // the side to move is mated, so the mover won
		case chess.Stalemate, chess.FivefoldRepetition, chess.SeventyFiveMoveRule,
			chess.InsufficientMaterial:
			winAfter = 50
		}

		lost := winBefore - winAfter
		j := judge(lost, ucis[i], before.Best)

		// Brilliant and Great are decided here rather than inside judge, which
		// knows the arithmetic but not the board.
		//
		// Brilliant does not require the engine to have picked the same move,
		// only that the move cost nothing. Requiring it to be the engine's top
		// choice was tried and it fails in exactly the case the label is for:
		// on the live box, at the search time a review can afford, Stockfish
		// prefers d4 to Nxe5 in Legal's Mate, so the most famous sacrifice in
		// chess came back as "excellent". A move that gives up a queen and
		// gains eight points of win percentage is brilliant whether or not a
		// shallow search would have found it.
		if j == Best || j == Excellent {
			if sacrificed(positions[i], positions[i+1], positions[i].Turn()) >= sacrificeMin &&
				winAfter >= brilliantFloor {
				j = Brilliant
			}
		}
		// Great stays tied to the engine's own move, because it is a claim
		// about the alternatives and the engine is the only thing here with an
		// opinion about those. The runner up is scored from the same side's
		// point of view as winBefore, so the two subtract directly.
		if j == Best && before.Second != nil && winBefore <= liveMax &&
			winBefore-WinPercent(engine.Analysis{
				CP: before.Second.CP, Mate: before.Second.Mate,
			}) >= onlyMoveGap {
			j = Great
		}

		m := Move{
			Ply:       i + 1,
			SAN:       sans[i],
			UCI:       ucis[i],
			White:     i%2 == 0,
			WinBefore: round(winBefore),
			WinAfter:  round(winAfter),
			Judgement: j,
			Accuracy:  round(accuracyOf(lost)),
			FEN:       positions[i+1].String(),
		}
		if ucis[i] != before.Best && before.Best != "" {
			m.Better = before.Best
			if mv := findMove(positions[i], before.Best); mv != nil {
				m.BetterSAN = chess.AlgebraicNotation{}.Encode(positions[i], mv)
			}
		}
		out.Moves = append(out.Moves, m)

		if m.White {
			out.White.add(m.Judgement)
		} else {
			out.Black.add(m.Judgement)
		}
	}

	out.WhiteAccuracy = sideAccuracy(out.Moves, true)
	out.BlackAccuracy = sideAccuracy(out.Moves, false)
	out.Worst = worst(out.Moves)
	return out, nil
}

// sideAccuracy combines one side's moves into a single number.
//
// Averaging move accuracies is the obvious way and it is wrong: eighty quiet
// moves in a decided game drown one catastrophe, and a player who blundered
// their queen would read 94%. The harmonic mean is dragged down hard by a
// single bad value, which is the behaviour wanted here.
//
// Lichess pairs that with a volatility weighted mean, so that moves played
// while the game was actually swinging count for more than moves played in a
// dead position, and averages the two. The same is done here.
func sideAccuracy(moves []Move, white bool) float64 {
	var acc, wins []float64
	for _, m := range moves {
		if m.White == white {
			acc = append(acc, m.Accuracy)
			wins = append(wins, m.WinBefore)
		}
	}
	if len(acc) == 0 {
		return 0
	}
	return round((harmonic(acc) + weightedByVolatility(acc, wins)) / 2)
}

func harmonic(v []float64) float64 {
	var sum float64
	for _, x := range v {
		// A zero would make the harmonic mean infinite. One percent is close
		// enough to zero for a move that lost the game and keeps the maths
		// finite.
		sum += 1 / math.Max(x, 1)
	}
	return float64(len(v)) / sum
}

// weightedByVolatility weights each move by how much the position was moving
// around it, measured as the spread of win percentages in a window either side.
func weightedByVolatility(acc, wins []float64) float64 {
	if len(acc) == 1 {
		return acc[0]
	}
	// The window scales with the length of the game, bounded so that a short
	// game does not end up with a window of one and a long one with a window
	// covering everything.
	window := len(wins) / 10
	if window < 2 {
		window = 2
	}
	if window > 8 {
		window = 8
	}

	var total, weighted float64
	for i, a := range acc {
		lo := max(0, i-window)
		hi := min(len(wins), i+window+1)
		w := math.Max(stdev(wins[lo:hi]), 0.5) // never zero, or a quiet game weighs nothing
		total += w
		weighted += w * a
	}
	if total == 0 {
		return harmonic(acc)
	}
	return weighted / total
}

func stdev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	var sum float64
	for _, x := range v {
		sum += (x - mean) * (x - mean)
	}
	return math.Sqrt(sum / float64(len(v)))
}

// worst is the few moves worth talking about. Only real errors qualify, so a
// clean game reports nothing rather than naming its least good move.
func worst(moves []Move) []Move {
	var bad []Move
	for _, m := range moves {
		if m.Judgement == Blunder || m.Judgement == Mistake {
			bad = append(bad, m)
		}
	}
	sort.SliceStable(bad, func(i, j int) bool {
		return (bad[i].WinBefore - bad[i].WinAfter) > (bad[j].WinBefore - bad[j].WinAfter)
	})
	if len(bad) > worstToKeep {
		bad = bad[:worstToKeep]
	}
	return bad
}

type errIllegal struct{ uci string }

func (e errIllegal) Error() string { return "move " + e.uci + " does not play" }

func findMove(pos *chess.Position, uci string) *chess.Move {
	for _, m := range pos.ValidMoves() {
		if (chess.UCINotation{}).Encode(pos, m) == uci {
			return m
		}
	}
	return nil
}

func round(v float64) float64 { return math.Round(v*10) / 10 }
