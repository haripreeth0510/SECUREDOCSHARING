package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Denylist records access tokens revoked before their expiry (logout).
// Entries expire with the token, so the list never grows without bound.
type Denylist interface {
	Deny(ctx context.Context, jti string, until time.Time) error
	IsDenied(ctx context.Context, jti string) (bool, error)
}

type redisDenylist struct {
	rdb    *redis.Client
	prefix string
}

func NewRedisDenylist(rdb *redis.Client, prefix string) Denylist {
	return &redisDenylist{rdb: rdb, prefix: prefix + "auth:denylist:"}
}

func (d *redisDenylist) Deny(ctx context.Context, jti string, until time.Time) error {
	ttl := time.Until(until)
	if ttl <= 0 {
		return nil // already expired, nothing to deny
	}
	if err := d.rdb.Set(ctx, d.prefix+jti, 1, ttl).Err(); err != nil {
		return fmt.Errorf("deny token: %w", err)
	}
	return nil
}

func (d *redisDenylist) IsDenied(ctx context.Context, jti string) (bool, error) {
	n, err := d.rdb.Exists(ctx, d.prefix+jti).Result()
	if err != nil {
		return false, fmt.Errorf("check denylist: %w", err)
	}
	return n > 0, nil
}
