package worker

import (
	"testing"
	"time"
)

// A review has to finish inside the queue's visibility timeout, or a long game
// is handed to a second worker while the first is still analysing it.
func TestAReviewStaysInsideTheVisibilityTimeout(t *testing.T) {
	const visibility = 30 * time.Second
	// 240 plies is the import cap, so 241 positions is the worst case.
	for _, positions := range []int{1, 10, 61, 121, 241} {
		total := time.Duration(positions) * paced(positions)
		if total > visibility {
			t.Errorf("%d positions take %s, past the %s visibility timeout",
				positions, total, visibility)
		}
	}
}

func TestShortGamesStillGetFullThinkingTime(t *testing.T) {
	if got := paced(40); got != maxMoveTime {
		t.Errorf("a 40 position game gets %s per move, want the full %s", got, maxMoveTime)
	}
}

func TestAVeryLongGameIsNotAnalysedAtZero(t *testing.T) {
	if got := paced(10000); got < minMoveTime {
		t.Errorf("an absurd game got %s per move, below the %s floor", got, minMoveTime)
	}
}
