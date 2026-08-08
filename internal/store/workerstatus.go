package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leozh0u/blundernet/internal/obs"
)

// Engine timings are measured in the worker but published by the api, which
// is a different process, so they travel through Redis.
//
// A hash keyed by instance rather than one key per worker, so the api reads
// every worker in a single call instead of scanning the keyspace. Hash
// fields cannot carry their own TTL, so freshness is decided by the
// timestamp inside each value and stale fields are cleaned up on read.
const (
	workerStatusKey = "worker:status"
	workerStatusTTL = time.Minute
)

// ReportEngine publishes this worker's timings under its instance id.
func (s *Games) ReportEngine(ctx context.Context, instance string, r obs.EngineReport) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.rdb.HSet(ctx, workerStatusKey, instance, raw).Err()
}

// LiveEngineReports returns the reports still inside the freshness window.
// Anything older is deleted, so a worker that dies stops counting towards
// the numbers instead of holding them at whatever it last managed.
func (s *Games) LiveEngineReports(ctx context.Context) ([]obs.EngineReport, error) {
	fields, err := s.rdb.HGetAll(ctx, workerStatusKey).Result()
	if err != nil {
		return nil, err
	}
	var live []obs.EngineReport
	var stale []string
	cutoff := time.Now().UTC().Add(-workerStatusTTL)
	for instance, raw := range fields {
		var r obs.EngineReport
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			stale = append(stale, instance)
			continue
		}
		if r.ReportedAt.Before(cutoff) {
			stale = append(stale, instance)
			continue
		}
		live = append(live, r)
	}
	if len(stale) > 0 {
		// Best effort. A failed cleanup only means trying again next read.
		_ = s.rdb.HDel(ctx, workerStatusKey, stale...).Err()
	}
	return live, nil
}

// PublishEngineReports writes a report on a ticker until the context ends.
func PublishEngineReports(ctx context.Context, games *Games, instance, engineName string, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		report(ctx, games, instance, engineName)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func report(ctx context.Context, games *Games, instance, engineName string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := games.ReportEngine(ctx, instance, obs.EngineSnapshot(engineName)); err != nil {
		slog.Warn("publish engine report", "err", err)
	}
}
