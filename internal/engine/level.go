package engine

// Levels are how the bot is made beatable on purpose.
//
// Two knobs, and the second one matters more than it looks. Cutting the search
// makes the bot shallower, but it still plays the best move it can see, which
// reads as a small engine rather than as a weaker player. Sampling the root
// move from the visit counts instead of taking the maximum is what produces
// the occasional inaccuracy that makes a game against a weak opponent feel
// like a game rather than a slow loss.
//
// The temperature is applied to visit counts: a move visited n times is picked
// with weight n^(1/T). T = 0 is the maximum, which is what a rated engine
// plays. Higher T flattens the distribution towards a coin flip between the
// moves the search actually considered, and it never picks a move the search
// never looked at, so the bot is weak rather than random.
type Level struct {
	Sims int
	Temp float64
}

// MaxLevel is the strongest configuration offered. The ceiling is a latency
// decision rather than a strength one: the SLO says an engine reply lands
// inside three seconds at p95, and the search is linear in simulations.
const (
	MinLevel     = 1
	MaxLevel     = 6
	DefaultLevel = 3
)

var levels = map[int]Level{
	1: {Sims: 8, Temp: 1.6},
	2: {Sims: 25, Temp: 1.1},
	3: {Sims: 75, Temp: 0.7},
	4: {Sims: 200, Temp: 0.35},
	5: {Sims: 400, Temp: 0},
	6: {Sims: 800, Temp: 0},
}

// Settings clamps an out of range level rather than failing. A level arrives
// from a request, and answering "level 47 is not a level" helps nobody when
// the honest response is to play the strongest one.
func Settings(level int) Level {
	if level < MinLevel {
		level = MinLevel
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	return levels[level]
}

// Leveled is implemented by engines that can play at a requested strength.
// The material fallback cannot, and does not have to: it exists so the stack
// boots without the model, not so it plays well.
type Leveled interface {
	BestMoveAt(fen string, level int) (string, error)
}
