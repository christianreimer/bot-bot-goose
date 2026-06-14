package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/redis/go-redis/v9"
)

// Valkey runs the same token-bucket arithmetic as Postgres but inside a
// Lua script evaluated against the Valkey/Redis store. Hot endpoints can
// rate-limit at ~1ms instead of paying a Postgres round-trip. Per-key
// state lives in a single hash; the 1-hour EXPIRE caps unbounded growth
// from one-off keys.
//
// Failure modes:
//
//   - Valkey unconfigured (no URL at boot): the wiring picks Postgres
//     instead, so this struct never sees that case in practice. The
//     Allow path still guards for it for safety.
//   - Valkey breaker open: the script returns ErrBreakerOpen via cache.Eval;
//     Allow falls back to Postgres so a Valkey outage stays graceful.
//   - Lua / network error: same fall-back. Logged once per breaker trip
//     by the cache layer.
type Valkey struct {
	cache    *cache.Cache
	fallback Limiter
	script   *redis.Script
}

// NewValkey constructs the Valkey-backed limiter with a Postgres fallback.
// If cache.Enabled() is false at startup, callers should prefer the
// Postgres limiter directly — this constructor still works, but every
// Allow call will short-circuit through the fallback.
func NewValkey(c *cache.Cache, fallback Limiter) *Valkey {
	return &Valkey{
		cache:    c,
		fallback: fallback,
		script:   redis.NewScript(tokenBucketLua),
	}
}

// Allow consumes one token from the Valkey-resident bucket. Returns the
// Postgres limiter's verdict on any Valkey failure so an outage doesn't
// silently disable rate limiting.
func (v *Valkey) Allow(ctx context.Context, key string, capacity int, refillPerHour float64) (bool, time.Duration, error) {
	if capacity <= 0 || refillPerHour <= 0 {
		return false, 0, errors.New("ratelimit: capacity and refill must be positive")
	}
	if v.cache == nil || !v.cache.Enabled() {
		return v.fallback.Allow(ctx, key, capacity, refillPerHour)
	}
	refillPerSec := refillPerHour / 3600.0
	nowMs := time.Now().UnixMilli()

	res, err := v.cache.Eval(ctx, "ratelimit", v.script, []string{key},
		strconv.Itoa(capacity),
		strconv.FormatFloat(refillPerSec, 'f', -1, 64),
		strconv.FormatInt(nowMs, 10),
	)
	if err != nil {
		// ErrUnconfigured / ErrBreakerOpen / transport errors all flow here.
		// Fall back to Postgres so rate limiting stays effective.
		if v.fallback != nil {
			return v.fallback.Allow(ctx, key, capacity, refillPerHour)
		}
		return true, 0, fmt.Errorf("ratelimit valkey: %w", err)
	}

	// The Lua script returns the post-decrement token count as a string so
	// it survives Redis's integer-rounding when refillPerSec is fractional.
	var tokens float64
	switch v := res.(type) {
	case string:
		tokens, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return true, 0, fmt.Errorf("ratelimit valkey: parse tokens %q: %w", v, err)
		}
	case int64:
		tokens = float64(v)
	default:
		return true, 0, fmt.Errorf("ratelimit valkey: unexpected result type %T", res)
	}
	if tokens < 0 {
		secs := math.Ceil(1.0 / refillPerSec)
		return false, time.Duration(secs) * time.Second, nil
	}
	return true, 0, nil
}

// Lua token-bucket. Mirrors the Postgres UPSERT exactly: refill since the
// last touch, cap at capacity, decrement one, persist. Returns the new
// token count as a string so the caller sees the fractional bucket level
// and can apply the same negative-on-denial semantics.
//
// KEYS[1] = bucket key
// ARGV[1] = capacity   (int)
// ARGV[2] = refill_per_sec
// ARGV[3] = now_unix_ms
const tokenBucketLua = `
local cap = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local now  = tonumber(ARGV[3])

local b = redis.call('HMGET', KEYS[1], 't', 'r')
local tokens = tonumber(b[1])
local last   = tonumber(b[2])
if tokens == nil then tokens = cap end
if last == nil then last = now end

local delta = math.max(0, now - last) / 1000.0
tokens = math.min(cap, tokens + delta * rate) - 1

redis.call('HMSET', KEYS[1], 't', tokens, 'r', now)
redis.call('EXPIRE', KEYS[1], 3600)
return tostring(tokens)
`
