package update

import (
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// dumpMaxHidden caps how many lines one collapsed command dump may hide, so
// a missing end line can never swallow the rest of the update output.
const dumpMaxHidden = 400

// outputFilter is the stateful screen filter for one `hermes update` run.
// On top of displayLine it (1) collapses a failure line that echoes an
// inline-code command, plus the script lines after it, to one short line,
// and (2) shortens "⚠ … did not finish: <long command line>" lines. The log
// file keeps every raw line.
type outputFilter struct {
	hiding bool
	hidden int
	// cuaHintStays lets Hermes' own cua-driver lines through unchanged. Set
	// when the safe updater does not do the deferred refresh itself (no
	// logon-task support off Windows): the line would otherwise promise a
	// refresh that never happens and hide the one hint the user can act on.
	cuaHintStays bool
}

// display returns the screen text for line ("" = hide).
func (f *outputFilter) display(line string) string {
	if f.cuaHintStays && (strings.Contains(line, hermes.CuaDeferred) || strings.Contains(line, hermes.CuaManualHint)) {
		return line
	}
	if f.hiding {
		switch {
		case hermes.CommandDumpEndRe.MatchString(line):
			f.hiding = false
			return ""
		case hermes.TriggerFor(line) != "" || f.hidden >= dumpMaxHidden:
			f.hiding = false // a progress line means the dump is over: show it
		default:
			f.hidden++
			return ""
		}
	}
	if m := hermes.CommandDumpStartRe.FindStringSubmatch(line); m != nil {
		if !hermes.CommandDumpEndRe.MatchString(line) {
			f.hiding, f.hidden = true, 0
		}
		return m[1] + " (command details in the log)"
	}
	if u, ok := hermes.ParseUnfinished(line); ok {
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		s := indent + "⚠ " + u.What + " did not finish"
		if u.Reason != "" {
			s += ": " + u.Reason
		}
		if u.Hint != "" {
			s += " (" + u.Hint + ")"
		}
		return s
	}
	return displayLine(line)
}

// cleanRow drops a value prefix that repeats the key ("settings: settings: …",
// "Start tile: Start tile: …").
func cleanRow(row ui.Row) ui.Row {
	if k := strings.TrimSpace(row.Key); k != "" && len(row.Value) > len(k)+2 &&
		strings.EqualFold(row.Value[:len(k)], k) && row.Value[len(k):len(k)+2] == ": " {
		row.Value = row.Value[len(k)+2:]
	}
	return row
}
