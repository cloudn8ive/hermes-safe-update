package config

import (
	"regexp"
	"strings"
)

var (
	reDollar  = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	rePercent = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_()]*)%`)
)

// ExpandPath expands a leading `~` (current user only), `$VAR`, `${VAR}` and
// `%VAR%` in a path from the config. It is applied only to fields documented
// as paths, so a `$` elsewhere is never touched. Unset variables are left as
// written, so the resulting "not found" error shows what the user typed.
func ExpandPath(p, home string, getenv func(string) string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = home + p[1:]
	}
	p = reDollar.ReplaceAllStringFunc(p, func(m string) string {
		sub := reDollar.FindStringSubmatch(m)
		name := sub[1]
		if name == "" {
			name = sub[2]
		}
		if v := getenv(name); v != "" {
			return v
		}
		return m
	})
	return rePercent.ReplaceAllStringFunc(p, func(m string) string {
		if v := getenv(m[1 : len(m)-1]); v != "" {
			return v
		}
		return m
	})
}
