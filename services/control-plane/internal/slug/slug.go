// Package slug derives URL-safe identifiers from display names.
package slug

import (
	"fmt"
	"strings"
	"unicode"
)

// Make lowercases, replaces non-alphanumerics with dashes, and collapses runs.
func Make(name string) string {
	var b strings.Builder
	lastDash := true // suppress leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "untitled"
	}
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// WithSuffix returns "base" for attempt 0, "base-2" for attempt 1, and so on —
// used by stores to resolve unique-slug collisions with a bounded retry loop.
func WithSuffix(base string, attempt int) string {
	if attempt == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, attempt+1)
}
