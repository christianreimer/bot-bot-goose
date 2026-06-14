// Package ratelimit is a small token-bucket limiter with two backends:
//
//   - Postgres-backed (Postgres struct). One row per bucket key, atomic
//     refill + consume in one UPSERT round-trip. Default for dev and the
//     fallback when Valkey isn't configured.
//   - Valkey-backed (Valkey struct in valkey.go). The same arithmetic
//     done inside a Lua script via EVALSHA so the hot endpoints can
//     rate-limit without touching Postgres at all.
//
// Both implement the Limiter interface. Callers depend on the interface;
// the launch-capacity wiring picks the backend at startup based on
// BBG_VALKEY_URL.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Limiter is the common surface for the token-bucket limiters. Both
// backends share the same fail-open semantics: an internal error returns
// allowed=true with err set, and the caller (an http handler) decides
// whether to log and proceed.
type Limiter interface {
	// Allow consumes one token from the bucket keyed by `key`. allowed=true
	// means the request may proceed. When allowed=false, retryAfter is a
	// suggestion for how long until one token is back.
	//
	//   - capacity is the maximum tokens the bucket can hold.
	//   - refillPerHour is how fast it tops up; e.g. 5 tokens/hour for "5
	//     submissions per hour per device".
	Allow(ctx context.Context, key string, capacity int, refillPerHour float64) (allowed bool, retryAfter time.Duration, err error)
}

// Postgres is the durable, single-table token-bucket limiter. One row per
// bucket key, atomic refill + consume in one round-trip via an UPSERT.
// Good enough for v1 — we're protecting a handful of endpoints (decoy
// submission, magic-link request, auth) where the request rate is low
// and durable cross-process state matters. For hot paths in the play
// loop the cost of a SQL round-trip would dominate; those don't use
// this — they use the Valkey limiter instead.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns the Postgres-backed limiter. Kept as the package's namesake
// constructor because pre-Valkey callers and tests construct it directly.
// Returns a *Postgres typed as Limiter so callers can swap to Valkey
// without code changes.
func New(pool *pgxpool.Pool) Limiter {
	return &Postgres{pool: pool}
}

// Allow on Postgres. The bucket goes negative on denial, which is
// intentional: a client that keeps banging on the endpoint gets penalized
// for longer before the counter comes back to zero.
func (l *Postgres) Allow(ctx context.Context, key string, capacity int, refillPerHour float64) (allowed bool, retryAfter time.Duration, err error) {
	if capacity <= 0 || refillPerHour <= 0 {
		return false, 0, errors.New("ratelimit: capacity and refill must be positive")
	}
	refillPerSec := refillPerHour / 3600.0

	const q = `
		INSERT INTO rate_limit_buckets (key, tokens, refilled_at)
		VALUES ($1, $2::numeric - 1, NOW())
		ON CONFLICT (key) DO UPDATE
		   SET tokens = LEAST(
		                    $2::numeric,
		                    rate_limit_buckets.tokens
		                      + EXTRACT(EPOCH FROM (NOW() - rate_limit_buckets.refilled_at)) * $3::numeric
		                ) - 1,
		       refilled_at = NOW()
		RETURNING tokens
	`
	var tokens float64
	if err := l.pool.QueryRow(ctx, q, key, float64(capacity), refillPerSec).Scan(&tokens); err != nil {
		return false, 0, fmt.Errorf("ratelimit bucket: %w", err)
	}
	if tokens < 0 {
		// Time to recover one full token at the configured refill rate.
		secs := math.Ceil(1.0 / refillPerSec)
		return false, time.Duration(secs) * time.Second, nil
	}
	return true, 0, nil
}
