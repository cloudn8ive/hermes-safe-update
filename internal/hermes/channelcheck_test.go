package hermes

import (
	"strings"
	"testing"
)

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// Each fixture is the shape of what `hermes update --check` prints on a
// release channel (synthetic text, see testdata/contracts). The verdict, the
// release label and the commit are read from the ASCII tail of the lines.
func TestParseChannelCheckFixtures(t *testing.T) {
	cases := []struct {
		file    string
		verdict CheckVerdict
		channel string
		label   string
		sha     string
		reason  string
	}{
		{"update-check-stable-available.txt", VerdictAvailable, "stable", "stable v0.22.1", "abcdef012345", ""},
		// Hermes v0.21.6 / v0.21.7 print the same "available" line for a release that is
		// OLDER than the checkout (they have no "newer than the selected release"
		// branch): the wording cannot tell, Safe Update asks git.
		{"update-check-stable-available-ahead-v0217.txt", VerdictAvailable, "stable", "stable v0.22.0", "0123456789ab", ""},
		{"update-check-stable-uptodate.txt", VerdictUpToDate, "stable", "stable v0.22.0", "0123456789ab", ""},
		{"update-check-stable-waits.txt", VerdictWaitsForNext, "stable", "stable v0.22.0", "", ""},
		{"update-check-stable-rollback.txt", VerdictRollback, "stable", "stable v0.22.0", "0123456789ab", ""},
		{"update-check-stable-unresolved.txt", VerdictUnresolved, "stable", "", "",
			"Connection to api.github.com failed (timed out) — check your connection, firewall or proxy."},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw := readFixture(t, c.file)
			for variant, out := range map[string]string{"as stored": raw, "CRLF": crlf(strings.ReplaceAll(raw, "\r\n", "\n"))} {
				got := ParseChannelCheck(out)
				if got.Verdict != c.verdict || got.Channel != c.channel || got.Label() != c.label || got.SHA12 != c.sha || got.Reason != c.reason {
					t.Errorf("%s: %+v (label %q), want verdict %v channel %q label %q sha %q reason %q",
						variant, got, got.Label(), c.verdict, c.channel, c.label, c.sha, c.reason)
				}
			}
		})
	}
}

func TestParseChannelCheckVersions(t *testing.T) {
	got := ParseChannelCheck(readFixture(t, "update-check-stable-available.txt"))
	if got.Version != "0.22.1" || got.Release != "stable" {
		t.Errorf("%+v", got)
	}
	// the waits line carries the version too, with no commit
	got = ParseChannelCheck(readFixture(t, "update-check-stable-waits.txt"))
	if got.Version != "0.22.0" || got.SHA12 != "" {
		t.Errorf("%+v", got)
	}
}

func TestParseChannelCheckMainChannel(t *testing.T) {
	got := ParseChannelCheck(readFixture(t, "update-check-main.txt"))
	if got.Channel != "main" || got.Verdict != VerdictAvailable || got.Commits == nil || *got.Commits != 7 || got.Label() != "" {
		t.Errorf("%+v", got)
	}
	got = ParseChannelCheck("→ Update channel: main\n→ Fetching from origin...\n✓ Already up to date.\n")
	if got.Channel != "main" || got.Verdict != VerdictUpToDate {
		t.Errorf("up to date: %+v", got)
	}
	// no channel line at all (an explicit --branch): still readable
	got = ParseChannelCheck("Update available: 3 commits behind origin/main.\n")
	if got.Channel != "" || got.Verdict != VerdictAvailable || got.Commits == nil || *got.Commits != 3 {
		t.Errorf("no channel line: %+v", got)
	}
}

// A mangled leading glyph (a console code page) must not matter: the
// patterns match the ASCII tail of each line.
func TestParseChannelCheckMangledGlyphs(t *testing.T) {
	mangle := strings.NewReplacer("→", "â†’", "✓", "âœ“", "✗", "âœ—", "⚕", "âš•")
	for _, f := range []struct {
		file    string
		verdict CheckVerdict
	}{
		{"update-check-stable-available.txt", VerdictAvailable},
		{"update-check-stable-uptodate.txt", VerdictUpToDate},
		{"update-check-stable-waits.txt", VerdictWaitsForNext},
		{"update-check-stable-rollback.txt", VerdictRollback},
		{"update-check-stable-unresolved.txt", VerdictUnresolved},
	} {
		out := mangle.Replace(readFixture(t, f.file))
		if got := ParseChannelCheck(out); got.Verdict != f.verdict || got.Channel != "stable" {
			t.Errorf("%s mangled: %+v", f.file, got)
		}
	}
	if got := ParseChannelCheck("??? Update channel: stable\n??? This checkout is newer than the selected release: stable v0.22.0 (0123456789ab).\n"); got.Verdict != VerdictRollback {
		t.Errorf("question marks: %+v", got)
	}
}

