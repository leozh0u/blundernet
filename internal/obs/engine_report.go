package obs

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// EngineReport is one worker's engine timings in a form that survives a trip
// through Redis. Buckets travel rather than percentiles because percentiles
// cannot be averaged: merging two workers' p95 gives a number that is not
// anybody's p95. Cumulative bucket counts add up exactly.
type EngineReport struct {
	ReportedAt time.Time         `json:"reported_at"`
	Engine     string            `json:"engine"`
	Buckets    map[string]uint64 `json:"buckets"`
	Count      uint64            `json:"count"`
	Played     uint64            `json:"played"`
	Deduped    uint64            `json:"deduped"`
}

// EngineSnapshot reads this process's engine histogram out of the registry.
func EngineSnapshot(engineName string) EngineReport {
	r := EngineReport{
		ReportedAt: time.Now().UTC(),
		Engine:     engineName,
		Buckets:    map[string]uint64{},
	}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return r
	}
	for _, f := range families {
		switch f.GetName() {
		case ns + "_engine_move_duration_seconds":
			for _, m := range f.GetMetric() {
				h := m.GetHistogram()
				r.Count += h.GetSampleCount()
				for _, b := range h.GetBucket() {
					r.Buckets[boundKey(b.GetUpperBound())] += b.GetCumulativeCount()
				}
			}
		case ns + "_worker_jobs_total":
			for _, m := range f.GetMetric() {
				n := uint64(m.GetCounter().GetValue())
				switch labelOf(m, "outcome") {
				case JobPlayed:
					r.Played += n
				case JobExpired, JobStale, JobConflict:
					r.Deduped += n
				}
			}
		}
	}
	return r
}

// MergeEngineReports sums the buckets across live workers and reads
// percentiles off the total. Reports are assumed to share bucket edges,
// which holds because every worker runs this same code.
func MergeEngineReports(reports []EngineReport) (q Quantiles, played, deduped uint64) {
	merged := map[float64]uint64{}
	var total uint64
	for _, r := range reports {
		total += r.Count
		played += r.Played
		deduped += r.Deduped
		for k, v := range r.Buckets {
			b, err := strconv.ParseFloat(k, 64)
			if err != nil {
				continue
			}
			merged[b] += v
		}
	}
	if total == 0 {
		return Quantiles{}, played, deduped
	}
	bounds := sortedBounds(merged)
	return Quantiles{
		Count: total,
		P50:   quantileAt(bounds, merged, total, 0.50),
		P95:   quantileAt(bounds, merged, total, 0.95),
		P99:   quantileAt(bounds, merged, total, 0.99),
	}, played, deduped
}

// boundKey formats a bucket edge as a map key. +Inf has to survive the round
// trip, and strconv writes it as "+Inf", which ParseFloat reads back.
func boundKey(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
