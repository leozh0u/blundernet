package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/leozh0u/blundernet/internal/obs"
)

func TestStatusRequiresAReportingWorker(t *testing.T) {
	s, _ := newTestServer(t)
	if rec := do(t, s, http.MethodGet, "/api/status", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no worker: status %d, want 503", rec.Code)
	}
	if err := s.games.ReportEngine(context.Background(), "test-worker", obs.EngineSnapshot("random")); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, s, http.MethodGet, "/api/status", ""); rec.Code != http.StatusOK {
		t.Fatalf("reporting worker: status %d, want 200: %s", rec.Code, rec.Body)
	}
}
