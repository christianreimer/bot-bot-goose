package play

import (
	"sort"
	"testing"
)

func TestNewPermutationIsValid(t *testing.T) {
	for _, n := range []int{2, 4, 6, 16, 64} {
		p := NewPermutation(n)
		if len(p) != n {
			t.Fatalf("len(%d) = %d", n, len(p))
		}
		c := append([]int16(nil), p...)
		sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
		for i, v := range c {
			if int(v) != i {
				t.Fatalf("permutation %v missing %d", p, i)
			}
		}
	}
}

func TestNewPermutationDistribution(t *testing.T) {
	// Cheap statistical check: across 4000 trials of n=4, every position
	// should be hit by every value somewhere between 800 and 1200 times.
	const trials = 4000
	var counts [4][4]int
	for i := 0; i < trials; i++ {
		p := NewPermutation(4)
		for slot, ord := range p {
			counts[slot][ord]++
		}
	}
	for slot := 0; slot < 4; slot++ {
		for ord := 0; ord < 4; ord++ {
			if counts[slot][ord] < 700 || counts[slot][ord] > 1300 {
				t.Errorf("slot %d ord %d count out of band: %d", slot, ord, counts[slot][ord])
			}
		}
	}
}
