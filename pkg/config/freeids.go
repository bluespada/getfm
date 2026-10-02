package config

import (
	"fmt"
	"regexp"
	"strings"
)

// compileFreeID turns one declared free id into a matcher. Entries without a
// wildcard are compared case-insensitively for speed; the rest become a regexp.
func compileFreeID(pattern string) (func(string) bool, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty free id")
	}
	if !strings.ContainsAny(pattern, "*?") {
		want := strings.ToLower(pattern)
		return func(s string) bool { return strings.ToLower(s) == want }, nil
	}

	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	return re.MatchString, nil
}
