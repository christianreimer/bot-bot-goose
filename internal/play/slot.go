package play

import (
	"crypto/rand"
	"encoding/binary"
)

// NewPermutation returns a fresh cryptographically-random permutation of
// [0,n). It is what defeats datamining: every play_round gets its own.
func NewPermutation(n int) []int16 {
	perm := make([]int16, n)
	for i := range perm {
		perm[i] = int16(i)
	}
	// Fisher-Yates using crypto/rand for the swap index.
	for i := n - 1; i > 0; i-- {
		j := int(randUint32() % uint32(i+1))
		perm[i], perm[j] = perm[j], perm[i]
	}
	return perm
}

func randUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand only fails if the OS rng is broken — fail loudly.
		panic("crypto/rand failure: " + err.Error())
	}
	return binary.BigEndian.Uint32(b[:])
}
