package ui

import (
	"os"
	"runtime"
	"strings"
)

// Screen-safe glyphs (v0.8.0). Hermes prints symbols the console fonts of
// conhost (Consolas, Lucida Console: the WGL4 character set) do not have, and
// they show as an empty box: the diamond of "◆ Pre-update snapshot", the
// warning sign, hourglass, and so on. The marks of marks() (check, cross,
// warning, info, caduceus) get the glyphs of the theme; this file covers the
// rest. Display only: the log keeps the raw text.
//
// hermesGlyphs maps every symbol that the Hermes source prints in update,
// check, plugin, skill and pm output (grep of hermes_cli, 2026-11-02) and is
// not in WGL4 to one that is. Anything else outside WGL4 (an emoji, a glyph a
// later Hermes adds) falls back to glyphFallback, so a box never reaches the
// screen. Every target here must itself be renderable (test).
var hermesGlyphs = map[rune]string{
	'◆': "♦", // U+25C6 "◆ Pre-update snapshot", "◆ Session database ..."
	'◇': "◊",
	'⇒': "→",
	'↷': "→",
	'↪': "→",
	'↻': "~",
	'⟲': "~",
	'⏳': "…",
	'★': "*",
	'⚡': "*",
	'⊘': "-",
	'▶': "►",
	'⏸': "||",
	'▸': "►",
	'✅': "√",
	'❌': "×",
	'🔌': "+",
	'‐': "-", // U+2010
	'‑': "-", // U+2011
}

// glyphFallback stands in for any other character outside the allowlist.
const glyphFallback = "?"

