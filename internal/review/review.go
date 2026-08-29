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
	Best       int `json:"best"`
	Excellent  int `json:"excellent"`
	Good       int `json:"good"`
	Inaccuracy int `json:"inaccuracy"`
	Mistake    int `json:"mistake"`
	Blunder    int `json:"blunder"`
}

func (c *Counts) add(j Judgement) {
	switch j {
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

// Game reviews a whole game from its UCI moves.
//
// One analysis per position, not two. The engine's opinion of the position
// before your move gives both what you could have had and what the best move
// was; its opinion of the position after gives what you actually got. Every
// position is therefore asked about exactly once, which halves the work
// compared to scoring each move independently.
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
		m := Move{
			Ply:       i + 1,
			SAN:       sans[i],
			UCI:       ucis[i],
			White:     i%2 == 0,
			WinBefore: round(winBefore),
			WinAfter:  round(winAfter),
			Judgement: judge(lost, ucis[i], before.Best),
			Accuracy:  round(accuracyOf(lost)),
			FEN:       positions[i+1].String(),
		}
		if m.Judgement != Best && before.Best != "" {
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
