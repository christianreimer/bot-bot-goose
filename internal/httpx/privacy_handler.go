package httpx

import (
	"net/http"

	"github.com/christianreimer/bot-bot-goose/internal/users"
)

// handlePrivacy renders the static-ish privacy disclosure. The page is
// public, stateless, and intentionally NOT inside the session-middleware
// group: a visitor who only reads the privacy page should not have a
// device cookie minted for the privilege.
//
// If the visitor already carries a valid device cookie (i.e. they've
// played before), we resolve it read-only and surface their user ID
// in the Contact section so a "please delete my data" email arrives
// with enough to locate them in the database.
func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	var userID string
	if u, err := users.ResolveOnly(r.Context(), s.cfg.DB, s.cfg.SessionKey, r); err == nil && u != nil {
		userID = u.ID.String()
	}
	s.renderHTML(w, http.StatusOK, "pages/privacy.html", map[string]any{
		"PuzzleNumber": int32(0),
		"BaseURL":      s.requestBaseURL(r),
		"UserID":       userID,
	})
}
