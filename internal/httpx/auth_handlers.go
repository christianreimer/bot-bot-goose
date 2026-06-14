package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/auth"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/email"
	"github.com/christianreimer/bot-bot-goose/internal/users"
	"github.com/go-chi/chi/v5"
)

// Magic-link rate limits. Tighter than decoy submission because every
// request triggers an outbound email (cost + reputation).
const (
	magicRequestEmailCapacity = 3
	magicRequestEmailRefill   = 3.0 // per hour

	magicRequestIPCapacity = 5
	magicRequestIPRefill   = 5.0 // per hour
)

type magicRequest struct {
	Email string `json:"email"`
}

// handleMagicRequest issues a magic link if the email is plausible. It
// ALWAYS returns a 200 with a generic body — even on rate-limit, even on
// validation failure, even on send-failure. The endpoint must not be
// usable to enumerate which emails are registered or which IPs are over
// quota. Internally, we still log + observe.
func (s *Server) handleMagicRequest(w http.ResponseWriter, r *http.Request) {
	var body magicRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Tell the client the request was malformed but still don't leak
		// anything about the address.
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "bad_body"})
		return
	}
	email := auth.NormalizeEmail(body.Email)
	if !auth.ValidEmail(email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "bad_email"})
		return
	}

	ctx := r.Context()
	respondGeneric := func() {
		writeJSON(w, http.StatusOK, map[string]string{"code": "ok"})
	}

	// Rate limits — both per-email and per-IP must pass.
	if ok, _, err := s.limiter.Allow(ctx, "magic:email:"+email, magicRequestEmailCapacity, magicRequestEmailRefill); err == nil && !ok {
		s.cfg.Logger.Info("magic_request rate limited (email)", "email_hash", emailHash(email))
		respondGeneric()
		return
	}
	if ok, _, err := s.limiter.Allow(ctx, "magic:ip:"+clientIP(r), magicRequestIPCapacity, magicRequestIPRefill); err == nil && !ok {
		s.cfg.Logger.Info("magic_request rate limited (ip)", "ip", clientIP(r))
		respondGeneric()
		return
	}

	// Self-contained signed token: the email rides inside the token under
	// HMAC. The DB only ever sees sha256(token).
	token, hash := auth.IssueMagicToken(s.cfg.SessionKey, email)
	expires := time.Now().Add(auth.TokenLifetime)
	if err := s.cfg.DB.InsertMagicLink(ctx, hash, expires, clientIP(r)); err != nil {
		s.cfg.Logger.Error("magic_request insert", "err", err)
		respondGeneric()
		return
	}

	link := s.requestBaseURL(r) + "/auth/magic/" + token
	if err := s.sendMagicEmail(ctx, email, link); err != nil {
		s.cfg.Logger.Error("magic_request send", "err", err)
		// Still return generic — the user clicked, the email may eventually
		// arrive on retry; surfacing this would leak whether the email is
		// real.
	}
	respondGeneric()
}

func (s *Server) sendMagicEmail(ctx context.Context, to, link string) error {
	subject := "Sign in to Bot Bot Goose"
	text := fmt.Sprintf(`Tap the link below to sign in:

%s

This link expires in 15 minutes and can only be used once.
If you didn't ask to sign in, ignore this email.

Bot Bot Goose
`, link)
	html := fmt.Sprintf(`<!doctype html>
<html><body style="font-family:system-ui,sans-serif;line-height:1.5;color:#14232b;">
<p>Tap the button below to sign in to Bot Bot Goose:</p>
<p><a href="%s" style="display:inline-block;background:#f4a23b;color:#241400;padding:12px 22px;border-radius:10px;text-decoration:none;font-weight:700;">Sign in 🪿</a></p>
<p style="color:#8fa6ae;font-size:13px;">Or paste this link into your browser:<br><code>%s</code></p>
<p style="color:#8fa6ae;font-size:13px;">This link expires in 15 minutes and can only be used once. If you didn't ask to sign in, ignore this email.</p>
</body></html>`, link, link)
	return s.cfg.Email.Send(ctx, email.Message{To: to, Subject: subject, Text: text, HTML: html})
}

