package ui

import (
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

const (
	esc   = "\x1b["
	reset = esc + "0m"
	bold  = "1"
)

// seg is one run of text with SGR codes (python-behaviour §5.2 "segments").
type seg struct {
	t     string
	codes []string
}

// style turns theme colours into escape sequences. With color=false (NO_COLOR)
// every colour parameter is dropped and only bold survives.
type style struct {
	th    *config.Theme
	color bool
}

func newStyle(th *config.Theme, color bool) *style { return &style{th: th, color: color} }

// stripColour removes colour parameters (38;5;n, 38;2;r;g;b, 48;...) from an
// SGR parameter string and returns what is left ("" if nothing).
func stripColour(code string) string {
	toks := strings.Split(code, ";")
	var keep []string
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "38", "48":
			if i+1 < len(toks) {
				switch toks[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		case "":
		default:
			keep = append(keep, toks[i])
		}
	}
	return strings.Join(keep, ";")
}

// sgr wraps text in one SGR sequence made of codes (empty codes are skipped).
func (s *style) sgr(text string, codes ...string) string {
	var use []string
	for _, c := range codes {
		if !s.color {
			c = stripColour(c)
		}
		if c != "" {
			use = append(use, c)
		}
	}
	if len(use) == 0 {
		return text
	}
	return esc + strings.Join(use, ";") + "m" + text + reset
}

// hue returns the SGR colour of a stage group (bright or dark); an unknown
// group gets the Check hue (80/30), as in the Python UI.
func (s *style) hue(group string, dark bool) string {
	h, ok := s.th.Hues[group]
	if !ok {
		h = config.Hue{Bright: 80, Dark: 30}
	}
	n := h.Bright
	if dark {
		n = h.Dark
	}
	return "38;5;" + itoa(n)
}

func (s *style) icon(group string) string {
	if h, ok := s.th.Hues[group]; ok && h.Icon != "" {
		return h.Icon
	}
	return s.th.Glyphs.Active
}

// barColour is the gradient colour of the band starting at cell i of n.
func (s *style) barColour(i, n int) string {
	t := float64(i) / math.Max(1, float64(n-1))
	var c [3]int
	for k := range c {
		a, z := float64(s.th.BarFrom[k]), float64(s.th.BarTo[k])
		c[k] = int(math.RoundToEven(a + (z-a)*t))
	}
	return "38;2;" + itoa(c[0]) + ";" + itoa(c[1]) + ";" + itoa(c[2])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// fit joins segments, cut to width visible columns (never wrapped).
func (s *style) fit(segs []seg, width int) string {
	var b strings.Builder
	used := 0
	for _, g := range segs {
		if used >= width {
			break
		}
		piece := g.t
		if n := utf8.RuneCountInString(piece); n > width-used {
			piece = string([]rune(piece)[:width-used])
		}
		if piece == "" {
			continue
		}
		used += utf8.RuneCountInString(piece)
		b.WriteString(s.sgr(piece, g.codes...))
	}
	return b.String()
}

// row is left segments, a filler of at least one column, then right
// segments ending exactly at column width.
func (s *style) row(left, right []seg, width int, fill string, fillCodes []string) string {
	used := 0
	for _, g := range left {
		used += utf8.RuneCountInString(g.t)
	}
	for _, g := range right {
		used += utf8.RuneCountInString(g.t)
	}
	n := width - used
	if n < 1 {
		n = 1
	}
	all := append(append(append([]seg{}, left...), seg{t: strings.Repeat(fill, n), codes: fillCodes}), right...)
	return s.fit(all, width)
}

// visibleLen is the number of columns a string occupies: runes minus CSI
// escape sequences (colour and cursor codes have zero width).
func visibleLen(str string) int {
	n := 0
	rs := []rune(str)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			i += 2
			for i < len(rs) && !(rs[i] >= 0x40 && rs[i] <= 0x7e) {
				i++
			}
			continue
		}
		n++
	}
	return n
}

