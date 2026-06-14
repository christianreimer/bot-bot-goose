package httpx

import "net/http"

// handleComingSoon renders the pre-launch placeholder served at "/" (and any
// unrouted path) while BBG_PRELAUNCH_MODE is on. The page is a single
// editorial column: kicker → display headline → one-sentence pitch → CTA
// pointing at /prelaunch → "we launch when the deck is full" coda. It
// reuses the base layout so the brand wordmark stays present and the
// privacy link in the footer remains reachable.
//
// No user is minted on this path — the route is mounted outside the
// session middleware group so a URL-walker doesn't generate a row in
// `users` for every poke at a soon-to-be-real page.
func (s *Server) handleComingSoon(w http.ResponseWriter, r *http.Request) {
	// 200 even when reached via NotFound: poster is the intended response
	// for any URL during pre-launch. Crawlers should treat it as the
	// canonical placeholder, not a missing resource.
	s.renderHTML(w, http.StatusOK, "pages/coming_soon.html", map[string]any{
		"PuzzleNumber": int32(0), // satisfies base layout's pad3 cosmetic
		"BaseURL":      s.requestBaseURL(r),
	})
}