// handleMagicConsume validates the signed token, recovers the email from
// its body (the DB has none), migrates the anonymous user into the
// email-bound user (or promotes them in place if no email-bound user
// exists), and redirects to /me.
func (s *Server) handleMagicConsume(w http.ResponseWriter, r *http.Request) {
	rawToken := chi.URLParam(r, "token")
	if rawToken == "" {
		s.renderMagicResult(w, "invalid")
		return
	}

	// First: verify the HMAC and extract the email from the token body.
	// A failure here means the link was tampered with — the DB doesn't
	// even need to be queried.
	mail, hash, err := auth.ParseMagicToken(s.cfg.SessionKey, rawToken)
	if err != nil {
		s.renderMagicResult(w, "invalid")
		return
	}

	ctx := r.Context()
	now := time.Now()

	// Second: enforce one-time-use + expiry against the magic_links row.
	// Any unusable state collapses to "invalid" so the link can't be used
	// to enumerate which addresses have outstanding tokens.
	if err := s.cfg.DB.ConsumeMagicLink(ctx, hash, now); err != nil {
		s.renderMagicResult(w, "invalid")
		return
	}

	current := users.FromContext(ctx)
	if current == nil {
		// No device-cookie session somehow. Render a "click play to start"
		// page rather than an opaque error.
		s.renderMagicResult(w, "no_session")
		return
	}

	// Resolve identity migration. If an email-bound user already exists,
	// merge the current anonymous user into it. Otherwise, just set the
	// email on the current user (promotion).
	existing, err := s.cfg.DB.UserByEmail(ctx, mail)
	switch {
	case err == nil:
		if existing.ID != current.ID {
			if err := s.cfg.DB.MergeUsers(ctx, current.ID, existing.ID, now); err != nil {
				s.cfg.Logger.Error("magic_consume merge", "err", err)
				s.renderMagicResult(w, "merge_failed")
				return
			}
		}
		// device_cookies entries for the current user were re-bound by the
		// merge transaction — the next request will resolve to `existing`.
	case errors.Is(err, db.ErrNotFound):
		if err := s.cfg.DB.SetEmailOnUser(ctx, current.ID, mail, now); err != nil {
			s.cfg.Logger.Error("magic_consume set email", "err", err)
			s.renderMagicResult(w, "set_email_failed")
			return
		}
	default:
		s.cfg.Logger.Error("magic_consume user lookup", "err", err)
		s.renderMagicResult(w, "lookup_failed")
		return
	}

	http.Redirect(w, r, "/me?signed_in=1", http.StatusSeeOther)
}

// renderMagicResult shows a small terminal-state page (not an error page).
// Used for invalid / expired / consumed tokens. We don't differentiate.
func (s *Server) renderMagicResult(w http.ResponseWriter, kind string) {
	headline := "That link won't work."
	body := "Magic links expire after 15 minutes and only work once. Request a fresh one from the sign-in form."
	if kind == "no_session" {
		headline = "Open the game first."
		body = "Play any puzzle once so we have a session to attach your account to, then click the link from your email again."
	}
	s.renderHTML(w, http.StatusOK, "pages/magic_result.html", map[string]any{
		"PuzzleNumber": int32(0),
		"Headline":     headline,
		"Body":         body,
		"BaseURL":      s.cfg.BaseURL,
	})
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------
//
// Two flavors:
//
//   POST /api/auth/logout      sign out of THIS device only — deletes the
//                              device_cookies row keyed on the current
//                              cookie's hash + expires the cookie.
//   POST /api/auth/logout-all  sign out of every device for this user.
//                              Useful as a security response ("I left my
//                              phone in a taxi"). Same cookie-clear plus
//                              a DELETE across all of the user's
//                              device_cookies rows.
//
// Neither touches the user row or any owned data. Magic-link sign-in
// brings them right back to the same account.

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	u := users.FromContext(r.Context())
	hash := users.CurrentDeviceCookieHash(r, s.cfg.SessionKey)
	if u != nil && hash != nil {
		if err := s.cfg.DB.DeleteDeviceCookie(r.Context(), hash); err != nil {
			s.cfg.Logger.Warn("logout: delete cookie row", "err", err)
		}
		users.InvalidateCookieHash(r.Context(), s.cfg.Cache, hash)
	}
	users.ClearCookie(w, s.cfg.SecureCookie)
	writeJSON(w, http.StatusOK, map[string]string{"code": "ok"})
}

