package ui

import (
	"regexp"
	"strings"
	"unicode"
)

// Key/value rows, verdict colours and token painting (python-behaviour §5.7).

var (
	badRe   = regexp.MustCompile(`(?i)\b(fail|error|dirty|missing|not found|refused|denied|timed out|NOT updated|too little)\b`)
	calmRe  = regexp.MustCompile(`(?i)\bnone active\b|^0 active|^nothing|^none\b|up to date`)
	attnRe  = regexp.MustCompile(`(?i)\b(\d+ new commit|will be reinstalled|stale|changed|waiting|still in use|\d+ active\b)`)
	goodRe  = regexp.MustCompile(`(?i)^(clean|current|unchanged|none|nothing|ok|ready|verified|running the new code|already)|build verified|already points|logon task current|\(enough\)|\bready\b|up to date|√`)
	tokenRe = regexp.MustCompile(`\b(v\d+\.\d+[\w.+-]*|[0-9a-f]{7,40}|\d{8}_\d{6}_[0-9a-f]{6})\b`)
)

// numberUnits in the order Python's alternation tried them.
var numberUnits = []string{"GB", "MB", "min", "s", "h"}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func wordOrDot(r rune) bool { return r == '.' || isWordRune(r) }

// numberAt returns the end index (in runes) of a number with an optional unit
// starting at rs[i], or -1. It follows the Python regex
// (?<![\w.])(\d+(?:\.\d+)?(?: ?(?:GB|MB|min|s|h))?)(?![\w.]) including its
// backtracking order; Go's RE2 has no lookaround, so it is done by hand.
func numberAt(rs []rune, i int) int {
	if !isDigit(rs[i]) || (i > 0 && wordOrDot(rs[i-1])) {
		return -1
	}
	j := i
	for j < len(rs) && isDigit(rs[j]) {
		j++
	}
	ends := []int{j}
	if j+1 < len(rs) && rs[j] == '.' && isDigit(rs[j+1]) {
		k := j + 1
		for k < len(rs) && isDigit(rs[k]) {
			k++
		}
		ends = []int{k, j}
	}
	for _, e := range ends {
		cands := []int{}
		for _, sp := range []int{1, 0} {
			p := e
			if sp == 1 {
				if p >= len(rs) || rs[p] != ' ' {
					continue
				}
				p++
			}
			for _, u := range numberUnits {
				ur := []rune(u)
				if p+len(ur) <= len(rs) && string(rs[p:p+len(ur)]) == u {
					cands = append(cands, p+len(ur))
				}
			}
		}
		cands = append(cands, e)
		for _, end := range cands {
			if end >= len(rs) || !wordOrDot(rs[end]) {
				return end
			}
		}
	}
	return -1
}

// paintNumbers makes numbers (with units) bold over the base colour.
func (s *style) paintNumbers(text string, base []string) string {
	rs := []rune(text)
	var b strings.Builder
	pos := 0
	plain := func(from, to int) {
		if to > from {
			b.WriteString(s.sgr(string(rs[from:to]), base...))
		}
	}
	for i := 0; i < len(rs); {
		if end := numberAt(rs, i); end > 0 {
			plain(pos, i)
			b.WriteString(s.sgr(string(rs[i:end]), append(append([]string{}, base...), bold)...))
			i, pos = end, end
			continue
		}
		i++
	}
	plain(pos, len(rs))
	return b.String()
}

// paint highlights versions, hashes and session ids (token colour) and
// numbers with units (bold) inside value.
func (s *style) paint(value string, base []string) string {
	var b strings.Builder
	pos := 0
	for _, m := range tokenRe.FindAllStringIndex(value, -1) {
		b.WriteString(s.paintNumbers(value[pos:m[0]], base))
		b.WriteString(s.sgr(value[m[0]:m[1]], s.th.Colors.Token))
		pos = m[1]
	}
	b.WriteString(s.paintNumbers(value[pos:], base))
	return b.String()
}

// verdictColours returns the dot colour and the value colour of a row.
func (s *style) verdictColours(r Row) (dot, val string) {
	c := s.th.Colors
	if r.Verdict != nil {
		switch *r.Verdict {
		case VerdictGood:
			return c.OK, ""
		case VerdictAttention:
			return c.Warn, ""
		case VerdictBad:
			return c.Err, c.Err
		}
		return c.Neutral, ""
	}
	switch v := r.Value; {
	case badRe.MatchString(v):
		return c.Err, c.Err
	case calmRe.MatchString(v):
		return c.OK, ""
	case attnRe.MatchString(v):
		return c.Warn, ""
	case goodRe.MatchString(v):
		return c.OK, ""
	}
	return c.Neutral, ""
}

func (s *style) valueCol() int { return 5 + s.th.LabelWidth }

// nestedLabelW is the wider label column of long nested labels.
const nestedLabelW = 34

// kv is one aligned key/value row: values start at the value column; a
// verdict dot precedes the label; continuation lines hang under the value.
func (s *style) kv(r Row) string {
	dotC, valC := s.verdictColours(r)
	var base []string
	if valC != "" {
		base = []string{valC}
	}
	first, rest, _ := strings.Cut(r.Value, "\n")
	var head string
	if r.Nested && runeLen(r.Key) >= s.th.LabelWidth {
		head = "     " + s.sgr(padRight(r.Key, nestedLabelW), s.th.Colors.Label)
	} else {
		label := padRight(truncRunes(r.Key, s.th.LabelWidth-1), s.th.LabelWidth)
		dot := "  "
		if !r.Nested {
			dot = s.sgr(s.th.Glyphs.Dot+" ", dotC)
		}
		head = "   " + dot + s.sgr(label, s.th.Colors.Label)
	}
	lines := []string{head + s.paint(first, base)}
	if rest != "" || strings.Contains(r.Value, "\n") {
		for _, extra := range strings.Split(rest, "\n") {
			extra = strings.TrimLeft(strings.TrimSpace(extra), s.th.Glyphs.Sep+" ")
			lines = append(lines, strings.Repeat(" ", s.valueCol())+
				s.sgr(s.th.Glyphs.Sep+" ", s.th.Colors.Faint)+s.paint(extra, []string{s.th.Colors.Soft}))
		}
	}
	return strings.Join(lines, "\n")
}

// free renders a free-form message line: "label: value" with a short label
// becomes a key/value row, anything else is painted text.
func (s *style) free(text string) string {
	lead := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
	t := strings.TrimSpace(text)
	if head, rest, ok := strings.Cut(t, ": "); ok {
		if n := runeLen(head); n >= 2 && n <= 34 && !strings.ContainsAny(head, `\/`) {
			switch {
			case n < s.th.LabelWidth:
				return s.kv(Row{Key: head, Value: rest, Nested: lead > 0})
			case lead > 0:
				return "     " + strings.Repeat(" ", lead) + s.sgr(padRight(head, nestedLabelW), s.th.Colors.Label) + s.paint(rest, nil)
			}
			return s.kv(Row{Key: head, Value: rest})
		}
	}
	return "   " + strings.Repeat(" ", lead) + s.paint(t, nil)
}
