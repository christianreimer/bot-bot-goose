package httpx

import (
	"net/http"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/metrics"
	"github.com/go-chi/chi/v5"
)

// instrumentHTTP wraps next, recording request count, status count, and
// latency keyed by the chi route pattern. Routes outside chi (e.g., the
// FileServer mounts) fall back to the request path so cardinality is
// bounded by route count, not URL count.
func instrumentHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)
		metrics.HTTPRecord(chiPattern(r), ww.status, time.Since(start))
	})
}

func chiPattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if p := rctx.RoutePattern(); p != "" {
			return p
		}
	}
	return r.URL.Path
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}
