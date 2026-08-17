package engine

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"

	"github.com/notnil/chess"
)

// MCTS is PUCT Monte-Carlo Tree Search over the policy/value network,
// the same algorithm the engine trains with. Each simulation walks the
// tree picking the move that maximizes
//
//	Q(s,a) + c_puct * P(s,a) * sqrt(N(s)) / (1 + N(s,a))
//
// exploitation plus a prior-weighted exploration bonus that decays as a
// move gets visited. The move with the most visits at the root wins.
// Raw policy is intuition; the search adds calculation.
type MCTS struct {
	eval  Evaluator
	sims  int
	cPuct float64
}

// Evaluator supplies raw policy logits (4096 from/to pairs) and a value
// in [-1, 1] from the side to move's perspective.
type Evaluator interface {
	Evaluate(pos *chess.Position) (policy []float32, value float32, err error)
}

func NewMCTS(eval Evaluator, sims int) *MCTS {
	return &MCTS{eval: eval, sims: sims, cPuct: 1.5}
}

func (m *MCTS) Name() string { return fmt.Sprintf("blundernet-mcts-%d", m.sims) }

// Score is the value head on its own, with no search over it. Used by the
// post-game review, which wants what the network thought of a position rather
// than what it would play there.
func (m *MCTS) Score(fen string) (float64, error) {
	pos, err := ParseFEN(fen)
	if err != nil {
		return 0, err
	}
	if v, done := terminalValue(pos); done {
		return v, nil
	}
	_, value, err := m.eval.Evaluate(pos)
	if err != nil {
		return 0, err
	}
	return float64(value), nil
}

// node mirrors the training implementation: children are stored in
// parallel with the legal moves that lead to them.
type node struct {
	prior    float64
	visits   int
	valueSum float64
	moves    []*chess.Move
	children []*node
}

func (n *node) q() float64 {
	if n.visits == 0 {
		return 0
	}
	return n.valueSum / float64(n.visits)
}

func (n *node) expanded() bool { return len(n.children) > 0 }

// expand fills in children with softmax priors over the legal moves and
// returns the value head's estimate for the position.
func (m *MCTS) expand(n *node, pos *chess.Position) (float64, error) {
	policy, value, err := m.eval.Evaluate(pos)
	if err != nil {
		return 0, err
	}
	n.moves = pos.ValidMoves()
	n.children = make([]*node, len(n.moves))

	// Softmax over the legal moves' logits only, shifted by the max for
	// numerical stability.
	maxLogit := float32(math.Inf(-1))
	for _, mv := range n.moves {
		if l := policy[MoveIndex(mv)]; l > maxLogit {
			maxLogit = l
		}
	}
	var sum float64
	exps := make([]float64, len(n.moves))
	for i, mv := range n.moves {
		exps[i] = math.Exp(float64(policy[MoveIndex(mv)] - maxLogit))
		sum += exps[i]
	}
	for i := range n.children {
		n.children[i] = &node{prior: exps[i] / sum}
	}
	return float64(value), nil
}

// terminalValue reports the game-over value from the side to move's
// perspective: mated (or stalemated with no winner) positions score -1
// and 0 respectively, matching the training convention.
func terminalValue(pos *chess.Position) (float64, bool) {
	switch pos.Status() {
	case chess.Checkmate:
		return -1, true
	case chess.Stalemate:
		return 0, true
	}
	return 0, false
}

// BestMove searches at the strength this engine was built with.
func (m *MCTS) BestMove(fen string) (string, error) {
	return m.search(fen, m.sims, 0)
}

// BestMoveAt searches at a requested level, which is how one worker serves
// bots of different strengths without holding one engine per level.
func (m *MCTS) BestMoveAt(fen string, level int) (string, error) {
	cfg := Settings(level)
	return m.search(fen, cfg.Sims, cfg.Temp)
}

func (m *MCTS) search(fen string, sims int, temp float64) (string, error) {
	root := &node{}
	rootPos, err := ParseFEN(fen)
	if err != nil {
		return "", err
	}
	if len(rootPos.ValidMoves()) == 0 {
		return "", fmt.Errorf("no legal moves in %q", fen)
	}
	// Mate-in-one probe before searching. The policy net can assign a
	// mating move a near-zero prior in positions unlike its training
	// data (sparse endgames especially), and PUCT then starves the move
	// of visits. One ply of lookahead costs |moves| status checks and
	// guarantees an immediate mate is never overlooked.
	for _, mv := range rootPos.ValidMoves() {
		if rootPos.Update(mv).Status() == chess.Checkmate {
			return IndexUCI(MoveIndex(mv), rootPos), nil
		}
	}
	if _, err := m.expand(root, rootPos); err != nil {
		return "", err
	}

	for s := 0; s < sims; s++ {
		n, pos := root, rootPos
		path := []*node{}

		// 1. Select: walk down via PUCT until a leaf.
		for n.expanded() {
			sqrtN := math.Sqrt(float64(n.visits) + 1)
			bestIdx, bestScore := 0, math.Inf(-1)
			for i, child := range n.children {
				u := m.cPuct * child.prior * sqrtN / float64(1+child.visits)
				// child.q is from the child mover's view; negate it.
				if score := -child.q() + u; score > bestScore {
					bestIdx, bestScore = i, score
				}
			}
			pos = pos.Update(n.moves[bestIdx])
			n = n.children[bestIdx]
			path = append(path, n)
		}

		// 2. Expand and evaluate (network, or the game result itself).
		value, done := terminalValue(pos)
		if !done {
			if value, err = m.expand(n, pos); err != nil {
				return "", err
			}
		}

		// 3. Backpropagate, flipping perspective each ply.
		root.visits++
		for i := len(path) - 1; i >= 0; i-- {
			path[i].visits++
			path[i].valueSum += value
			value = -value
		}
	}

	return IndexUCI(MoveIndex(root.moves[pickRoot(root, temp)]), rootPos), nil
}

// pickRoot is the most-visited move at temperature zero, and a sample from
// visits^(1/T) above it.
func pickRoot(root *node, temp float64) int {
	if temp <= 0 {
		best := 0
		for i, child := range root.children {
			if child.visits > root.children[best].visits {
				best = i
			}
		}
		return best
	}

	weights := make([]float64, len(root.children))
	total := 0.0
	for i, child := range root.children {
		// The +1 keeps a move the search never visited in play with a small
		// weight, which is what a weak player looking at few moves does.
		w := math.Pow(float64(child.visits)+1, 1/temp)
		weights[i] = w
		total += w
	}
	r := rand.Float64() * total
	for i, w := range weights {
		if r < w {
			return i
		}
		r -= w
	}
	return len(root.children) - 1
}

// SimsFromEnv reads ENGINE_SIMS, defaulting to 300. Values below 2 mean
// "no search, raw policy".
func SimsFromEnv() int {
	if v := os.Getenv("ENGINE_SIMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 300
}
