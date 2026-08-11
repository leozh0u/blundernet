package store

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testLimiter(t *testing.T) *Limiter {
	t.Helper()
	mr := miniredis.RunT(t)
	return NewLimiter(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
}

func TestBurstIsSpendableThenRefused(t *testing.T) {
	l := testLimiter(t)
	ctx := context.Background()
	limit := Limit{Burst: 3, Rate: 0.0001} // refill slow enough to ignore

	for i := 0; i < 3; i++ {
		if !l.Allow(ctx, "user:a", limit) {
			t.Fatalf("request %d should have been allowed inside the burst", i+1)
		}
	}
	if l.Allow(ctx, "user:a", limit) {
		t.Error("the fourth request should have exhausted the bucket")
	}
}

func TestBucketsAreIndependent(t *testing.T) {
	l := testLimiter(t)
	ctx := context.Background()
	limit := Limit{Burst: 1, Rate: 0.0001}

	if !l.Allow(ctx, "user:a", limit) {
		t.Fatal("first bucket should be allowed")
	}
	if l.Allow(ctx, "user:a", limit) {
		t.Fatal("first bucket should now be empty")
	}
	if !l.Allow(ctx, "user:b", limit) {
		t.Error("a different bucket must not be affected by the first")
	}
}

// rewind moves the bucket's stored timestamp back, which the script cannot
// tell apart from that much time having passed.
func rewind(t *testing.T, l *Limiter, bucket string, seconds float64) {
	t.Helper()
	ctx := context.Background()
	key := "ratelimit:" + bucket
	ts, err := l.rdb.HGet(ctx, key, "ts").Float64()
	if err != nil {
		t.Fatalf("reading bucket timestamp: %v", err)
	}
	if err := l.rdb.HSet(ctx, key, "ts", ts-seconds).Err(); err != nil {
		t.Fatalf("rewinding bucket: %v", err)
	}
}

func TestTokensRefillOverTime(t *testing.T) {
	l := testLimiter(t)
	ctx := context.Background()
	limit := Limit{Burst: 2, Rate: 1} // one token per second

	l.Allow(ctx, "user:c", limit)
	l.Allow(ctx, "user:c", limit)
	if l.Allow(ctx, "user:c", limit) {
		t.Fatal("bucket should be empty")
	}

	rewind(t, l, "user:c", 2)
	if !l.Allow(ctx, "user:c", limit) {
		t.Error("a token should be available after enough elapsed time")
	}
}

func TestBucketNeverExceedsItsBurst(t *testing.T) {
	l := testLimiter(t)
	ctx := context.Background()
	// A slow rate on purpose. This is testing the clamp on accumulated
	// tokens, and at a fast rate the bucket legitimately refills during the
	// loop below, which measures wall-clock timing rather than the clamp.
	limit := Limit{Burst: 2, Rate: 1}

	l.Allow(ctx, "user:d", limit)
	// A long idle period must not let the bucket accumulate past its size,
	// or a client that waits an hour earns an unbounded burst.
	rewind(t, l, "user:d", 3600)

	allowed := 0
	for i := 0; i < 10; i++ {
		if l.Allow(ctx, "user:d", limit) {
			allowed++
		}
	}
	if allowed > limit.Burst {
		t.Errorf("allowed %d immediately after an idle hour, burst is %d", allowed, limit.Burst)
	}
}