// The rollback second line alone is enough: a wrapper must never read a
// pending downgrade as "up to date" (the old CheckSaysBehind did).
func TestParseChannelCheckRollbackIsNotUpToDate(t *testing.T) {
	out := readFixture(t, "update-check-stable-rollback.txt")
	if got := ParseChannelCheck(out); got.Verdict != VerdictRollback {
		t.Errorf("%+v", got)
	}
	tail := "  `hermes update` moves it back to that release (`hermes update --set-channel main` follows every commit).\n"
	if got := ParseChannelCheck(tail); got.Verdict != VerdictRollback {
		t.Errorf("tail only: %+v", got)
	}
}

// Words nobody knows must never come out as "nothing to do".
func TestParseChannelCheckUnknownWording(t *testing.T) {
	for _, out := range []string{
		"",
		"\r\n",
		"→ Update channel: stable\n",
		"→ Update channel: stable\n✓ Everything is fine, nothing to see.\n",
		"→ Update channel: stable\nSomething completely new happened\n",
		"Traceback (most recent call last):\n  File \"x.py\", line 1\nValueError: boom\n",
	} {
		if got := ParseChannelCheck(out); got.Verdict != VerdictUnknown {
			t.Errorf("%q read as %v", out, got.Verdict)
		}
	}
	if VerdictUnknown != 0 {
		t.Error("the zero verdict must be unknown")
	}
}

func TestChannelLineRegex(t *testing.T) {
	for out, want := range map[string]string{
		"→ Update channel: stable\n":             "stable",
		"→ Update channel: main\r\n":             "main",
		"x Update channel: canary-2\n":           "canary-2",
		"â†’ Update channel: stable\r\nrest":     "stable",
		"Update channel for 0123456789abcdef: x": "", // the --set-channel reply is not this line
	} {
		got := ParseChannelCheck(out).Channel
		if got != want {
			t.Errorf("%q -> %q, want %q", out, got, want)
		}
	}
}

func TestUpdateChannelAndReleaseLines(t *testing.T) {
	raw := readFixture(t, "update-stable-switching.txt")
	for variant, out := range map[string]string{"as stored": raw, "CRLF": crlf(raw)} {
		u := ParseUpdateRelease(out)
		if u.Channel != "stable" || u.Label() != "stable v0.22.1" || u.SHA12 != "abcdef012345" || u.Waits || u.SwitchSHA != "abcdef0123" {
			t.Errorf("%s: %+v", variant, u)
		}
		// a release update never prints the branch-path line
		if n, ok := ParseFoundCommits(out); ok {
			t.Errorf("%s: found %d commits on a release update", variant, n)
		}
		if m := UpdateCompleteRe.FindStringSubmatch(out); m == nil || m[1] != "v0.22.0" || m[2] != "v0.22.1" {
			t.Errorf("%s: complete line: %v", variant, m)
		}
	}
	w := ParseUpdateRelease(readFixture(t, "update-stable-waits.txt"))
	if w.Channel != "stable" || w.Label() != "stable v0.22.0" || !w.Waits || w.SwitchSHA != "" {
		t.Errorf("waits: %+v", w)
	}
	// the main path: no channel / release line, the Found line stays
	main := "→ Fetching updates...\n→ Found 12 new commit(s)\n✓ Update complete! (v0.22.0+5.gabc → v0.22.0+17.gdef)\n"
	if u := ParseUpdateRelease(main); u != (UpdateRelease{}) {
		t.Errorf("main output: %+v", u)
	}
	if n, ok := ParseFoundCommits(main); !ok || n != 12 {
		t.Errorf("found: %d %v", n, ok)
	}
	// "Update channel:" at the start of a main-channel check/apply
	if u := ParseUpdateRelease("→ Update channel: main\n→ Found 2 new commit(s)\n"); u.Channel != "main" || u.Label() != "" {
		t.Errorf("main with a channel line: %+v", u)
	}
}
