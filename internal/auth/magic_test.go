package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestIssueParseRoundtrip(t *testing.T) {
	key := []byte("a-real-32-byte-server-side-key!!")
	token, hash := IssueMagicToken(key, "alice@example.com")

	gotEmail, gotHash, err := ParseMagicToken(key, token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gotEmail != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", gotEmail)
	}
	if !bytes.Equal(gotHash, hash) {
		t.Errorf("hash mismatch on roundtrip")
	}
	if !bytes.Equal(HashTokenForLookup(token), hash) {
		t.Errorf("HashTokenForLookup mismatch")
	}
}

func TestParseRejectsTampered(t *testing.T) {
	key := []byte("a-real-32-byte-server-side-key!!")
	token, _ := IssueMagicToken(key, "alice@example.com")
	if len(token) < 60 {
		t.Fatal("token too short for tamper test")
	}

	// Tamper a middle base64 character — guaranteed to land inside the
	// signed region (salt or sig or payload), not in the trailing
	// zero-bit padding that base64.RawURLEncoding may ignore.
	mid := len(token) / 2
	bad := []byte(token)
	// Replace with a distinct character from the base64url alphabet so the
	// decode itself succeeds but the bytes are different.
	if bad[mid] == 'A' {
		bad[mid] = 'B'
	} else {
		bad[mid] = 'A'
	}
	if _, _, err := ParseMagicToken(key, string(bad)); err == nil {
		t.Errorf("parse accepted a tampered token")
	}
}

func TestParseRejectsTamperedPayloadEnd(t *testing.T) {
	// Stronger: extend the token with garbage. The original sig only
	// covered the original bytes; appending should break HMAC.
	key := []byte("a-real-32-byte-server-side-key!!")
	token, _ := IssueMagicToken(key, "alice@example.com")
	extended := token + "AA"
	if _, _, err := ParseMagicToken(key, extended); err == nil {
		t.Errorf("parse accepted an extended token")
	}
}

func TestParseRejectsWrongKey(t *testing.T) {
	token, _ := IssueMagicToken([]byte("key-A"), "alice@example.com")
	if _, _, err := ParseMagicToken([]byte("key-B"), token); err == nil {
		t.Errorf("parse accepted a token signed with a different key")
	}
}

func TestIssueIsRandomAcrossCalls(t *testing.T) {
	// Same email, same key — different salts → different tokens.
	key := []byte("a-real-32-byte-server-side-key!!")
	a, _ := IssueMagicToken(key, "alice@example.com")
	b, _ := IssueMagicToken(key, "alice@example.com")
	if a == b {
		t.Errorf("two issued tokens for the same email must differ")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	key := []byte("k")
	cases := []string{"", "a", "abcdef", strings.Repeat("z", 5)}
	for _, c := range cases {
		if _, _, err := ParseMagicToken(key, c); err == nil {
			t.Errorf("ParseMagicToken(%q) accepted garbage", c)
		}
	}
}
