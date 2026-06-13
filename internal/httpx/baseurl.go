package httpx

import "net/http"

// requestBaseURL returns the scheme://host the browser used to reach this
// request. Honors X-Forwarded-Proto + X-Forwarded-Host (set by Caddy and
// the Cloudflare Tunnel daemon) so share cards link back to the URL the
// player actually browsed, not the BBG_BASE_URL the server was started with.
//
// Falls back to s.cfg.BaseURL if the request has no useful host hints,
// which only happens in synthetic test contexts. Production requests
// always carry a Host.
func (s *Server) requestBaseURL(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return s.cfg.BaseURL
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + host
}
