package obs

import (
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var startedAt = time.Now()

// Snapshot is the subset of the metrics that the public status page reports.
// Everything here comes from this process, so on a multi-instance fleet the
// counters describe one instance rather than the whole service. The status
// handler labels them that way.
type Snapshot struct {
	Uptime       time.Duration
	Requests     uint64
	ServerErrors uint64
	APILatency   Quantiles
	EngineMove   Quantiles
	WSOpen       float64
	JobOutcomes  map[string]uint64
}

type Quantiles struct {
	Count uint64
	P50   float64
	P95   float64
	P99   float64
}

// Gather reads the default registry. Pulling from the registry rather than
// keeping a second set of counters means the status page and /metrics can
// never disagree.
func Gather() Snapshot {
	s := Snapshot{Uptime: time.Since(startedAt), JobOutcomes: map[string]uint64{}}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return s
	}
	for _, f := range families {
		switch f.GetName() {
		case ns + "_http_requests_total":
			for _, m := range f.GetMetric() {
				n := uint64(m.GetCounter().GetValue())
				s.Requests += n
				if strings.HasPrefix(labelOf(m, "status"), "5") {
					s.ServerErrors += n
				}
			}
		case ns + "_http_request_duration_seconds":
			// Only the JSON API counts towards the latency target. Serving
			// the frontend bundle is a different job with a different shape,
			// and folding it in would flatter the numbers.
			s.APILatency = quantiles(f.GetMetric(), func(m *dto.Metric) bool {
				return strings.Contains(labelOf(m, "route"), "/api/")
			})
		case ns + "_engine_move_duration_seconds":
			s.EngineMove = quantiles(f.GetMetric(), nil)
		case ns + "_ws_connections":
			for _, m := range f.GetMetric() {
				s.WSOpen += m.GetGauge().GetValue()
			}
		case ns + "_worker_jobs_total":
			for _, m := range f.GetMetric() {
				s.JobOutcomes[labelOf(m, "outcome")] += uint64(m.GetCounter().GetValue())
			}
		}
	}
	return s
}

func sortedBounds(buckets map[float64]uint64) []float64 {
	bounds := make([]float64, 0, len(buckets))
	for b := range buckets {
		bounds = append(bounds, b)
	}
	sort.Float64s(bounds)
	return bounds
}

func labelOf(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// quantiles merges the histogram series that keep matches true into one set
// of buckets and reads percentiles off it.
func quantiles(metrics []*dto.Metric, keep func(*dto.Metric) bool) Quantiles {
	merged := map[float64]uint64{}
	var total uint64
	for _, m := range metrics {
		if keep != nil && !keep(m) {
			continue
		}
		h := m.GetHistogram()
		total += h.GetSampleCount()
		for _, b := range h.GetBucket() {
			merged[b.GetUpperBound()] += b.GetCumulativeCount()
		}
	}
	if total == 0 {
		return Quantiles{}
	}
	bounds := sortedBounds(merged)
	return Quantiles{
		Count: total,
		P50:   quantileAt(bounds, merged, total, 0.50),
		P95:   quantileAt(bounds, merged, total, 0.95),
		P99:   quantileAt(bounds, merged, total, 0.99),
	}
}

// quantileAt interpolates within the bucket the target rank falls in, which
// is what Prometheus's histogram_quantile does. The result is only ever as
// precise as the bucket edges, so a p99 that lands in the widest bucket
// carries real error. Reporting it to the millisecond would overstate it.
func quantileAt(bounds []float64, cumulative map[float64]uint64, total uint64, q float64) float64 {
	rank := q * float64(total)
	var prevBound float64
	var prevCount uint64
	for _, b := range bounds {
		count := cumulative[b]
		if float64(count) < rank {
			prevBound, prevCount = b, count
			continue
		}
		// The +Inf bucket has no upper edge to interpolate towards, so the
		// best available answer is the last finite edge.
		if isInf(b) {
			return prevBound
		}
		span := float64(count - prevCount)
		if span == 0 {
			return b
		}
		return prevBound + (b-prevBound)*(rank-float64(prevCount))/span
	}
	return prevBound
}

func isInf(f float64) bool { return f > 1e300 }
