// Package play owns the server-authoritative play loop.
//
// The single most consequential rule: answer labels never leave the server
// until a guess is committed. Two mechanisms enforce that:
//
//  1. The canonical answer order in puzzle_round_answers is by id (random
//     UUIDs), but every play_round generates its OWN slot_permutation. The
//     client only ever sees [slot 0..3] → text; it doesn't see the canonical
//     ordinal, and can't reconstruct it from another player's session.
//  2. Every state-changing call (hint, guess) carries an HMAC'd play token
//     bound to (play_id, round_index, slot_permutation_hash, issued_at).
//     The token is signed with the per-play `plays.hmac_secret`, so a
//     leaked global key doesn't compromise other plays.
package play

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// TokenMaxAge caps how long a freshly-issued token is honored. Players
	// who alt-tab for an hour have to re-fetch; this is the abandonment
	// window, not the round time budget.
	TokenMaxAge = 30 * time.Minute

	// SuspiciousFastGuessFloor is the minimum time between round-start and
	// guess-commit below which we flag (but do not reject) the guess. Per the
	// plan: "no hard reject (false positives ruin UX)".
	SuspiciousFastGuessFloor = 800 * time.Millisecond
)

// Token is the wire format: <play_id>.<round_index>.<perm_hash_hex>.<issued_unix>.<sig_b64>
// playID is a UUID string (contains dashes only, no dots).
type Token struct {
	PlayID     uuid.UUID
	RoundIndex int16
	PermHash   []byte // sha256 of the slot_permutation
	IssuedAt   time.Time
}

// PermutationHash is a deterministic digest of a slot permutation.
func PermutationHash(perm []int16) []byte {
	h := sha256.New()
	for _, v := range perm {
		_, _ = h.Write([]byte{byte(v >> 8), byte(v)})
	}
	return h.Sum(nil)
}

// Issue mints a token signed with the play's secret.
func Issue(secret []byte, playID uuid.UUID, roundIdx int16, perm []int16, now time.Time) string {
	permHash := PermutationHash(perm)
	payload := encodePayload(playID, roundIdx, permHash, now)
	sig := sign(secret, payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Verify parses + validates a token against the secret. It does not (and
// cannot) verify that the round is uncommitted or in order — those are
// database invariants checked by the commit code.
func Verify(secret []byte, raw string, now time.Time) (*Token, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 5 {
		return nil, errors.New("token: bad shape")
	}
	payload := strings.Join(parts[:4], ".")
	want := sign(secret, payload)
	// .Strict() enforces that the (4-bit) data quanta in the last base64
	// char have their two unused low bits set to zero. Without Strict the
	// decoder accepts non-canonical encodings — including ones produced by
	// flipping those padding bits — and Verify would mistakenly accept a
	// "tampered" token whose decoded HMAC happens to match. Belt-and-
	// suspenders: the HMAC equality check would still catch a real forgery,
	// but rejecting non-canonical encodings up front means there's a single
	// canonical wire shape for a given signature.
	got, err := base64.RawURLEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return nil, errors.New("token: bad sig encoding")
	}
	if !hmac.Equal(want, got) {
		return nil, errors.New("token: signature mismatch")
	}

	playID, err := uuid.Parse(parts[0])
	if err != nil {
		return nil, fmt.Errorf("token: bad play_id: %w", err)
	}
	idx, err := strconv.Atoi(parts[1])
	if err != nil || idx < 0 || idx > 32767 {
		return nil, errors.New("token: bad round_index")
	}
	permHash, err := hex.DecodeString(parts[2])
	if err != nil || len(permHash) != 32 {
		return nil, errors.New("token: bad perm_hash")
	}
	issuedUnix, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, errors.New("token: bad issued_at")
	}
	issued := time.Unix(issuedUnix, 0)
	if now.Sub(issued) > TokenMaxAge {
		return nil, errors.New("token: expired")
	}
	if issued.After(now.Add(2 * time.Minute)) {
		return nil, errors.New("token: issued in the future")
	}
	return &Token{
		PlayID:     playID,
		RoundIndex: int16(idx),
		PermHash:   permHash,
		IssuedAt:   issued,
	}, nil
}

func encodePayload(playID uuid.UUID, roundIdx int16, permHash []byte, now time.Time) string {
	return fmt.Sprintf("%s.%d.%s.%d", playID, roundIdx, hex.EncodeToString(permHash), now.Unix())
}

func sign(secret []byte, payload string) []byte {
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write([]byte(payload))
	return m.Sum(nil)
}
