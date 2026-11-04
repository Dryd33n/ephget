package ttl

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]time.Duration{"1h": time.Hour, "36h": 36 * time.Hour, "7d": 7 * 24 * time.Hour} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "h", "0h", "0d", "1m", "30m", "1", "-1h", "+1h", "1.5d", "1 d", "99999999999999999d"} {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, got)
		}
	}
}
