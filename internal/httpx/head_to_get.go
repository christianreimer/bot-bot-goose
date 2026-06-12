package httpx

import "net/http"

// headSupport makes HEAD requests behave like GET (per RFC 9110 §9.3.2):
// same status + headers, empty body. chi does not auto-map HEAD to GET, so
// without this every GET-only route returns 405 on HEAD. Some link-preview
// crawlers (notably Slack and a few corporate gateways) HEAD-then-GET, and
// a 405 on the HEAD makes them give up before fetching the OG tags.
func headSupport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.Method = http.MethodGet
		next.ServeHTTP(&headWriter{ResponseWriter: w}, r2)
	})
}

// headWriter discards the body but keeps headers/status. Per the HTTP spec
// a HEAD response must NOT include a body but MUST include the same
// Content-Length / Content-Type the GET would have produced — those are
// headers and pass through untouched.
type headWriter struct {
	http.ResponseWriter
}

func (h *headWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
