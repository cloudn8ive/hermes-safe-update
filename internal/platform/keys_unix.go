package platform

// Pure helpers for the macOS/Linux console, progress and disk backends.
// No build constraint so they are tested on every OS.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// parseKey turns the bytes of one raw-mode read into a Key (D3: only
// printable keys, Enter, Esc and Ctrl+C mean something; every escape
// sequence, arrow or function key is Other).
func parseKey(b []byte) Key {
	if len(b) == 0 {
		return Key{Other: true}
	}
	switch {
	case len(b) == 1 && b[0] == 0x03:
		return Key{CtrlC: true}
	case len(b) == 1 && (b[0] == '\r' || b[0] == '\n'):
		return Key{Rune: '\r'}
	case len(b) == 1 && b[0] == 0x1b:
		return Key{Rune: 0x1b}
	case b[0] == 0x1b:
		return Key{Other: true}
	}
	r, _ := utf8.DecodeRune(b)
	if r == utf8.RuneError || r < 0x20 || r == 0x7f {
		return Key{Other: true}
	}
	return Key{Rune: []rune(strings.ToLower(string(r)))[0]}
}

// oscProgress renders the OSC 9;4 progress sequence (ConEmu/Windows
// Terminal convention; other terminals may ignore it).
func oscProgress(state ProgressState, fraction float64) string {
	st := map[ProgressState]int{
		ProgressNone: 0, ProgressNormal: 1, ProgressError: 2,
		ProgressIndeterminate: 3, ProgressPaused: 4,
	}[state]
	pct := int(fraction*100 + 0.5)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if state == ProgressNone || state == ProgressIndeterminate {
		pct = 0
	}
	return fmt.Sprintf("\x1b]9;4;%d;%d\a", st, pct)
}

// parseMounts returns the mount points of local block-device filesystems in
// /proc/mounts text, in order, without duplicates or loop devices.
func parseMounts(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[0], "/dev/") || strings.HasPrefix(f[0], "/dev/loop") {
			continue
		}
		mp := unescapeMount(f[1])
		if !seen[mp] {
			seen[mp] = true
			out = append(out, mp)
		}
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 = space) of /proc/mounts.
func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var n int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%03o", &n); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
