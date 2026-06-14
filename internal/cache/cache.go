// Package cache wraps the Valkey/Redis client used by the launch-capacity
// caches. Every layer that goes through this package shares one universal
// degradation contract: a miss — whether the key is empty, Redis is
// unreachable, or the circuit breaker is open — maps to "go to Postgres."
// Nothing in this package returns stale-on-error.
//
// The Cache type is safe for concurrent use and nil-safe: calling any
// method on a nil receiver is a no-op miss. That lets callers wire the
// cache unconditionally (`s.cache.Get(...)`) and dev / test setups that
// don't run Valkey skip the env var entirely.
package cache

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// ErrUnconfigured is returned by Eval / EvalSha when no Valkey URL was
// supplied at boot. Callers that need fast-path semantics on this (the
// rate limiter falls back to Postgres) check for it explicitly.
var ErrUnconfigured = errors.New("cache: valkey unconfigured")

// ErrBreakerOpen is returned by Eval / EvalSha while the breaker is
// holding the circuit open. The rate limiter treats it the same as a
// raw transport error — fall back to the Postgres path.
var ErrBreakerOpen = errors.New("cache: breaker open")

// Cache is the Valkey wrapper. Use New() to construct; nil is a valid
// no-op instance.
type Cache struct {
	rdb     *redis.Client
	log     *slog.Logger
	breaker *breaker
}

// New constructs a Cache. When url is empty, it returns a nil-safe
// no-op Cache (every Get is a miss, every Set silently drops). Otherwise
// it dials Valkey, pings once to fail fast on a misconfigured URL, and
// installs the breaker.
func New(ctx context.Context, url string, log *slog.Logger) (*Cache, error) {
	if url == "" {
		return &Cache{log: log}, nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return &Cache{
		rdb:     rdb,
		log:     log,
		breaker: newBreaker(10, 30*time.Second),
	}, nil
}

// Enabled reports whether Valkey is configured. Callers should not branch
// on this for cache hits — Get already handles the unconfigured case —
// but the rate limiter checks it to decide between the Valkey path and
// the Postgres fallback at startup.
func (c *Cache) Enabled() bool { return c != nil && c.rdb != nil }

// Client returns the underlying redis.Client for callers that need to run
// scripts against it directly (the rate limiter does this for EVALSHA).
// Returns nil when the cache is unconfigured.
func (c *Cache) Client() *redis.Client {
	if c == nil {
		return nil
	}
	return c.rdb
}

// Close releases the Redis pool. Safe on a nil or unconfigured Cache.
func (c *Cache) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// Get fetches the bytes at `key`. Returns (nil, false) on a miss, on a
// transport error (the breaker absorbs the failure), or when the cache
// is unconfigured. The miss path always lands on Postgres.
func (c *Cache) Get(ctx context.Context, ns, key string) ([]byte, bool) {
	if !c.Enabled() {
		return nil, false
	}
	if c.breaker.open() {
		metrics.CacheMiss(ns)
		return nil, false
	}
	start := time.Now()
	b, err := c.rdb.Get(ctx, key).Bytes()
	metrics.CacheLatency(ns, time.Since(start))
	switch {
	case err == nil:
		c.breaker.succeed()
		metrics.CacheHit(ns)
		return b, true
	case errors.Is(err, redis.Nil):
		c.breaker.succeed()
		metrics.CacheMiss(ns)
		return nil, false
	default:
		c.breaker.fail()
		metrics.CacheError(ns)
		if c.log != nil {
			c.log.Warn("cache get", "ns", ns, "err", err)
		}
		return nil, false
	}
}

// Set writes the bytes at `key` with the given TTL. A zero ttl is a
// permanent set (no expiry); every other caller passes a positive TTL.
// Errors are swallowed (the next read just misses).
func (c *Cache) Set(ctx context.Context, ns, key string, val []byte, ttl time.Duration) {
	if !c.Enabled() {
		return
	}
	if c.breaker.open() {
		return
	}
	if err := c.rdb.Set(ctx, key, val, ttl).Err(); err != nil {
		c.breaker.fail()
		metrics.CacheError(ns)
		if c.log != nil {
			c.log.Warn("cache set", "ns", ns, "err", err)
		}
		return
	}
	c.breaker.succeed()
}

// Del removes one or more keys. No-op when unconfigured or the breaker
// is open; errors are swallowed.
func (c *Cache) Del(ctx context.Context, ns string, keys ...string) {
	if !c.Enabled() || len(keys) == 0 {
		return
	}
	if c.breaker.open() {
		return
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		c.breaker.fail()
		metrics.CacheError(ns)
		if c.log != nil {
			c.log.Warn("cache del", "ns", ns, "err", err)
		}
		return
	}
	c.breaker.succeed()
}

// Eval runs a redis.Script. Returns ErrUnconfigured / ErrBreakerOpen on
// the degraded paths so the rate limiter can detect them and fall back
// to Postgres. Other Redis errors are wrapped through.
func (c *Cache) Eval(ctx context.Context, ns string, script *redis.Script, keys []string, args ...any) (any, error) {
	if !c.Enabled() {
		return nil, ErrUnconfigured
	}
	if c.breaker.open() {
		return nil, ErrBreakerOpen
	}
	start := time.Now()
	res, err := script.Run(ctx, c.rdb, keys, args...).Result()
	metrics.CacheLatency(ns, time.Since(start))
	if err != nil && !errors.Is(err, redis.Nil) {
		c.breaker.fail()
		metrics.CacheError(ns)
		return nil, err
	}
	c.breaker.succeed()
	return res, nil
}

// ---------------------------------------------------------------------------
// Circuit breaker. 10 consecutive errors → open for 30s. While open every
// Get returns a miss and every Set/Del is a no-op without touching Redis.
// The metrics gauge flips with the state so an operator dashboard can
// see "cache is degraded" at a glance.
// ---------------------------------------------------------------------------

type breaker struct {
	threshold int
	cooldown  time.Duration

	mu       sync.Mutex
	failures int
	openedAt time.Time
	isOpen   atomic.Bool
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown}
}

func (b *breaker) open() bool {
	if !b.isOpen.Load() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.openedAt) >= b.cooldown {
		b.failures = 0
		b.isOpen.Store(false)
		metrics.SetBreakerOpen(false)
		return false
	}
	return true
}

func (b *breaker) fail() {
	b.mu.Lock()
	b.failures++
	if b.failures >= b.threshold && !b.isOpen.Load() {
		b.openedAt = time.Now()
		b.isOpen.Store(true)
		metrics.SetBreakerOpen(true)
	}
	b.mu.Unlock()
}

func (b *breaker) succeed() {
	b.mu.Lock()
	if b.failures > 0 {
		b.failures = 0
	}
	b.mu.Unlock()
}
