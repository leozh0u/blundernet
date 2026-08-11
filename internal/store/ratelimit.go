package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Token bucket, evaluated inside Redis.
//
// Reading the bucket, deciding, and writing it back has to be one atomic step.
// Done as three round trips from Go, two instances handling requests for the
// same user interleave and both see enough tokens, which is exactly the case
// the limit exists for. A script runs to completion on the server.
//
// The clock comes from the caller rather than Redis TIME. Skew between
// instances is milliseconds and the bucket refills continuously, so it cannot
// meaningfully widen the limit, and keeping the script deterministic avoids
// the replication caveats around TIME.
var tokenBucket = redis.NewScript(`
local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local now      = tonumber(ARGV[3])
local cost     = tonumber(ARGV[4])

local state  = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(state[1])
local ts     = tonumber(state[2])
if tokens == nil then
  tokens = capacity
  ts = now
end

local elapsed = now - ts
if elapsed < 0 then elapsed = 0 end
tokens = math.min(capacity, tokens + elapsed * refill)

local allowed = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
-- Expire after the time it takes an empty bucket to refill, so idle keys do
-- not accumulate. A key that expires is indistinguishable from a full bucket,
-- which is the correct outcome anyway.
redis.call('EXPIRE', key, math.ceil(capacity / refill) + 1)

return {allowed, tostring(tokens)}
`)

// Limit is a bucket size and a refill rate. Burst is what a client can spend
// at once; Rate is the sustained ceiling.
type Limit struct {
	Burst int
	Rate  float64 // tokens per second
}

// RetryAfter is roughly how long until one more token exists.
func (l Limit) RetryAfter() time.Duration {
	if l.Rate <= 0 {
		return time.Second
	}
	return time.Duration(float64(time.Second) / l.Rate)
}

type Limiter struct {
	rdb *redis.Client
}

func NewLimiter(rdb *redis.Client) *Limiter { return &Limiter{rdb: rdb} }

// Allow spends one token from the named bucket. On a Redis failure it returns
// true: the limiter is protection against load, not an authorisation check,
// and taking the whole site down because the limiter is unavailable is worse
// than briefly not limiting.
func (l *Limiter) Allow(ctx context.Context, bucket string, limit Limit) bool {
	res, err := tokenBucket.Run(ctx, l.rdb,
		[]string{"ratelimit:" + bucket},
		limit.Burst, limit.Rate, float64(time.Now().UnixNano())/1e9, 1,
	).Slice()
	if err != nil {
		return true
	}
	allowed, ok := res[0].(int64)
	return !ok || allowed == 1
}
