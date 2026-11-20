package hermes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- Hermes' apply-time hold (v0.8.1) ----

func TestParseApplyHoldTable(t *testing.T) {
	const why = "→ This build is newer than the stable release 0123456789ab: that release predates the gateway runtime your sessions use, so there is nothing to update to yet."
	const hold = "✓ Holding at this build until a newer stable release ships."
	for _, c := range []struct {
		name, in string
		ok       bool
		ch, sha  string
	}{
		{"stable, both lines", why + "\n" + hold + "\n", true, "stable", "0123456789ab"},
		{"beta", strings.ReplaceAll(why, "stable", "beta") + "\n" + strings.ReplaceAll(hold, "stable", "beta"), true, "beta", "0123456789ab"},
		{"any channel name", strings.ReplaceAll(why+"\n"+hold, "stable", "nightly-2"), true, "nightly-2", "0123456789ab"},
		{"only the Holding line", hold, true, "stable", ""},
		{"ASCII-mapped glyphs", strings.NewReplacer("→", "->", "✓", "OK").Replace(why + "\n" + hold), true, "stable", "0123456789ab"},
		{"question marks for glyphs", strings.NewReplacer("→", "?", "✓", "?").Replace(why + "\n" + hold), true, "stable", "0123456789ab"},
		{"mojibake glyph", strings.NewReplacer("→", "â†’", "✓", "âœ“").Replace(why + "\n" + hold), true, "stable", "0123456789ab"},
		{"no glyph at all", strings.NewReplacer("→ ", "", "✓ ", "").Replace(why + "\n" + hold), true, "stable", "0123456789ab"},
		{"CRLF", strings.ReplaceAll(why+"\n"+hold+"\n", "\n", "\r\n"), true, "stable", "0123456789ab"},
		{"indented", "  " + hold, true, "stable", ""},
		{"channel differs between the lines: sha is not taken", strings.ReplaceAll(why, "stable", "beta") + "\n" + hold, true, "stable", ""},
		{"the → line alone is a hold (the stash notice replaced the Holding line)", why + "\n", true, "stable", "0123456789ab"},
		{"the → line alone, beta, CRLF, ASCII glyph", strings.NewReplacer("stable", "beta", "→", "->", "\n", "\r\n").Replace(why + "\n"), true, "beta", "0123456789ab"},
		{"the → line of a refusal-shaped text is not a hold", "→ This build is newer than the stable release 0123456789ab: that release predates the gateway runtime.", false, "", ""},
		{"check wording is not the apply hold", "→ Holding at this build: stable v0.22.0 (0123456789ab) predates the gateway runtime your sessions use.", false, "", ""},
		{"an unrelated line", "✓ Already up to date!", false, "", ""},
		{"empty", "", false, "", ""},
		{"quoted inside another line", "log: saw '" + hold + "' earlier", false, "", ""},
		{"too long a glyph prefix", "abcde " + hold, false, "", ""},
		{"upper-case channel is not a channel name", "✓ Holding at this build until a newer Stable release ships.", false, "", ""},
		{"a different ending", "✓ Holding at this build until a newer stable release ships soon.", false, "", ""},
	} {
		h, ok := ParseApplyHold(c.in)
		if ok != c.ok || h.Channel != c.ch || h.SHA12 != c.sha {
			t.Errorf("%s: got (%+v, %v), want channel=%q sha=%q ok=%v", c.name, h, ok, c.ch, c.sha, c.ok)
		}
	}
}

func TestApplyHoldFixtures(t *testing.T) {
	h, ok := ParseApplyHold(readFixture(t, "update-apply-hold.txt"))
	if !ok || h.Channel != "stable" || h.SHA12 != "0123456789ab" {
		t.Errorf("stable fixture: %+v %v", h, ok)
	}
	if got := h.Why(); got != "the stable release 0123456789ab predates the gateway runtime your sessions use" {
		t.Errorf("why = %q", got)
	}
	b, ok := ParseApplyHold(readFixture(t, "update-apply-hold-beta.txt"))
	if !ok || b.Channel != "beta" || b.SHA12 != "fedcba987654" {
		t.Errorf("beta fixture: %+v %v", b, ok)
	}
	if got := (ApplyHold{Channel: "beta"}).Why(); got != "the beta release predates the gateway runtime your sessions use" {
		t.Errorf("why without sha = %q", got)
	}
}

