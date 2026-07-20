package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter implements sliding-window rate limits over Redis sorted sets
// (`rl:{scope}:{key}`). Each attempt is a zset member scored by unix-nano time;
// old members are trimmed on every check.
type Limiter struct {
	rdb *redis.Client
}

// NewLimiter builds a limiter over the shared Redis client.
func NewLimiter(rdb *redis.Client) *Limiter { return &Limiter{rdb: rdb} }

// Allow records an attempt in the window and reports whether it is within the
// limit. When denied, retryAfter is how long until the oldest counted attempt
// leaves the window. The attempt itself is only recorded when allowed —
// rejected requests must not extend the lockout.
func (l *Limiter) Allow(ctx context.Context, scope, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error) {
	now := time.Now()
	zkey := fmt.Sprintf("rl:%s:%s", scope, key)
	cutoff := now.Add(-window).UnixNano()

	pipe := l.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, zkey, "0", fmt.Sprintf("%d", cutoff))
	countCmd := pipe.ZCard(ctx, zkey)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, fmt.Errorf("redisx: rate limit trim: %w", err)
	}

	if int(countCmd.Val()) >= limit {
		oldest, err := l.rdb.ZRangeWithScores(ctx, zkey, 0, 0).Result()
		if err != nil || len(oldest) == 0 {
			return false, window, nil
		}
		expiry := time.Unix(0, int64(oldest[0].Score)).Add(window)
		ra := time.Until(expiry)
		if ra < time.Second {
			ra = time.Second
		}
		return false, ra, nil
	}

	pipe = l.rdb.TxPipeline()
	pipe.ZAdd(ctx, zkey, redis.Z{Score: float64(now.UnixNano()), Member: fmt.Sprintf("%d", now.UnixNano())})
	pipe.Expire(ctx, zkey, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, fmt.Errorf("redisx: rate limit record: %w", err)
	}
	return true, 0, nil
}