// renderable reports whether the WGL4 set (Consolas, Lucida Console, the
// Windows console raster fonts) has the character, or it needs no glyph.
func renderable(r rune) bool {
	if r < 0x7f { // ASCII and control characters (tab, escape sequences)
		return true
	}
	for _, rg := range wgl4 {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// wgl4 is the Windows Glyph List 4 minus what is only ASCII: inclusive ranges.
var wgl4 = [][2]rune{
	{0xA0, 0x17F}, {0x192, 0x192}, {0x1FA, 0x1FF}, {0x2C6, 0x2C7}, {0x2C9, 0x2C9}, {0x2D8, 0x2DD},
	{0x384, 0x38A}, {0x38C, 0x38C}, {0x38E, 0x3A1}, {0x3A3, 0x3CE}, {0x401, 0x45C}, {0x45E, 0x45F}, {0x490, 0x491},
	{0x1E80, 0x1E85}, {0x1EF2, 0x1EF3},
	{0x2013, 0x2015}, {0x2017, 0x201E}, {0x2020, 0x2022}, {0x2026, 0x2026}, {0x2030, 0x2030},
	{0x2032, 0x2033}, {0x2039, 0x203A}, {0x203C, 0x203C}, {0x203E, 0x203E}, {0x2044, 0x2044},
	{0x207F, 0x207F}, {0x20A3, 0x20A4}, {0x20A7, 0x20A7}, {0x20AC, 0x20AC},
	{0x2105, 0x2105}, {0x2113, 0x2113}, {0x2116, 0x2116}, {0x2122, 0x2122}, {0x2126, 0x2126}, {0x212E, 0x212E},
	{0x215B, 0x215E}, {0x2190, 0x2195}, {0x21A8, 0x21A8},
	{0x2202, 0x2202}, {0x2206, 0x2206}, {0x220F, 0x220F}, {0x2211, 0x2212}, {0x2215, 0x2215}, {0x2219, 0x221A},
	{0x221E, 0x221F}, {0x2229, 0x2229}, {0x222B, 0x222B}, {0x2248, 0x2248}, {0x2260, 0x2261}, {0x2264, 0x2265},
	{0x2302, 0x2302}, {0x2310, 0x2310}, {0x2320, 0x2321},
	{0x2500, 0x2500}, {0x2502, 0x2502}, {0x250C, 0x250C}, {0x2510, 0x2510}, {0x2514, 0x2514}, {0x2518, 0x2518},
	{0x251C, 0x251C}, {0x2524, 0x2524}, {0x252C, 0x252C}, {0x2534, 0x2534}, {0x253C, 0x253C}, {0x2550, 0x256C},
	{0x2580, 0x2580}, {0x2584, 0x2584}, {0x2588, 0x2588}, {0x258C, 0x258C}, {0x2590, 0x2593},
	{0x25A0, 0x25A1}, {0x25AA, 0x25AC}, {0x25B2, 0x25B2}, {0x25BA, 0x25BA}, {0x25BC, 0x25BC}, {0x25C4, 0x25C4},
	{0x25CA, 0x25CB}, {0x25CF, 0x25CF}, {0x25D8, 0x25D9}, {0x25E6, 0x25E6},
	{0x263A, 0x263C}, {0x2640, 0x2640}, {0x2642, 0x2642}, {0x2660, 0x2660}, {0x2663, 0x2663}, {0x2665, 0x2666},
	{0x266A, 0x266B},
}

// screenSafe rewrites s so that every character is one the console font has.
// keep lists extra characters to leave alone (the theme's own glyphs, and the
// marks that remark() turns into theme glyphs afterwards); nil = none.
func screenSafe(s string, keep func(rune) bool) string {
	return screenSafeAt(s, keep, 0)
}

// classicConhost reports whether the text goes to the classic Windows console
// host (conhost with a raster/Consolas font), the only place the WGL4
// allowlist is needed. Windows Terminal (WT_SESSION), ConEmu/cmder
// (ConEmuPID), VS Code and other terminal programs (TERM_PROGRAM), and any
// non-Windows system draw CJK, emoji and symbols with font fallback.
func classicConhost(getenv func(string) string, goos string) bool {
	if goos != "windows" {
		return false
	}
	for _, k := range []string{"WT_SESSION", "TERM_PROGRAM", "ConEmuPID", "ConEmuBuild", "ANSICON"} {
		if getenv(k) != "" {
			return false
		}
	}
	return true
}

// allowlistFallback says whether characters outside WGL4 are replaced
// (a variable so tests can force either path).
var allowlistFallback = func() bool { return classicConhost(os.Getenv, runtime.GOOS) }

// screenSafeAt is screenSafe for text that will start at column col0 (the
// gutter of a passthrough line): tab stops count from the screen column and
// CSI escape sequences take no column.
func screenSafeAt(s string, keep func(rune) bool, col0 int) string {
	fallback := allowlistFallback()
	clean := true
	for _, r := range s {
		if r == '\t' || r == '\r' || fallback && !renderable(r) && (keep == nil || !keep(r)) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	col := col0 // columns on screen so far: tabs go to the next stop of 8
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == 0x1b && i+1 < len(rs) && rs[i+1] == '[' { // CSI: parameters, then a final byte 0x40-0x7e
			j := i + 2
			for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x3f {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(string(rs[i:j]))
			i = j - 1
			continue
		}
		col++
		switch {
		case r == '\n':
			col = col0
			b.WriteRune(r)
		case r == '\t':
			// N11: a tab is one rune but several columns; the wrapper counts runes
			n := 8 - (col-1)%8
			b.WriteString(strings.Repeat(" ", n))
			col += n - 1
		case r == '\r':
			// a carriage return in the middle of a line: the console would overwrite it from column 0
			b.Reset()
			col = col0
		case !fallback || renderable(r) || (keep != nil && keep(r)):
			b.WriteRune(r)
		case r == 0xFE0F || r == 0xFE0E || r == 0x200B || r == 0x200D: // variation selectors and zero-width joiners draw nothing
		case hermesGlyphs[r] != "":
			b.WriteString(hermesGlyphs[r])
		case r == 0x2800: // blank braille cell
			b.WriteByte(0x20)
		case r > 0x2800 && r <= 0x28FF: // braille art
			b.WriteByte('*')
		default:
			b.WriteString(glyphFallback)
		}
	}
	return b.String()
}

// keepGlyphs is the keep function of screenSafe for this theme: its own glyphs
// stay as the user configured them, and so do the first runes of the marks.
func (v *VT) keepGlyphs() func(rune) bool {
	own := map[rune]bool{}
	add := func(s string) {
		for _, r := range s {
			own[r] = true
		}
	}
	g := v.th.Glyphs
	for _, s := range []string{g.OK, g.Err, g.Warn, g.Info, g.Command, g.Active, g.Pending, g.Dot, g.Bar, g.Rule, g.RuleThin, g.Sep, g.Gutter, g.Arrow, g.Title, g.Section} {
		add(s)
	}
	for _, h := range v.th.Hues {
		add(h.Icon)
	}
	for _, s := range v.th.SectionIcons {
		add(s)
	}
	for _, m := range v.marks() {
		for _, r := range m.src {
			own[r] = true
		}
	}
	return func(r rune) bool { return own[r] }
}
