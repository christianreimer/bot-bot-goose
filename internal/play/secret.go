package play

import "crypto/rand"

// NewSecret returns 32 random bytes for use as a per-play HMAC secret.
func NewSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failure: " + err.Error())
	}
	return b
}
