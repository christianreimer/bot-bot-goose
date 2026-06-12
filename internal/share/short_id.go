package share

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ShortID returns a 12-hex-char prefix of a UUID, suitable for short public
// URLs like /d/<short> (decoys) and /r/<short> (play results). 16^12 ≈
// 2.8 × 10^14 — collision risk is effectively zero at any reasonable volume.
//
// Storing this is unnecessary: it's a pure function of the UUID. The lookup
// queries in internal/db do a `LIKE '<short>%'` against id::text, which is
// O(n) at v1 volumes and gets a stored-column + index when tables grow.
func ShortID(id uuid.UUID) string {
	s := strings.ReplaceAll(id.String(), "-", "")
	return s[:12]
}

// DecoyShortID and PlayShortID are aliases kept for grep-ability so a reader
// of one call site immediately knows what kind of URL is being built.
func DecoyShortID(id uuid.UUID) string { return ShortID(id) }
func PlayShortID(id uuid.UUID) string  { return ShortID(id) }

// ParseShortID validates and lower-cases an incoming /d/<short> path segment.
// We accept either the 12-char prefix or the full 36-char UUID, so users
// pasting a copy-paste-mangled URL still resolves.
func ParseShortID(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch len(s) {
	case 12:
		if !isHex(s) {
			return "", errors.New("non-hex short id")
		}
		return s, nil
	case 36:
		// Full UUID — strip dashes and take the first 12 chars.
		stripped := strings.ReplaceAll(s, "-", "")
		if len(stripped) != 32 || !isHex(stripped) {
			return "", errors.New("malformed uuid")
		}
		return stripped[:12], nil
	default:
		return "", errors.New("bad short id length")
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
