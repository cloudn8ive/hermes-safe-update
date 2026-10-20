package main

import (
	"strings"
	"testing"
)

// --no-elevate is about UAC, which only Windows has.
func TestHelpSaysNoElevateIsWindowsOnly(t *testing.T) {
	i := strings.Index(helpText, "--no-elevate")
	if i < 0 {
		t.Fatal("help does not list --no-elevate")
	}
	line := helpText[i:]
	if j := strings.Index(line, "\n\n"); j >= 0 {
		line = line[:j]
	}
	if !strings.Contains(strings.SplitN(line, "\n\n", 2)[0], "Windows only") {
		t.Errorf("--no-elevate entry does not say it is Windows only:\n%s", line)
	}
}

// helpEntry is one flag's paragraph of --help.
func helpEntry(t *testing.T, flag string) string {
	t.Helper()
	i := strings.Index(helpText, "\n  "+flag)
	if i < 0 {
		t.Fatalf("help does not list %s", flag)
	}
	entry := helpText[i+1:]
	// the next entry starts at two spaces and a dash or a letter
	lines := strings.Split(entry, "\n")
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "                       ") {
			break
		}
		out = append(out, l)
	}
	return strings.Join(strings.Fields(strings.Join(out, " ")), " ") // one line: the wrap is not the text
}

// --unattended finishes an unfinished update, but only once.
func TestHelpSaysUnattendedFinishesAnUnfinishedUpdateOnce(t *testing.T) {
	for _, f := range []string{"--unattended", "--yes"} {
		e := helpEntry(t, f)
		if !strings.Contains(e, "unfinished update once") && !strings.Contains(e, "update that did not finish, once") {
			t.Errorf("%s entry does not say it finishes an unfinished update once:\n%s", f, e)
		}
	}
}

// NO_COLOR drops the colours and keeps the layout (ui.New); only --plain,
// HERMES_SAFE_UPDATE_PLAIN and TERM=dumb give plain text. The help says so.
func TestHelpDoesNotCallNoColorAPlainSwitch(t *testing.T) {
	e := helpEntry(t, "--plain")
	if !strings.Contains(e, "HERMES_SAFE_UPDATE_PLAIN=1") || !strings.Contains(e, "TERM=dumb") || !strings.Contains(e, "NO_COLOR keeps the layout and only drops the colours") {
		t.Errorf("--plain entry:\n%s", e)
	}
	if strings.Contains(e, "HERMES_SAFE_UPDATE_PLAIN=1, NO_COLOR") {
		t.Errorf("--plain entry lists NO_COLOR as a plain switch:\n%s", e)
	}
}

// The skipped unattended retry is exit 1 (needs attention), not 0, and both
// causes are named.
func TestHelpExitCodeOneNamesTheSkippedRetry(t *testing.T) {
	i0, i1, i2 := strings.Index(helpText, "\n  0  "), strings.Index(helpText, "\n  1  "), strings.Index(helpText, "\n  2  ")
	if i0 < 0 || i1 < i0 || i2 < i1 {
		t.Fatal("the exit code table is not where the test expects it")
	}
	zero, one := helpText[i0:i1], helpText[i1:i2]
	if strings.Contains(zero, "unfinished update") {
		t.Errorf("the exit 0 entry still mentions the skipped retry:\n%s", zero)
	}
	for _, want := range []string{"left an unfinished update alone", "an earlier run already", "own log could not be read"} {
		if !strings.Contains(strings.Join(strings.Fields(one), " "), want) {
			t.Errorf("the exit 1 entry lacks %q:\n%s", want, one)
		}
	}
}
