package share

import (
	"testing"

	"github.com/google/uuid"
)

func TestDecoyShortIDStableAndPrefix(t *testing.T) {
	id := uuid.MustParse("8ce1c63a-1234-5678-9abc-def012345678")
	got := DecoyShortID(id)
	if got != "8ce1c63a1234" {
		t.Errorf("got %q, want 8ce1c63a1234", got)
	}
}

func TestParseShortIDAcceptsPrefixAndFullUUID(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"8ce1c63a1234", "8ce1c63a1234", false},
		{"8CE1C63A1234", "8ce1c63a1234", false},
		{"8ce1c63a-1234-5678-9abc-def012345678", "8ce1c63a1234", false},
		{"", "", true},
		{"toolong-toolong-toolong", "", true},
		{"zzzzzzzzzzzz", "", true}, // non-hex
	}
	for _, c := range cases {
		got, err := ParseShortID(c.in)
		if c.err && err == nil {
			t.Errorf("ParseShortID(%q) expected error", c.in)
		}
		if !c.err && got != c.want {
			t.Errorf("ParseShortID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
