package obs

import (
	"math"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// hist builds one histogram series. counts are cumulative, matching the
// exposition format, and the last entry is the +Inf bucket.
func hist(route string, bounds []float64, counts []uint64) *dto.Metric {
	m := &dto.Metric{
		Label:     []*dto.LabelPair{{Name: strPtr("route"), Value: strPtr(route)}},
		Histogram: &dto.Histogram{SampleCount: &counts[len(counts)-1]},
	}
	for i, b := range bounds {
		bound, count := b, counts[i]
		m.Histogram.Bucket = append(m.Histogram.Bucket,
			&dto.Bucket{UpperBound: &bound, CumulativeCount: &count})
	}
	return m
}

func strPtr(s string) *string { return &s }

func TestQuantileInterpolatesWithinBucket(t *testing.T) {
	// 100 observations, all landing in the 10ms to 25ms bucket. The rank for
	// p50 is 50, the bucket spans ranks 1 to 100, so the answer sits 49/99
	// of the way from 0.01 to 0.025.
	bounds := []float64{0.001, 0.01, 0.025, math.Inf(1)}
	counts := []uint64{0, 1, 100, 100}
	got := quantiles([]*dto.Metric{hist("/api/games", bounds, counts)}, nil)

	if got.Count != 100 {
		t.Fatalf("count = %d, want 100", got.Count)
	}
	want := 0.01 + (0.025-0.01)*(50-1)/99
	if math.Abs(got.P50-want) > 1e-9 {
		t.Errorf("p50 = %v, want %v", got.P50, want)
	}
}

func TestQuantileFallsBackToLastFiniteEdgeInInfBucket(t *testing.T) {
	// Half the observations are above the largest finite edge, so p99 lands
	// in +Inf. There is no upper bound to interpolate towards, so the honest
	// answer is the last finite edge rather than a number invented past it.
	bounds := []float64{0.01, 0.025, math.Inf(1)}
	counts := []uint64{10, 50, 100}
	got := quantiles([]*dto.Metric{hist("/api/games", bounds, counts)}, nil)

	if got.P99 != 0.025 {
		t.Errorf("p99 = %v, want 0.025", got.P99)
	}
}

func TestQuantilesMergeAcrossSeries(t *testing.T) {
	bounds := []float64{0.01, math.Inf(1)}
	a := hist("/api/games", bounds, []uint64{40, 40})
	b := hist("/api/games/{id}", bounds, []uint64{60, 60})

	if got := quantiles([]*dto.Metric{a, b}, nil); got.Count != 100 {
		t.Errorf("merged count = %d, want 100", got.Count)
	}
}

func TestQuantilesHonourTheKeepFilter(t *testing.T) {
	bounds := []float64{0.01, math.Inf(1)}
	api := hist("GET /api/games", bounds, []uint64{40, 40})
	spa := hist("GET /", bounds, []uint64{1000, 1000})

	keep := func(m *dto.Metric) bool { return labelOf(m, "route") == "GET /api/games" }
	got := quantiles([]*dto.Metric{api, spa}, keep)
	if got.Count != 40 {
		t.Errorf("count = %d, want 40; the frontend series leaked in", got.Count)
	}
}

func TestEmptyHistogramReportsNothing(t *testing.T) {
	bounds := []float64{0.01, math.Inf(1)}
	got := quantiles([]*dto.Metric{hist("/api/games", bounds, []uint64{0, 0})}, nil)
	if got != (Quantiles{}) {
		t.Errorf("got %+v, want zero value", got)
	}
}
