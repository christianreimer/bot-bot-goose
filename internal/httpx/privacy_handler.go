package httpx

import "net/http"

// handlePrivacy renders the static-ish privacy disclosure. The page is
// public, stateless, and intentionally NOT inside the session-middleware
// group: a visitor who only reads the privacy page should not have a
// device cookie minted for the privilege.
func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	s.renderHTML(w, http.StatusOK, "pages/privacy.html", map[string]any{
		"PuzzleNumber": int32(0),
		"BaseURL":      s.requestBaseURL(r),
	})
}
