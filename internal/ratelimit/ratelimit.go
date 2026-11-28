// Package ratelimit implements a Redis-backed sliding-window rate limiter that
// is shared by every API instance.
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration // set when Allowed is false
}

type Limiter interface {
	// Allow records one hit against key and reports whether it is within
	// limit hits per window.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error)
}

// The script runs atomically in Redis and uses Redis's own clock, so every
// API instance agrees on the window regardless of local clock drift.
// Rejected hits are not recorded, so a client that keeps retrying while
// blocked is let back in as soon as the window allows.
var slidingWindow = redis.NewScript(`
local key    = KEYS[1]
local window = tonumber(ARGV[1])
local limit  = tonumber(ARGV[2])
local member = ARGV[3]
local t      = redis.call('TIME')
local now    = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
if redis.call('ZCARD', key) < limit then
  redis.call('ZADD', key, now, member)
  redis.call('PEXPIRE', key, window)
  return {1, 0}
end
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
return {0, tonumber(oldest[2]) + window - now}
`)

type redisLimiter struct {
	rdb    *redis.Client
	prefix string
}

// NewRedis returns a limiter whose keys are namespaced under prefix.
func NewRedis(rdb *redis.Client, prefix string) Limiter {
	return &redisLimiter{rdb: rdb, prefix: prefix + "ratelimit:"}
}

func (l *redisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error) {
	if limit <= 0 || window <= 0 {
		return Decision{}, fmt.Errorf("ratelimit: limit and window must be positive")
	}
	member := make([]byte, 8)
	if _, err := rand.Read(member); err != nil {
		return Decision{}, fmt.Errorf("ratelimit: %w", err)
	}
	res, err := slidingWindow.Run(ctx, l.rdb, []string{l.prefix + key},
		window.Milliseconds(), limit, hex.EncodeToString(member)).Int64Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("ratelimit: %w", err)
	}
	if res[0] == 1 {
		return Decision{Allowed: true}, nil
	}
	retry := time.Duration(res[1]) * time.Millisecond
	if retry < time.Second {
		retry = time.Second
	}
	return Decision{Allowed: false, RetryAfter: retry}, nil
}
