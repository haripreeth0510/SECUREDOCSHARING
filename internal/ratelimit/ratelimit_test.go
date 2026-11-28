package ratelimit_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/securedocsharing/app/internal/ratelimit"
)

// newLimiter connects to REDIS_ADDR and namespaces keys per test run.
func newLimiter(t *testing.T) ratelimit.Limiter {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not reachable: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return ratelimit.NewRedis(rdb, "test:"+hex.EncodeToString(b)+":")
}

func TestAllowWithinAndOverLimit(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t)

	for i := 0; i < 3; i++ {
		d, err := l.Allow(ctx, "k", 3, time.Minute)
		if err != nil || !d.Allowed {
			t.Fatalf("hit %d: %+v %v", i+1, d, err)
		}
	}
	d, err := l.Allow(ctx, "k", 3, time.Minute)
	if err != nil || d.Allowed {
		t.Fatalf("4th hit must be blocked: %+v %v", d, err)
	}
	if d.RetryAfter < time.Second || d.RetryAfter > time.Minute {
		t.Fatalf("RetryAfter out of range: %v", d.RetryAfter)
	}

	// Keys are independent.
	if d, _ := l.Allow(ctx, "other", 3, time.Minute); !d.Allowed {
		t.Fatal("separate key must not share the limit")
	}
}

func TestWindowSlides(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t)
	window := 300 * time.Millisecond

	for i := 0; i < 2; i++ {
		if d, _ := l.Allow(ctx, "k", 2, window); !d.Allowed {
			t.Fatal("expected allowed")
		}
	}
	if d, _ := l.Allow(ctx, "k", 2, window); d.Allowed {
		t.Fatal("expected blocked")
	}
	time.Sleep(window + 50*time.Millisecond)
	if d, _ := l.Allow(ctx, "k", 2, window); !d.Allowed {
		t.Fatal("expected allowed after the window passed")
	}
}

func TestConcurrentHitsNeverExceedLimit(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t)

	const limit, callers = 10, 50
	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := l.Allow(ctx, "burst", limit, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if d.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != limit {
		t.Fatalf("allowed %d of %d concurrent hits, want exactly %d", allowed, callers, limit)
	}
}

func TestInvalidArguments(t *testing.T) {
	l := newLimiter(t)
	if _, err := l.Allow(context.Background(), "k", 0, time.Minute); err == nil {
		t.Fatal("limit 0 must be rejected")
	}
}
