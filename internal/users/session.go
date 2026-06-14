// Package users owns the device-cookie + (future) magic-link auth.
//
// The first play is intentionally anonymous: a fresh visitor gets a random
// device cookie, a freshly created users row, and immediately starts
// playing. Magic-link email upgrade is a future step (it migrates a device
// cookie's user_id to the existing email-bound user if there is one).
package users

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/christianreimer/bot-bot-goose/internal/db"
)

// userCacheTTL is the rolling lifetime of a cached UserByCookieHash blob.
// The Valkey cache absorbs the hot per-request SELECT during launch traffic
// while staying short enough that handle / role changes propagate within
// minutes. See plans/launch-capacity §2.3.
const userCacheTTL = 5 * time.Minute

// cacheKeyForCookieHash is "u:<hex>" — the namespace prefix the plan calls
// out so an operator running `redis-cli KEYS 'u:*'` can read session
// pressure at a glance.
func cacheKeyForCookieHash(hash []byte) string {
	return "u:" + hex.EncodeToString(hash)
}

func loadUserFromCache(ctx context.Context, c *cache.Cache, hash []byte) *db.User {
	if c == nil {
		return nil
	}
	b, ok := c.Get(ctx, "users", cacheKeyForCookieHash(hash))
	if !ok {
		return nil
	}
	var u db.User
	if err := json.Unmarshal(b, &u); err != nil {
		return nil
	}
	return &u
}

func storeUserInCache(ctx context.Context, c *cache.Cache, hash []byte, u *db.User) {
	if c == nil || u == nil {
		return
	}
	b, err := json.Marshal(u)
	if err != nil {
		return
	}
	c.Set(ctx, "users", cacheKeyForCookieHash(hash), b, userCacheTTL)
}

// InvalidateCookieHash is the public hook handlers call after a state
// change that should not wait for the TTL — logout, handle change.
// Other mutations (logout-all, SetUserRole) can't enumerate every
// session's hash; for those the TTL is the bound. See plan §2.3.
func InvalidateCookieHash(ctx context.Context, c *cache.Cache, hash []byte) {
	if c == nil || hash == nil {
		return
	}
	c.Del(ctx, "users", cacheKeyForCookieHash(hash))
}

const (
	// CookieName is the device-cookie's name. v1 reflects the format below.
	CookieName = "bbg_dev_v1"
	// CookieMaxAge is rolling — refreshed on every request that resolves a user.
	CookieMaxAge = 365 * 24 * time.Hour
)

type ctxKey int

const userKey ctxKey = 1

// FromContext returns the authenticated user, or nil if anonymous (which
// shouldn't happen post-middleware but is checked defensively).
func FromContext(ctx context.Context) *db.User {
	u, _ := ctx.Value(userKey).(*db.User)
	return u
}

