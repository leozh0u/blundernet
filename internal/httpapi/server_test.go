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
	"github.com/leozh0u/blundernet/internal/testdb"
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
	return New(Deps{Games: store.NewGames(rdb), Jobs: q, Redis: rdb, Static: static}), q
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

// newAccountServer is newTestServer with a real database behind it, which
// anything touching identities needs: a friend game seats people by user id.
func newAccountServer(t *testing.T) (*Server, *captureQueue) {
	t.Helper()
	ctx := context.Background()
	archive, err := store.NewArchive(ctx, testdb.URL(t, "httpapi_test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archive.Close)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	q := &captureQueue{}
	return New(Deps{
		Games: store.NewGames(rdb), Archive: archive,
		Users: store.NewUsers(archive.Pool()), Jobs: q, Redis: rdb,
		Static: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}},
	}), q
}

// A friend game seats the second person who opens the link, refuses anyone
// after that, and never asks the bot for a move.
func TestFriendGameSeatsTwoAndOnlyTwo(t *testing.T) {
	s, q := newAccountServer(t)

	rec := do(t, s, "POST", "/api/games", `{"color":"white","mode":"friend"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var made State
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if !made.Friend || !made.Waiting {
		t.Fatalf("got %+v", made)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("a friend game queued engine work: %v", q.jobs)
	}
	white := rec.Result().Cookies()

	// Second person takes the other side.
	joinReq := httptest.NewRequest("POST", "/api/games/"+made.ID+"/join", nil)
	joined := httptest.NewRecorder()
	s.ServeHTTP(joined, joinReq)
	if joined.Code != http.StatusOK {
		t.Fatalf("join: %d %s", joined.Code, joined.Body)
	}
	var seat State
	if err := json.Unmarshal(joined.Body.Bytes(), &seat); err != nil {
		t.Fatal(err)
	}
	if seat.You != "black" {
		t.Errorf("second seat = %q, want black", seat.You)
	}
	black := joined.Result().Cookies()

	move := func(cookies []*http.Cookie, uci string) int {
		req := httptest.NewRequest("POST", "/api/games/"+made.ID+"/moves",
			strings.NewReader(`{"uci":"`+uci+`"}`))
		req.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := move(white, "e2e4"); code != http.StatusOK {
		t.Errorf("white's move: %d", code)
	}
	if code := move(white, "d2d4"); code != http.StatusConflict {
		t.Errorf("white moving twice: %d, want 409", code)
	}
	if code := move(black, "e7e5"); code != http.StatusOK {
		t.Errorf("black's move: %d", code)
	}
	if len(q.jobs) != 0 {
		t.Errorf("a friend game queued engine work: %v", q.jobs)
	}

	// A third person is a spectator: no seat, no moves.
	third := httptest.NewRecorder()
	s.ServeHTTP(third, httptest.NewRequest("POST", "/api/games/"+made.ID+"/join", nil))
	if third.Code != http.StatusConflict {
		t.Errorf("third join: %d, want 409", third.Code)
	}
	if code := move(third.Result().Cookies(), "d2d4"); code != http.StatusForbidden {
		t.Errorf("spectator move: %d, want 403", code)
	}
}

