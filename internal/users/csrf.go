package users

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

const (
	csrfCookie = "bbg_csrf_v1"
	csrfHeader = "X-CSRF-Token"
)

// CSRFMiddleware uses the double-submit pattern: a CSRF cookie value must
// equal the X-CSRF-Token header on every state-changing request.
//
// The cookie is set on every request (rolling) and is NOT HttpOnly because
// JS needs to read it to set the header.
func CSRFMiddleware(secureCookie bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := ""
			if c, err := r.Cookie(csrfCookie); err == nil {
				tok = c.Value
			}
			if tok == "" {
				tok = newCSRFToken()
				http.SetCookie(w, &http.Cookie{
					Name:     csrfCookie,
					Value:    tok,
					Path:     "/",
					HttpOnly: false,
					Secure:   secureCookie,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   60 * 60 * 24 * 7,
				})
			}
			if isStateChanging(r) {
				if got := r.Header.Get(csrfHeader); !hmac.Equal([]byte(got), []byte(tok)) {
					http.Error(w, "csrf", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func newCSRFToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("csrf rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func isStateChanging(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}