// The hold wording must not disturb the parsers that read the same output.
func TestApplyHoldDoesNotDisturbOtherParsers(t *testing.T) {
	out := readFixture(t, "update-apply-hold.txt")
	if u := ParseUpdateRelease(out); u.Channel != "stable" || u.Release != "stable" || u.Version != "0.22.0" || u.SHA12 != "0123456789ab" {
		t.Errorf("release parse = %+v", u)
	}
	if UpdateCompleteRe.MatchString(out) {
		t.Error("a hold looks like Update complete!")
	}
	if _, ok := ParseFoundCommits(out); ok {
		t.Error("a hold has commits")
	}
	if len(ParseCatalogNotices(out)) != 0 {
		t.Error("a hold has a catalog notice")
	}
	// and the check wording stays a check wording
	if _, ok := ParseApplyHold(readFixture(t, "update-check-stable-holding.txt")); ok {
		t.Error("the --check hold wording was taken for the apply hold")
	}
	if c := ParseChannelCheck(readFixture(t, "update-check-stable-holding.txt")); c.Verdict != VerdictRollback {
		t.Errorf("check hold verdict = %v", c.Verdict)
	}
}

// ---- the new-catalog plugin notice (v0.8.1) ----

func TestParseCatalogNoticesFixtureAndRegression(t *testing.T) {
	out := readFixture(t, "update-catalog-notice.txt")
	got := ParseCatalogNotices(out)
	want := []CatalogNotice{
		{Label: "this profile", Count: 2, Names: []string{"memory-demo", "notes-demo"}},
		{Label: "work", Count: 1, Names: []string{"notes-demo"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("notices = %+v, want %+v", got, want)
	}
	// every other parser still reads the same output as before
	if m := UpdateCompleteRe.FindStringSubmatch(out); m == nil || m[1] != "v0.22.7" || m[2] != "v0.23.0" {
		t.Errorf("Update complete! = %v", m)
	}
	if u := ParseUpdateRelease(out); u.Channel != "stable" || u.Version != "0.23.0" || u.SHA12 != "0123456789ab" || u.SwitchSHA != "0123456789" {
		t.Errorf("release parse = %+v", u)
	}
	if _, ok := ParseApplyHold(out); ok {
		t.Error("the notice looks like a hold")
	}
	if got := UnfinishedSteps(out); len(got) != 0 {
		t.Errorf("unfinished steps = %v", got)
	}
	if _, held := ParseGatewayHeld(out); held {
		t.Error("gateway held")
	}
	if TriggerFor("  → 2 plugin update(s) from the new catalog in this profile: a, b") != "" {
		t.Error("the notice advanced a progress trigger")
	}
	if _, ok := ParseGCLocked("  → 2 plugin update(s) from the new catalog in this profile: a, b"); ok {
		t.Error("the notice looks like a locked cleanup")
	}
}

func TestParseCatalogNoticesTable(t *testing.T) {
	const line = "  → 2 plugin update(s) from the new catalog in this profile: a, b"
	for _, c := range []struct {
		name, in string
		want     []CatalogNotice
	}{
		{"plain", line, []CatalogNotice{{Label: "this profile", Count: 2, Names: []string{"a", "b"}}}},
		{"ASCII arrow", strings.Replace(line, "→", "->", 1), []CatalogNotice{{Label: "this profile", Count: 2, Names: []string{"a", "b"}}}},
		{"question mark glyph", strings.Replace(line, "→", "?", 1), []CatalogNotice{{Label: "this profile", Count: 2, Names: []string{"a", "b"}}}},
		{"CRLF", line + "\r\n    apply: hermes plugins update\r\n", []CatalogNotice{{Label: "this profile", Count: 2, Names: []string{"a", "b"}}}},
		{"one name", "  → 1 plugin update(s) from the new catalog in work: solo", []CatalogNotice{{Label: "work", Count: 1, Names: []string{"solo"}}}},
		{"a sibling label with a space", "  → 3 plugin update(s) from the new catalog in profile 'my work': x, y, z", []CatalogNotice{{Label: "profile 'my work'", Count: 3, Names: []string{"x", "y", "z"}}}},
		{"an unsafe name is dropped, not shown", "  → 2 plugin update(s) from the new catalog in this profile: good, bad;rm -rf", []CatalogNotice{{Label: "this profile", Count: 2, Names: []string{"good"}, Dropped: 1}}},
		{"only unsafe names: no notice", "  → 1 plugin update(s) from the new catalog in this profile: $(x)", nil},
		{"zero", "  → 0 plugin update(s) from the new catalog in this profile: a", nil},
		{"no names", "  → 2 plugin update(s) from the new catalog in this profile: ", nil},
		{"unrelated", "  → Found 3 new commit(s)", nil},
		{"the apply line alone", "    apply: hermes plugins update   (or turn on: hermes plugins auto-update on)", nil},
		{"quoted in prose", "note: '→ 2 plugin update(s) from the new catalog in this profile: a' was printed", nil},
		{"empty", "", nil},
	} {
		if got := ParseCatalogNotices(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// ---- plugins pinned with --ref (v0.8.1) ----

func readFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPinsFromTheRecordsOfBothHermesVersions(t *testing.T) {
	for _, fx := range []string{"plugins-install-metadata-v0.22.7.json", "plugins-install-metadata-v0.23.0.json"} {
		s := ParsePinStore(readFixtureBytes(t, fx))
		for _, c := range []struct {
			name, class string
			want        PluginPin
		}{
			{"memory-demo", "catalog", PluginPin{}}, // installed at the reviewed pin: pinned:true, catalog.pin set
			{"notes-demo", "catalog", PluginPin{Kind: PinRef, Ref: "89abcdef0123456789abcdef0123456789abcdef"}},
			{"by-url", "git", PluginPin{}},
			{"by-url-ref", "git", PluginPin{Kind: PinGit, Ref: "2222222222222222222222222222222222222222"}},
			{"missing", "catalog", PluginPin{Kind: PinUnknown, Why: "the plugin has no install record"}},
			{"by-url", "catalog", PluginPin{Kind: PinUnknown, Why: "its install record has no catalog block"}},
			{"memory-demo", "git", PluginPin{Kind: PinUnknown, Why: "a git plugin whose install record has a catalog block"}},
			{"memory-demo", "pip", PluginPin{Kind: PinUnknown, Why: `plugins of class "pip" are not updated by Safe Update`}},
			// the apply-time re-check has no class: the record's shape decides
			{"notes-demo", "", PluginPin{Kind: PinRef, Ref: "89abcdef0123456789abcdef0123456789abcdef"}},
			{"by-url-ref", "", PluginPin{Kind: PinGit, Ref: "2222222222222222222222222222222222222222"}},
			{"memory-demo", "", PluginPin{}},
			{"by-url", "", PluginPin{}},
		} {
			if got := s.Pin(c.name, c.class); got != c.want {
				t.Errorf("%s %s/%s: got %+v, want %+v", fx, c.name, c.class, got, c.want)
			}
		}
	}
	if s := ParsePinStore(readFixtureBytes(t, "plugins-install-metadata-v0.23.0.json")); s.Pin("old-demo", "catalog").Held() {
		t.Error("an unpinned catalog install (pinned:false, pin set) is held")
	}
}

func TestPinsFailTowardNotTouching(t *testing.T) {
	cat := func(pinned, pin string) string {
		return `{"p": {"pinned": ` + pinned + `, "revision": "abc", "catalog": {"name": "p", "pin": ` + pin + `}}}`
	}
	for _, c := range []struct {
		name, body, class string
		kind              PinKind
	}{
		{"not JSON", `{{{`, "catalog", PinUnknown},
		{"JSON null", `null`, "catalog", PinUnknown},
		{"a list", `[1]`, "catalog", PinUnknown},
		{"a record that is a string", `{"p": "x"}`, "catalog", PinUnknown},
		{"pinned as text", cat(`"yes"`, `"abc"`), "catalog", PinUnknown},
		{"pin as a number", cat(`true`, `7`), "catalog", PinUnknown},
		{"catalog block as text", `{"p": {"pinned": false, "catalog": "x"}}`, "catalog", PinUnknown},
		{"catalog block null", `{"p": {"pinned": false, "catalog": null}}`, "catalog", PinUnknown},
		{"older package-manager format", `{"p": {"catalog_name": "p", "revision": "abc", "pinned": true}}`, "catalog", PinUnknown},
		{"held with --ref", cat(`true`, `""`), "catalog", PinRef},
		{"held, pin null", cat(`true`, `null`), "catalog", PinRef},
		{"held, pin absent", `{"p": {"pinned": true, "revision": "abc", "catalog": {"name": "p"}}}`, "catalog", PinRef},
		{"at the reviewed pin", cat(`true`, `"abc"`), "catalog", PinNone},
		{"not pinned", cat(`false`, `""`), "catalog", PinNone},
		{"pinned absent: Hermes tests `is True`", `{"p": {"revision": "abc", "catalog": {"name": "p", "pin": ""}}}`, "catalog", PinNone},
		{"git, pinned null", `{"p": {"pinned": null, "revision": "abc"}}`, "git", PinNone},
		{"git, pinned", `{"p": {"pinned": true, "revision": "abc"}}`, "git", PinGit},
		{"git, pinned as text", `{"p": {"pinned": "true"}}`, "git", PinUnknown},
	} {
		if got := ParsePinStore([]byte(c.body)).Pin("p", c.class); got.Kind != c.kind {
			t.Errorf("%s: kind = %v (%+v), want %v", c.name, got.Kind, got, c.kind)
		}
	}
	// a BOM is read as Hermes does (utf-8-sig)
	if got := ParsePinStore(append([]byte("\xef\xbb\xbf"), cat(`true`, `""`)...)).Pin("p", "catalog"); got.Kind != PinRef {
		t.Errorf("with BOM: %+v", got)
	}
}

func TestReadPinStoreFromDisk(t *testing.T) {
	home := t.TempDir()
	if s := ReadPinStore(home); !s.Absent || s.Pin("p", "catalog").Kind != PinUnknown {
		t.Errorf("no file: %+v", s)
	}
	p := InstallMetadataPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, readFixtureBytes(t, "plugins-install-metadata-v0.23.0.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadPinStore(home).Pin("notes-demo", "catalog"); got.Kind != PinRef || ShortRef(got.Ref) != "89abcdef" {
		t.Errorf("from disk: %+v", got)
	}
	// a directory where the file should be cannot be read: unknown, not "not pinned"
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ReadPinStore(home).Pin("notes-demo", "catalog"); got.Kind != PinUnknown {
		t.Errorf("unreadable: %+v", got)
	}
	if ShortRef("") != "?" || ShortRef("abc") != "abc" {
		t.Error("ShortRef")
	}
}

func TestPluginPinHeldMeansEverythingButRecognisedUnpinned(t *testing.T) {
	for k, want := range map[PinKind]bool{PinNone: false, PinRef: true, PinGit: true, PinUnknown: true} {
		if got := (PluginPin{Kind: k}).Held(); got != want {
			t.Errorf("kind %d: Held = %v, want %v", k, got, want)
		}
	}
}

func TestPinUnknownSaysWhy(t *testing.T) {
	for _, c := range []struct{ body, why string }{
		{`{"p": {"catalog_name": "p", "revision": "abc", "pinned": true}}`, "older package-manager format"},
		{`{"p": {"pinned": false, "catalog": "x"}}`, "no catalog block"},
		{`{"p": {"pinned": "no", "catalog": {}}}`, "not true or false"},
		{`{"other": {}}`, "no install record"},
		{`[]`, "not a JSON object"},
	} {
		if got := ParsePinStore([]byte(c.body)).Pin("p", "catalog"); got.Kind != PinUnknown || !strings.Contains(got.Why, c.why) {
			t.Errorf("%s: %+v, want unknown mentioning %q", c.body, got, c.why)
		}
	}
}

func TestApplyHoldStashNotice(t *testing.T) {
	h, ok := ParseApplyHold(readFixture(t, "update-apply-hold-stash.txt"))
	if !ok || h.Channel != "stable" || h.SHA12 != "0123456789ab" || h.StashFiles != 3 || h.StashRef != "stash@{0}" {
		t.Errorf("%+v %v", h, ok)
	}
	plain, _ := ParseApplyHold(readFixture(t, "update-apply-hold.txt"))
	if plain.StashFiles != 0 || plain.StashRef != "" {
		t.Errorf("stash in the plain hold: %+v", plain)
	}
	// a ref with shell characters is not kept
	bad, ok := ParseApplyHold(strings.Replace(readFixture(t, "update-apply-hold-stash.txt"), "Stash ref: stash@{0}", "Stash ref: $(rm -rf)", 1))
	if !ok || bad.StashRef != "" || bad.StashFiles != 3 {
		t.Errorf("%+v", bad)
	}
}
