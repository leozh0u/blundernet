package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

const scholars = `[Event "Rated blitz"]
[White "a"]

1. e4 { [%clk 0:03:00] } 1... e5 2. Bc4 Nc6 3. Qh5 Nf6?? 4. Qxf7# 1-0`

// Reviewing a pasted game deliberately does not need an account, because it is
// the most useful thing the site does for somebody who just arrived.
func TestAnyoneCanPasteAGame(t *testing.T) {
	s := newPuzzleServer(t)
	rec := do(t, s, "POST", "/api/review/pgn", `{"pgn":`+quote(scholars)+`}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("pasting signed out: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		ID    string `json:"id"`
		Moves int    `json:"moves"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Moves != 7 {
		t.Errorf("read %d moves, want 7", got.Moves)
	}

	// The review is not ready yet, and saying so is a 202 rather than an
	// error: the client polls this.
	rec = do(t, s, "GET", "/api/review/"+got.ID, "")
	if rec.Code != http.StatusAccepted {
		t.Errorf("before the worker runs: %d %s", rec.Code, rec.Body)
	}
}

func TestRubbishIsRefusedWithAReason(t *testing.T) {
	s := newPuzzleServer(t)
	for _, c := range []struct{ pgn, want string }{
		{"", "no moves"},
		{"hello there", "no moves"},
		{"[Event \"x\"]", "no moves"},
	} {
		rec := do(t, s, "POST", "/api/review/pgn", `{"pgn":`+quote(c.pgn)+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("pasting %q: %d %s", c.pgn, rec.Code, rec.Body)
		}
	}
}

func TestAReviewIdHasToBeOne(t *testing.T) {
	s := newPuzzleServer(t)
	for _, id := range []string{"not-a-uuid", "1", "%2e%2e"} {
		if rec := do(t, s, "GET", "/api/review/"+id, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/review/%s: %d", id, rec.Code)
		}
	}
	// A literal ".." never reaches the handler at all: the mux cleans the path
	// and redirects first, which is worth knowing rather than worth testing
	// for here, since it is the standard library's behaviour and not this
	// code's.
	if rec := do(t, s, "GET", "/api/review/../etc", ""); rec.Code != http.StatusTemporaryRedirect {
		t.Errorf("a traversing path gave %d, want the mux to clean and redirect it", rec.Code)
	}
	// A well formed id that does not exist is also a 404, not a 500.
	rec := do(t, s, "GET", "/api/review/2ec4a3f6-0000-4000-8000-000000000000", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unknown review: %d %s", rec.Code, rec.Body)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