// Middleware resolves or mints a device cookie on every request. It is
// applied broadly so the play loop can trust a user is present; admin /
// auth-only handlers add their own role checks on top.
//
// c may be nil — when it is, the Valkey path is skipped and every lookup
// goes straight to Postgres. This matches the launch-capacity plan's
// universal "miss → Postgres" rule and keeps dev/test setups working
// without Valkey running.
func Middleware(d *db.DB, c *cache.Cache, secret []byte, secureCookie bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			raw, _ := readCookie(r)
			user, cleartext, err := resolveOrMint(ctx, d, c, secret, raw, r.UserAgent())
			if err != nil {
				http.Error(w, "session error", http.StatusInternalServerError)
				return
			}

			// Always (re)set the cookie so MaxAge rolls forward and a
			// fresh-mint shows up on the response.
			setCookie(w, cleartext, secureCookie)

			ctx = context.WithValue(ctx, userKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// resolveOrMint either looks up the user behind a presented cookie, or
// creates a brand-new anonymous user + cookie if the presented one is
// missing/invalid. Returns the resolved user and the (possibly new)
// cleartext cookie value to set on the response.
func resolveOrMint(ctx context.Context, d *db.DB, c *cache.Cache, secret []byte, presented, ua string) (*db.User, string, error) {
	if presented != "" {
		cleartext, ok := unwrapCookie(secret, presented)
		if ok {
			hash := hashCookie(cleartext)
			if u := loadUserFromCache(ctx, c, hash); u != nil {
				return u, presented, nil
			}
			if u, err := d.UserByCookieHash(ctx, hash); err == nil {
				storeUserInCache(ctx, c, hash, u)
				return u, presented, nil
			} else if !errors.Is(err, db.ErrNotFound) {
				return nil, "", err
			}
			// Hash unknown: fall through to mint a new identity.
		}
	}

	// Mint.
	userID, handle, err := d.CreateAnonymousUser(ctx)
	if err != nil {
		return nil, "", err
	}
	cleartext := newCookieValue()
	hash := hashCookie(cleartext)
	if err := d.InsertDeviceCookie(ctx, userID, hash, ua); err != nil {
		return nil, "", err
	}
	signed := wrapCookie(secret, cleartext)
	u := &db.User{ID: userID, Handle: &handle, Role: "player", SpotterELO: 1200}
	storeUserInCache(ctx, c, hash, u)
	return u, signed, nil
}

// ResolveOnly looks up the user behind a presented device cookie if one
// exists and is valid. It does NOT mint a new identity when the cookie
// is missing or unrecognized — that's the difference from the regular
// session middleware. Returns (nil, nil) when no user can be resolved
// without writing. Used by the privacy page so that a reader who's
// never played gets no cookie set just for visiting.
func ResolveOnly(ctx context.Context, d *db.DB, c *cache.Cache, secret []byte, r *http.Request) (*db.User, error) {
	raw, _ := readCookie(r)
	if raw == "" {
		return nil, nil
	}
	cleartext, ok := unwrapCookie(secret, raw)
	if !ok {
		return nil, nil
	}
	hash := hashCookie(cleartext)
	if u := loadUserFromCache(ctx, c, hash); u != nil {
		return u, nil
	}
	u, err := d.UserByCookieHash(ctx, hash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	storeUserInCache(ctx, c, hash, u)
	return u, nil
}

// newCookieValue returns 32 random bytes, base64url-encoded. The cleartext
// never leaves the server-side cookie value; we store only its SHA-256.
func newCookieValue() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failure: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashCookie(cleartext string) []byte {
	h := sha256.Sum256([]byte(cleartext))
	return h[:]
}

// wrapCookie produces "<cleartext>.<hmac(secret, cleartext)>". The HMAC
// is a tamper check: it lets us cheaply discard garbage cookies without
// hitting the DB.
func wrapCookie(secret []byte, cleartext string) string {
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write([]byte(cleartext))
	sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	return cleartext + "." + sig
}

func unwrapCookie(secret []byte, raw string) (string, bool) {
	dot := strings.LastIndex(raw, ".")
	if dot <= 0 {
		return "", false
	}
	cleartext := raw[:dot]
	sig := raw[dot+1:]
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write([]byte(cleartext))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", false
	}
	return cleartext, true
}

func readCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

func setCookie(w http.ResponseWriter, value string, secureCookie bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(CookieMaxAge / time.Second),
	})
}

// ClearCookie writes a Set-Cookie header that expires the device cookie
// immediately. Used by the logout handlers. The browser drops the cookie;
// the next request will mint a fresh anonymous identity through the
// session middleware.
func ClearCookie(w http.ResponseWriter, secureCookie bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1, // tells the browser to delete the cookie now
	})
}

// CurrentDeviceCookieHash returns the SHA-256 of the cleartext cookie value
// the request is carrying, or nil if there's no valid cookie. Used by the
// logout handler to identify which device_cookies row to delete.
func CurrentDeviceCookieHash(r *http.Request, secret []byte) []byte {
	raw, ok := readCookie(r)
	if !ok {
		return nil
	}
	cleartext, ok := unwrapCookie(secret, raw)
	if !ok {
		return nil
	}
	return hashCookie(cleartext)
}
