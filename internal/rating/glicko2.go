// Package rating implements Glicko-2, from Mark Glickman's paper
// "Example of the Glicko-2 system" (glicko.net/glicko/glicko2.pdf).
//
// Glicko-2 over Elo because a new player's rating should not be trusted the
// way a hundred-game veteran's is. Elo carries no uncertainty, so it moves a
// beginner and a regular by the same amount for the same result. Glicko-2
// tracks a deviation alongside the rating, moves an uncertain rating further,
// and widens the deviation again while somebody is not playing.
package rating

import "math"

// Ratings are stored on the familiar 1500-centred scale and converted to the
// internal one for the maths, which is what the paper does.
const (
	DefaultRating     = 1500
	DefaultDeviation  = 350
	DefaultVolatility = 0.06

	scale = 173.7178 // conversion between the display and internal scales

	// tau constrains how much volatility can move in one period. The paper
	// suggests 0.3 to 1.2, smaller being more conservative. 0.5 is the usual
	// starting point.
	tau = 0.5

	convergence = 0.000001
)

type Player struct {
	Rating     float64
	Deviation  float64
	Volatility float64
}

func New() Player {
	return Player{Rating: DefaultRating, Deviation: DefaultDeviation, Volatility: DefaultVolatility}
}

// Result is one game against an opponent of known strength. Score is 1 for a
// win, 0.5 for a draw, 0 for a loss.
type Result struct {
	OpponentRating    float64
	OpponentDeviation float64
	Score             float64
}

// Update returns the player's rating after a rating period.
//
// A rating period here is usually one game, because a chess site updates
// immediately and nobody wants to wait for a batch. The paper prefers periods
// holding 10 to 15 games, and per-game updates make the deviation shrink
// faster than the model intends. That is a deliberate trade of statistical
// tidiness for a number that moves when you finish a game.
func Update(p Player, results []Result) Player {
	// No games played: the rating stands, but confidence in it decays.
	if len(results) == 0 {
		return Player{
			Rating:     p.Rating,
			Deviation:  math.Min(math.Sqrt(p.Deviation*p.Deviation+p.Volatility*p.Volatility), DefaultDeviation),
			Volatility: p.Volatility,
		}
	}

	mu := (p.Rating - DefaultRating) / scale
	phi := p.Deviation / scale

	// Step 3: estimated variance of the rating from game outcomes alone.
	var vInv float64
	for _, r := range results {
		muJ := (r.OpponentRating - DefaultRating) / scale
		phiJ := r.OpponentDeviation / scale
		gj := g(phiJ)
		ej := e(mu, muJ, phiJ)
		vInv += gj * gj * ej * (1 - ej)
	}
	v := 1 / vInv

	// Step 4: the estimated improvement, comparing actual to expected score.
	var deltaSum float64
	for _, r := range results {
		muJ := (r.OpponentRating - DefaultRating) / scale
		phiJ := r.OpponentDeviation / scale
		deltaSum += g(phiJ) * (r.Score - e(mu, muJ, phiJ))
	}
	delta := v * deltaSum

	sigma := newVolatility(phi, v, delta, p.Volatility)

	// Step 6 and 7: widen by the new volatility, then contract by what the
	// games taught us.
	phiStar := math.Sqrt(phi*phi + sigma*sigma)
	phiPrime := 1 / math.Sqrt(1/(phiStar*phiStar)+1/v)
	muPrime := mu + phiPrime*phiPrime*deltaSum

	return Player{
		Rating:     muPrime*scale + DefaultRating,
		Deviation:  phiPrime * scale,
		Volatility: sigma,
	}
}

func g(phi float64) float64 {
	return 1 / math.Sqrt(1+3*phi*phi/(math.Pi*math.Pi))
}

func e(mu, muJ, phiJ float64) float64 {
	return 1 / (1 + math.Exp(-g(phiJ)*(mu-muJ)))
}

// newVolatility is step 5, solved with the Illinois variant of regula falsi
// exactly as the paper specifies. It is iterative because the equation has no
// closed form.
func newVolatility(phi, v, delta, sigma float64) float64 {
	a := math.Log(sigma * sigma)
	f := func(x float64) float64 {
		ex := math.Exp(x)
		num := ex * (delta*delta - phi*phi - v - ex)
		den := 2 * (phi*phi + v + ex) * (phi*phi + v + ex)
		return num/den - (x-a)/(tau*tau)
	}

	A := a
	var B float64
	if delta*delta > phi*phi+v {
		B = math.Log(delta*delta - phi*phi - v)
	} else {
		// Walk down until f turns negative, which brackets the root.
		k := 1.0
		for f(a-k*tau) < 0 {
			k++
		}
		B = a - k*tau
	}

	fA, fB := f(A), f(B)
	for math.Abs(B-A) > convergence {
		C := A + (A-B)*fA/(fB-fA)
		fC := f(C)
		if fC*fB <= 0 {
			A, fA = B, fB
		} else {
			// Illinois: halving fA is what stops one endpoint sticking and
			// the iteration crawling.
			fA /= 2
		}
		B, fB = C, fC
	}
	return math.Exp(A / 2)
}
