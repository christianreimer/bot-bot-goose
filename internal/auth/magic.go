// Package auth holds the magic-link flow: token generation, storage,
// verification, and the identity-merge transaction that ties an anonymous
// device cookie to an email-bound user.
//
// The schema (db/migrations/0001_init.sql) stores SHA-256 hashes of tokens,
// never cleartext. The cleartext token only ever lives in the email
// message itself; if the magic_links table leaks, an attacker still can't
// log in as anyone.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// TokenLifetime is how long a freshly-issued token is honored. 15 min is
// long enough for slow email pipelines but short enough that a leaked link
// can't be replayed forever.
const TokenLifetime = 15 * time.Minute

// magicSalt + magicSig sizes in the encoded token. Layout:
//
//	[16 bytes salt][32 bytes HMAC-SHA256][N bytes email]
//
// HMAC covers the salt + email. The whole thing is base64url-encoded.
const (
	magicSaltLen = 16
	magicSigLen  = 32
)

// IssueMagicToken builds a self-contained, signed magic-link token that
// carries the email in its body and the matching HMAC in its head. The
// caller sends `token` in the email; the cleartext email is NEVER stored
// in the magic_links table — only `tokenHash` (sha256 of the raw bytes).
//
// At consume time, ParseMagicToken extracts the email back from the token
// after verifying the HMAC, so the consumed row never needs the cleartext.
func IssueMagicToken(serverKey []byte, email string) (token string, tokenHash []byte) {
	salt := make([]byte, magicSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic("crypto/rand failure: " + err.Error())
	}
	mac := hmac.New(sha256.New, deriveMagicKey(serverKey))
	mac.Write(salt)
	mac.Write([]byte(email))
	sig := mac.Sum(nil)

	raw := make([]byte, 0, magicSaltLen+magicSigLen+len(email))
	raw = append(raw, salt...)
	raw = append(raw, sig...)
	raw = append(raw, []byte(email)...)

	h := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), h[:]
}

// ParseMagicToken validates the HMAC and returns the embedded email plus the
// token's SHA-256 (used by the DB to enforce one-time-use + expiry).
// Returns ErrTokenInvalid for any tampering: bad base64, bad length, bad
// HMAC. Callers must NOT distinguish between these in the user-facing
// response.
func ParseMagicToken(serverKey []byte, token string) (email string, tokenHash []byte, err error) {
	raw, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil || len(raw) < magicSaltLen+magicSigLen+1 {
		return "", nil, ErrTokenInvalid
	}
	salt := raw[:magicSaltLen]
	sig := raw[magicSaltLen : magicSaltLen+magicSigLen]
	payload := raw[magicSaltLen+magicSigLen:]

	mac := hmac.New(sha256.New, deriveMagicKey(serverKey))
	mac.Write(salt)
	mac.Write(payload)
	want := mac.Sum(nil)
	if !hmac.Equal(sig, want) {
		return "", nil, ErrTokenInvalid
	}
	h := sha256.Sum256(raw)
	return string(payload), h[:], nil
}

// HashTokenForLookup is the DB-storage form for a presented raw token. It's
// the same value IssueMagicToken returns as `tokenHash` and ParseMagicToken
// returns alongside the email.
func HashTokenForLookup(token string) []byte {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		// Fall back to hashing the encoded form so a tampered token still
		// produces a deterministic non-matching hash.
		h := sha256.Sum256([]byte(token))
		return h[:]
	}
	h := sha256.Sum256(raw)
	return h[:]
}

// deriveMagicKey domain-separates the magic-link HMAC from any other HMAC
// the same server key might be used for (play tokens, cookie auth). The
// trailing ":magic" tag isn't a secret; it just ensures key reuse can't
// accidentally produce identical signatures across surfaces.
func deriveMagicKey(serverKey []byte) []byte {
	mac := hmac.New(sha256.New, serverKey)
	mac.Write([]byte(":magic"))
	return mac.Sum(nil)
}

// NormalizeEmail lowercases + trims an email. Postgres's citext column
// handles case-insensitive equality but we still normalize on input so
// rate-limit keys and "we already sent" checks are stable.
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidEmail is a lightweight check, not full RFC 5321. It catches obvious
// typos and prevents fields like "username" from being treated as an
// address. The real validation is delivery — if Resend can't deliver, it's
// not a valid address for our purposes.
func ValidEmail(s string) bool {
	at := strings.Index(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	local := s[:at]
	dom := s[at+1:]
	if len(local) > 64 || len(dom) > 255 {
		return false
	}
	if !strings.Contains(dom, ".") {
		return false
	}
	for _, c := range s {
		if c <= 0x20 || c >= 0x7f {
			return false
		}
	}
	return true
}

// ErrTokenInvalid covers expired, unknown, or already-consumed tokens.
// Callers should NOT distinguish between these — leaking "this token is
// expired vs unknown" would help an attacker validate guesses.
var ErrTokenInvalid = errors.New("auth: token invalid")
