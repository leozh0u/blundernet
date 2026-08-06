package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
)

type captureQueue struct {
	jobs []queue.Job
}

func (c *captureQueue) Enqueue(_ context.Context, j queue.Job) error {
	c.jobs = append(c.jobs, j)
	return nil
}

func newTestServer(t *testing.T) (*Server, *captureQueue) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	q := &captureQueue{}
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}}
	// No archive in unit tests; the failure path is guarded.
	return New(store.NewGames(rdb), nil, q, rdb, static), q
}

func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestCreateAndMoveFlow(t *testing.T) {
	s, q := newTestServer(t)

	rec := do(t, s, "POST", "/api/games", `{"color":"white"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var st State
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("white game should not enqueue on create, got %v", q.jobs)
	}

	rec = do(t, s, "POST", "/api/games/"+st.ID+"/moves", `{"uci":"e2e4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	if len(q.jobs) != 1 || q.jobs[0].Ply != 1 {
		t.Fatalf("expected one engine job at ply 1, got %v", q.jobs)
	}
}

func TestCreateAsBlackEnqueuesOpeningJob(t *testing.T) {
	s, q := newTestServer(t)
	rec := do(t, s, "POST", "/api/games", `{"color":"black"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	if len(q.jobs) != 1 || q.jobs[0].Ply != 0 {
		t.Fatalf("expected engine job at ply 0, got %v", q.jobs)
	}
}

func TestMoveValidation(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, "POST", "/api/games", `{"color":"white"}`)
	var st State
	_ = json.Unmarshal(rec.Body.Bytes(), &st)

	// Illegal move: 400.
	if rec := do(t, s, "POST", "/api/games/"+st.ID+"/moves", `{"uci":"e2e5"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("illegal move: %d", rec.Code)
	}
	// Garbage body: 400.
	if rec := do(t, s, "POST", "/api/games/"+st.ID+"/moves", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: %d", rec.Code)
	}
	// Legal move, then moving again out of turn: 409.
	do(t, s, "POST", "/api/games/"+st.ID+"/moves", `{"uci":"e2e4"}`)
	if rec := do(t, s, "POST", "/api/games/"+st.ID+"/moves", `{"uci":"d2d4"}`); rec.Code != http.StatusConflict {
		t.Fatalf("out of turn: %d", rec.Code)
	}
	// Unknown game: 404.
	if rec := do(t, s, "POST", "/api/games/nope/moves", `{"uci":"e2e4"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing game: %d", rec.Code)
	}
	// Bad color on create: 400.
	if rec := do(t, s, "POST", "/api/games", `{"color":"purple"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad color: %d", rec.Code)
	}
}

func TestResignIsTerminal(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, "POST", "/api/games", `{"color":"white"}`)
	var st State
	_ = json.Unmarshal(rec.Body.Bytes(), &st)

	if rec := do(t, s, "POST", "/api/games/"+st.ID+"/resign", ""); rec.Code != http.StatusOK {
		t.Fatalf("resign: %d", rec.Code)
	}
	// Acting on a finished game: 409.
	if rec := do(t, s, "POST", "/api/games/"+st.ID+"/resign", ""); rec.Code != http.StatusConflict {
		t.Fatalf("double resign: %d", rec.Code)
	}
	if rec := do(t, s, "POST", "/api/games/"+st.ID+"/moves", `{"uci":"e2e4"}`); rec.Code != http.StatusConflict {
		t.Fatalf("move after resign: %d", rec.Code)
	}
}
