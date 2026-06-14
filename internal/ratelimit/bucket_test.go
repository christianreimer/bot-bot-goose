package ratelimit

import "testing"

func TestAllowRejectsInvalidConfig(t *testing.T) {
	l := &Postgres{}
	if _, _, err := l.Allow(nil, "k", 0, 1); err == nil {
		t.Errorf("expected error for capacity=0")
	}
	if _, _, err := l.Allow(nil, "k", 5, 0); err == nil {
		t.Errorf("expected error for refill=0")
	}
}

// The behavioral tests (consume → deny → refill → allow) live in an
// integration suite that needs a real Postgres. We don't run those in
// `go test ./...` to keep the suite hermetic; see CONTRIBUTING.md.
