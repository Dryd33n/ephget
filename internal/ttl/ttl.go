// Package ttl parses time-to-live values such as "1h" and "7d".
package ttl

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse accepts a whole number of hours or days, at least one hour.
func Parse(s string) (time.Duration, error) {
	invalid := fmt.Errorf("invalid TTL %q: use hours or days, such as 12h or 7d, with a minimum of 1h", s)
	if len(s) < 2 {
		return 0, invalid
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	default:
		return 0, invalid
	}
	digits := s[:len(s)-1]
	if strings.Trim(digits, "0123456789") != "" {
		return 0, invalid
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 1 || n > int64(1<<63-1)/int64(unit) {
		return 0, invalid
	}
	return time.Duration(n) * unit, nil
}
