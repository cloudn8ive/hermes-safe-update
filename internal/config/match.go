package config

import (
	"errors"
	"fmt"
	"strings"
)

// MatchKey reports whether a localStorage key matches a pattern from
// settings.keep_new / settings.union. The only wildcard is `*`, which
// matches any run of characters (including none); everything else is
// literal. Example: "hermes.desktop.lastRoute.profile.*" matches the key of
// every profile (DECISIONS D8).
func MatchKey(pattern, key string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == key
	}
	if !strings.HasPrefix(key, parts[0]) {
		return false
	}
	key = key[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(key, p)
		if i < 0 {
			return false
		}
		key = key[i+len(p):]
	}
	return strings.HasSuffix(key, last)
}

// MatchAny reports whether key matches any of the patterns.
func MatchAny(patterns []string, key string) bool {
	for _, p := range patterns {
		if MatchKey(p, key) {
			return true
		}
	}
	return false
}

// ValidatePattern rejects empty patterns and glob syntax other than `*`, so
// a user who writes `[abc]` or `?` is told instead of silently matching
// nothing.
func ValidatePattern(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("empty pattern")
	}
	if i := strings.IndexAny(p, "[]?\\"); i >= 0 {
		return fmt.Errorf("pattern %q: only * is supported as a wildcard (found %q)", p, p[i])
	}
	return nil
}
