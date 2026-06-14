// Package metrics centralizes the expvar publication tree for bbg.
//
// Lives in its own package so internal/httpx, internal/cache, and
// internal/ratelimit can all bump counters without creating an import
// cycle. Stays expvar-only on purpose — the launch-capacity plan calls
// for "expvar or prometheus"; expvar is stdlib and good enough, and
// Prometheus can be added later by re-publishing the same counters
// without touching call sites.
package metrics

import (
	"expvar"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	once sync.Once
	root *expvar.Map

	httpReqs     *expvar.Map // route -> *expvar.Int
	httpStatuses *expvar.Map // "route status" -> *expvar.Int
	httpLatency  = &latencyMap{m: map[string]*histogram{}}

	cacheHits    *expvar.Map // namespace -> *expvar.Int
	cacheMisses  *expvar.Map
	cacheErrors  *expvar.Map
	cacheLatency = &latencyMap{m: map[string]*histogram{}}

	breakerOpen expvar.Int
)

// Init publishes the bbg expvar tree. Idempotent — repeated calls in a
// test wrapper or multi-Server construction don't re-publish (expvar
// panics on duplicate names).
func Init(pool *pgxpool.Pool) {
	once.Do(func() {
		root = new(expvar.Map).Init()
		expvar.Publish("bbg", root)

		httpReqs = new(expvar.Map).Init()
		httpStatuses = new(expvar.Map).Init()
		cacheHits = new(expvar.Map).Init()
		cacheMisses = new(expvar.Map).Init()
		cacheErrors = new(expvar.Map).Init()

		root.Set("http_requests", httpReqs)
		root.Set("http_statuses", httpStatuses)
		root.Set("http_latency_ms", httpLatency)
		root.Set("cache_hits", cacheHits)
		root.Set("cache_misses", cacheMisses)
		root.Set("cache_errors", cacheErrors)
		root.Set("cache_latency_ms", cacheLatency)
		root.Set("cache_breaker_open", &breakerOpen)

		root.Set("pool", expvar.Func(func() any {
			if pool == nil {
				return map[string]any{"unconfigured": true}
			}
			s := pool.Stat()
			return map[string]any{
				"total":               int(s.TotalConns()),
				"in_use":              int(s.AcquiredConns()),
				"idle":                int(s.IdleConns()),
				"max":                 int(s.MaxConns()),
				"acquire_count":       s.AcquireCount(),
				"acquire_duration_ns": s.AcquireDuration().Nanoseconds(),
				"empty_acquire_count": s.EmptyAcquireCount(),
				"canceled_acquire":    s.CanceledAcquireCount(),
				"constructing_conns":  int(s.ConstructingConns()),
			}
		}))
	})
}

// Handler returns the http.Handler that serves /metrics. Wraps expvar.Handler
// so an operator sees the whole publication tree at one URL.
func Handler() http.Handler { return expvar.Handler() }

// HTTPRecord registers one request after it completes. route should be a
// chi route pattern (e.g. "/r/{short}/og.png") so cardinality stays bounded.
func HTTPRecord(route string, status int, latency time.Duration) {
	incMap(httpReqs, route)
	incMap(httpStatuses, route+" "+strconv.Itoa(status))
	httpLatency.observe(route, latency)
}

// Cache-side counters. Namespaces are short keys like "users", "rounds",
// "og". Keep the set small — one expvar.Int per name is allocated lazily.
func CacheHit(ns string)                      { incMap(cacheHits, ns) }
func CacheMiss(ns string)                     { incMap(cacheMisses, ns) }
func CacheError(ns string)                    { incMap(cacheErrors, ns) }
func CacheLatency(ns string, d time.Duration) { cacheLatency.observe(ns, d) }

// SetBreakerOpen flips the cache_breaker_open gauge. 1 = open (we're
// degrading), 0 = closed.
func SetBreakerOpen(open bool) {
	if open {
		breakerOpen.Set(1)
	} else {
		breakerOpen.Set(0)
	}
}

func incMap(m *expvar.Map, key string) {
	if m == nil {
		return
	}
	m.Add(key, 1)
}

// ---------------------------------------------------------------------------
// Histogram — fixed-bucket sketch for p50/p99. Exponential boundaries in
// milliseconds: 1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000.
// Cheap enough to keep one per route and per cache namespace; accurate
// enough for alert thresholds.
// ---------------------------------------------------------------------------

var histBoundsMs = []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

type histogram struct {
	mu     sync.Mutex
	counts [13]uint64
	total  atomic.Uint64
	sumNs  atomic.Uint64
}

func (h *histogram) observe(d time.Duration) {
	ms := float64(d.Milliseconds())
	h.total.Add(1)
	h.sumNs.Add(uint64(d.Nanoseconds()))
	idx := len(histBoundsMs)
	for i, b := range histBoundsMs {
		if ms <= b {
			idx = i
			break
		}
	}
	h.mu.Lock()
	h.counts[idx]++
	h.mu.Unlock()
}

func (h *histogram) snapshot() (count, sumMs uint64, p50, p99 float64) {
	total := h.total.Load()
	if total == 0 {
		return 0, 0, 0, 0
	}
	h.mu.Lock()
	cs := h.counts
	h.mu.Unlock()
	return total, h.sumNs.Load() / 1_000_000, quantile(cs[:], 0.50), quantile(cs[:], 0.99)
}

func quantile(counts []uint64, q float64) float64 {
	var total uint64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := uint64(math.Ceil(q * float64(total)))
	var cum uint64
	for i, c := range counts {
		cum += c
		if cum >= target {
			if i >= len(histBoundsMs) {
				return histBoundsMs[len(histBoundsMs)-1] * 2
			}
			return histBoundsMs[i]
		}
	}
	return histBoundsMs[len(histBoundsMs)-1] * 2
}

type latencyMap struct {
	mu sync.RWMutex
	m  map[string]*histogram
}

func (l *latencyMap) observe(key string, d time.Duration) {
	l.mu.RLock()
	h, ok := l.m[key]
	l.mu.RUnlock()
	if !ok {
		l.mu.Lock()
		h, ok = l.m[key]
		if !ok {
			h = &histogram{}
			l.m[key] = h
		}
		l.mu.Unlock()
	}
	h.observe(d)
}

func (l *latencyMap) String() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := "{"
	first := true
	for k, h := range l.m {
		count, sumMs, p50, p99 := h.snapshot()
		if !first {
			out += ","
		}
		first = false
		out += strconv.Quote(k) + ":"
		out += `{"count":` + strconv.FormatUint(count, 10)
		out += `,"sum_ms":` + strconv.FormatUint(sumMs, 10)
		out += `,"p50_ms":` + strconv.FormatFloat(p50, 'f', -1, 64)
		out += `,"p99_ms":` + strconv.FormatFloat(p99, 'f', -1, 64) + `}`
	}
	out += "}"
	return out
}