// fmtDur is m:ss, or h:mm:ss from one hour (Python round(): half to even).
func fmtDur(sec float64) string {
	s := int(math.RoundToEven(sec))
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return itoa(s/3600) + ":" + pad2(s%3600/60) + ":" + pad2(s%60)
	}
	return itoa(s/60) + ":" + pad2(s%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func fmtLeft(sec float64) string {
	if sec < 45 {
		return "under a minute left"
	}
	m := int(math.RoundToEven(sec / 60))
	if m < 1 {
		m = 1
	}
	return "about " + itoa(m) + " min left"
}

// Clocks and dates are fixed English with no leading zero (D12); the layouts
// do not depend on the system locale.
func fmtClock(t time.Time) string    { return t.Format("3:04 PM") }
func fmtClockSec(t time.Time) string { return t.Format("3:04:05 PM") }
func fmtDate(t time.Time) string     { return t.Format("Mon 2 Jan") }

// wrapWords wraps text at width columns like Python's textwrap.wrap: words
// separated by single spaces, over-long words broken to fill lines.
func wrapWords(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	cur := ""
	curLen := 0
	flush := func() {
		lines = append(lines, cur)
		cur, curLen = "", 0
	}
	for _, w := range strings.Fields(text) {
		wr := []rune(w)
		sep := 0
		if curLen > 0 {
			sep = 1
		}
		if curLen+sep+len(wr) <= width {
			if sep == 1 {
				cur += " "
			}
			cur += w
			curLen += sep + len(wr)
			continue
		}
		if len(wr) <= width {
			flush()
			cur, curLen = w, len(wr)
			continue
		}
		// word longer than a whole line: fill the rest of this line, then cut
		if left := width - curLen - sep; curLen > 0 && left > 0 {
			cur += " " + string(wr[:left])
			wr = wr[left:]
		}
		if curLen > 0 || cur != "" {
			flush()
		}
		for len(wr) > width {
			lines = append(lines, string(wr[:width]))
			wr = wr[width:]
		}
		cur, curLen = string(wr), len(wr)
	}
	if cur != "" {
		flush()
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// truncRunes cuts s to n runes.
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// padRight pads s with spaces to n runes (never truncates).
func padRight(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// padLeft right-justifies s in n columns (never truncates).
func padLeft(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// ---- wrapping

// cell is one visible column of a styled line: the rune, the SGR codes in
// force at that rune (concatenated escape sequences, "" = none) and any
// non-colour escape sequence written just before it. Display width is one
// column per rune, the same rule fit, row and visibleLen use.
type cell struct {
	r     rune
	state string
	pre   string
}

// parseCells splits a styled line into cells. SGR sequences update the state
// (a reset clears it) and never take a column.
func parseCells(s string) []cell {
	var cells []cell
	state, pre := "", ""
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			j := i + 2
			for j < len(rs) && !(rs[j] >= 0x40 && rs[j] <= 0x7e) {
				j++
			}
			if j >= len(rs) {
				break // a cut-off sequence carries no text
			}
			seq := string(rs[i : j+1])
			switch params := string(rs[i+2 : j]); {
			case rs[j] != 'm':
				pre += seq
			case params == "" || params == "0":
				state = ""
			default:
				state += seq
			}
			i = j
			continue
		}
		cells = append(cells, cell{r: rs[i], state: state, pre: pre})
		pre = ""
	}
	return cells
}

// renderCells writes cells back as one line: each run of equal state gets
// its escape sequences once, and a run that is still open at the end of the
// line is closed, so a line break never falls inside a colour.
func renderCells(cells []cell) string {
	var b strings.Builder
	cur := ""
	for _, c := range cells {
		if c.state != cur {
			if cur != "" {
				b.WriteString(reset)
			}
			b.WriteString(c.state)
			cur = c.state
		}
		b.WriteString(c.pre)
		b.WriteRune(c.r)
	}
	if cur != "" {
		b.WriteString(reset)
	}
	return b.String()
}

// minWrapText is the least text width a continuation line keeps; a hanging
// indent wider than width-minWrapText is shortened to fit.
const minWrapText = 12

// wrapStyled wraps one styled line (SGR codes are zero width and are never
// split) so no line is wider than width visible columns. Continuation lines
// start with hang (a styled prefix, e.g. the gutter plus spaces); the first
// line is cut only at spaces from visible column hangW on, so the margin,
// gutter and label before the text never count as a break point. Words are
// broken at spaces; a word longer than a whole line is cut hard. A line that
// fits is returned unchanged, byte for byte; width <= 0 turns wrapping off.
// Embedded newlines are kept and every part is wrapped alone.
func wrapStyled(line string, width int, hang string, hangW int) string {
	if width <= 0 {
		return line
	}
	if strings.Contains(line, "\n") {
		parts := strings.Split(line, "\n")
		for i, p := range parts {
			parts[i] = wrapStyled(p, width, hang, hangW)
		}
		return strings.Join(parts, "\n")
	}
	if visibleLen(line) <= width {
		return line
	}
	minBreak := hangW
	if hangW > width-minWrapText {
		hangW = maxInt(0, width-minWrapText)
		hang = strings.Repeat(" ", hangW)
	}
	cells := parseCells(line)
	var out []string
	i, avail, first := 0, width, true
	for i < len(cells) {
		if len(cells)-i <= avail {
			out = append(out, hangIf(!first, hang)+renderCells(cells[i:]))
			break
		}
		// the first printable cell the line may break after
		start := i
		if first {
			start = minBreak
		}
		for start < len(cells) && cells[start].r == ' ' {
			start++
		}
		end, next := i+avail, i+avail // hard cut
		for j := i + avail; j > start && j > i; j-- {
			if cells[j].r == ' ' {
				end, next = j, j
				break
			}
		}
		for next < len(cells) && cells[next].r == ' ' {
			next++
		}
		seg := cells[i:end]
		for len(seg) > 0 && seg[len(seg)-1].r == ' ' {
			seg = seg[:len(seg)-1]
		}
		out = append(out, hangIf(!first, hang)+renderCells(seg))
		i = next
		if first {
			first, avail = false, width-hangW
		}
	}
	return strings.Join(out, "\n")
}

func hangIf(on bool, hang string) string {
	if on {
		return hang
	}
	return ""
}

// spaces is n blank columns (never negative).
func spaces(n int) string { return strings.Repeat(" ", maxInt(0, n)) }

// join concatenates segments uncut; fit cuts, join does not.
func (s *style) join(segs []seg) string {
	var b strings.Builder
	for _, g := range segs {
		if g.t != "" {
			b.WriteString(s.sgr(g.t, g.codes...))
		}
	}
	return b.String()
}