func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	u := users.FromContext(r.Context())
	if u != nil {
		if err := s.cfg.DB.DeleteAllDeviceCookiesForUser(r.Context(), u.ID); err != nil {
			s.cfg.Logger.Warn("logout-all: delete user cookies", "err", err)
		}
		// Caller-driven invalidation of every per-device user cache entry is
		// impossible without enumerating cookie hashes (we don't store them
		// indexed by user_id). The cached blobs age out within
		// users.userCacheTTL — see plan §2.3 trade-off.
	}
	users.ClearCookie(w, s.cfg.SecureCookie)
	writeJSON(w, http.StatusOK, map[string]string{"code": "ok"})
}

// ---------------------------------------------------------------------------
// /me handle + anonymous toggle
// ---------------------------------------------------------------------------

type handlePatchReq struct {
	Handle string `json:"handle"`
}

// reserved handles can't be claimed because they're either implicit
// states ("anonymous") or impersonation hazards ("admin", "system").
var reservedHandles = map[string]bool{
	"anonymous": true,
	"admin":     true,
	"system":    true,
	"bot":       true,
	"goose":     true,
	"bbg":       true,
}

func (s *Server) handlePatchHandle(w http.ResponseWriter, r *http.Request) {
	var body handlePatchReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_body", "")
		return
	}
	h := strings.TrimSpace(body.Handle)
	if !validHandle(h) {
		writeJSONErr(w, http.StatusBadRequest, "bad_handle", "3–20 chars, letters/digits/_/- only")
		return
	}
	lower := strings.ToLower(h)
	// AnonymousGoose<n> is the auto-assigned-handle namespace (migration
	// 0007). Reserving the lowercase prefix prevents a user from manually
	// picking a value that would later collide with a freshly minted one.
	if strings.HasPrefix(lower, "anonymousgoose") {
		writeJSONErr(w, http.StatusBadRequest, "reserved", "that handle is reserved")
		return
	}
	if reservedHandles[lower] {
		writeJSONErr(w, http.StatusBadRequest, "reserved", "that handle is reserved")
		return
	}
	u := users.FromContext(r.Context())
	if err := s.cfg.DB.SetHandle(r.Context(), u.ID, h); err != nil {
		if errors.Is(err, db.ErrHandleTaken) {
			writeJSONErr(w, http.StatusConflict, "handle_taken", "someone else already has that handle")
			return
		}
		writeJSONErr(w, http.StatusInternalServerError, "db", err.Error())
		return
	}
	// Invalidate the cached user blob for THIS device so the next request
	// sees the new handle immediately. Sibling devices still ride the TTL.
	if hash := users.CurrentDeviceCookieHash(r, s.cfg.SessionKey); hash != nil {
		users.InvalidateCookieHash(r.Context(), s.cfg.Cache, hash)
	}
	writeJSON(w, http.StatusOK, map[string]string{"handle": h})
}

func validHandle(h string) bool {
	if len(h) < 3 || len(h) > 20 {
		return false
	}
	if h[0] == '-' || h[0] == '_' {
		return false
	}
	for _, c := range h {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// emailHash is a 64-bit short hash of an email, used in log lines so we can
// rate-limit-debug without writing the cleartext address to logs.
func emailHash(email string) string {
	var h uint64 = 1469598103934665603 // FNV offset basis
	for i := 0; i < len(email); i++ {
		h ^= uint64(email[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
