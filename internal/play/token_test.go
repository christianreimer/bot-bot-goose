package play

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIssueVerifyRoundtrip(t *testing.T) {
	secret := []byte("super-secret-secret-secret-32bts")
	playID := uuid.New()
	perm := []int16{2, 0, 3, 1}
	now := time.Unix(1_700_000_000, 0)

	tok := Issue(secret, playID, 1, perm, now)
	out, err := Verify(secret, tok, now)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if out.PlayID != playID {
		t.Errorf("play_id mismatch")
	}
	if out.RoundIndex != 1 {
		t.Errorf("round mismatch: %d", out.RoundIndex)
	}
	if !bytes.Equal(out.PermHash, PermutationHash(perm)) {
		t.Errorf("perm hash mismatch")
	}
	if !out.IssuedAt.Equal(now) {
		t.Errorf("issued_at mismatch: %v != %v", out.IssuedAt, now)
	}
}

func TestVerifyRejectsTamperedSig(t *testing.T) {
	secret := []byte("super-secret-secret-secret-32bts")
	playID := uuid.New()
	perm := []int16{0, 1, 2, 3}
	now := time.Unix(1_700_000_000, 0)

	tok := Issue(secret, playID, 0, perm, now)
	// Flip the last char of the signature.
	tampered := tok[:len(tok)-1] + string([]byte{tok[len(tok)-1] ^ 1})
	if _, err := Verify(secret, tampered, now); err == nil {
		t.Fatal("verify accepted tampered token")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	playID := uuid.New()
	perm := []int16{0, 1, 2, 3}
	now := time.Unix(1_700_000_000, 0)

	tok := Issue([]byte("aaa"), playID, 0, perm, now)
	if _, err := Verify([]byte("bbb"), tok, now); err == nil {
		t.Fatal("verify accepted token signed with different secret")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	secret := []byte("super-secret-secret-secret-32bts")
	playID := uuid.New()
	perm := []int16{0, 1, 2, 3}
	issued := time.Unix(1_700_000_000, 0)

	tok := Issue(secret, playID, 0, perm, issued)
	later := issued.Add(TokenMaxAge + time.Second)
	if _, err := Verify(secret, tok, later); err == nil {
		t.Fatal("verify accepted expired token")
	}
}

func TestVerifyRejectsFutureIssued(t *testing.T) {
	secret := []byte("super-secret-secret-secret-32bts")
	playID := uuid.New()
	perm := []int16{0, 1, 2, 3}
	now := time.Unix(1_700_000_000, 0)

	// Forge a token that claims it was issued 5 minutes from now.
	tok := Issue(secret, playID, 0, perm, now.Add(5*time.Minute))
	if _, err := Verify(secret, tok, now); err == nil {
		t.Fatal("verify accepted future-issued token (>2min skew)")
	}
}

func TestPermutationHashStable(t *testing.T) {
	p := []int16{3, 0, 1, 2}
	a := PermutationHash(p)
	b := PermutationHash(p)
	if !bytes.Equal(a, b) {
		t.Errorf("perm hash unstable")
	}
	c := PermutationHash([]int16{3, 0, 2, 1})
	if bytes.Equal(a, c) {
		t.Errorf("different perms must hash differently")
	}
}
