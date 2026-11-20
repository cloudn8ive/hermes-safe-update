package hermes

import (
	"strings"
	"testing"
)

func TestParsePluginRows(t *testing.T) {
	rows, err := ParsePluginRows("notice before the list\n" + readFixture(t, "extras-plugins-check.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	want := map[string]bool{"memory-demo": true, "notes-demo": false, "broken-demo": false, "pipped-demo": false, "by-hand": false}
	for _, r := range rows {
		if got := r.Updatable(); got != want[r.Name] {
			t.Errorf("%s: Updatable = %v, want %v", r.Name, got, want[r.Name])
		}
	}
	if Short(rows[0].Current) != "01234567" || Short(rows[4].Current) != "-" {
		t.Errorf("Short: %q %q", Short(rows[0].Current), Short(rows[4].Current))
	}
}

func TestParsePluginRowsRejectsOtherText(t *testing.T) {
	for _, in := range []string{"", "Plugin updates\n┌──┐\n│ Name │\n", "[ {\"class\": \"git\"} ]", "[ not json"} {
		if _, err := ParsePluginRows(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestSafePluginName(t *testing.T) {
	for _, ok := range []string{"holographic", "a", "memory-store_2.x"} {
		if !SafePluginName(ok) {
			t.Errorf("%q should be safe", ok)
		}
	}
	for _, bad := range []string{"", "-rf", "../x", "a b", "a;b", "a/b", "a\\b", strings.Repeat("a", 65), ".hidden"} {
		if SafePluginName(bad) {
			t.Errorf("%q must not be safe", bad)
		}
	}
}

func TestParseSkillCheck(t *testing.T) {
	sc, ok := ParseSkillCheck(readFixture(t, "extras-skills-check-update.txt"))
	if !ok || sc.Updates != 1 || len(sc.Rows) != 2 || strings.Join(sc.UpdateNames(), ",") != "demo-skill" {
		t.Fatalf("update fixture: ok=%v %+v", ok, sc)
	}
	sc, ok = ParseSkillCheck(readFixture(t, "extras-skills-check-current.txt"))
	if !ok || sc.Updates != 0 || len(sc.Rows) != 1 {
		t.Fatalf("current fixture: ok=%v %+v", ok, sc)
	}
	sc, ok = ParseSkillCheck(readFixture(t, "extras-skills-check-empty.txt"))
	if !ok || !sc.Empty {
		t.Fatalf("empty fixture: ok=%v %+v", ok, sc)
	}
	if _, ok := ParseSkillCheck("Everything fine\n"); ok {
		t.Error("reworded output was read as an answer")
	}
}

func TestParseDoctor(t *testing.T) {
	d, ok := ParseDoctor(readFixture(t, "extras-pm-doctor-fault.txt"))
	if !ok || d.OK != 3 || d.Soft != 1 || d.NA != 1 || len(d.Faults) != 2 {
		t.Fatalf("fault fixture: ok=%v %+v", ok, d)
	}
	if f := d.Faults[0]; f.Name != "git" || !f.DigestFault() || !strings.Contains(f.Advice(), "stray files") {
		t.Errorf("git fault: %+v / %s", f, f.Advice())
	}
	if f := d.Faults[1]; f.Name != "ripgrep" || f.DigestFault() || f.Advice() != "`hermes pm install ripgrep` installs it" {
		t.Errorf("ripgrep fault: %+v / %s", f, f.Advice())
	}
	d, ok = ParseDoctor(readFixture(t, "extras-pm-doctor-ok.txt"))
	if !ok || len(d.Faults) != 0 || d.OK != 2 {
		t.Fatalf("ok fixture: ok=%v %+v", ok, d)
	}
	if _, ok := ParseDoctor("Traceback (most recent call last):\n  boom\n"); ok {
		t.Error("a traceback was read as a doctor answer")
	}
}

func TestParsePMUpdateCheck(t *testing.T) {
	ups := ParsePMUpdateCheck(readFixture(t, "extras-pm-update-check.txt"))
	if len(ups) != 2 || ups[0] != (ToolUpdate{"agent-browser", "1.0.0", "1.4.1"}) || ups[1].Name != "git" || ups[1].To != "2.56.0+4" {
		t.Fatalf("ups = %+v", ups)
	}
}

func TestEvictedPluginsAndRefusals(t *testing.T) {
	out := "x\nDisabled plugin 'memory-demo' in C:\\h: the dependency environment no longer builds with it\nDisabled plugin 'b' in /h: y\n"
	if got := EvictionLines(out); len(got) != 2 || !strings.HasPrefix(got[0], "Disabled plugin 'memory-demo'") || !strings.Contains(got[0], "no longer builds") || strings.Contains(got[1], "\n") {
		t.Errorf("eviction lines = %q", got)
	}
	if got := strings.Join(ConfigSetProviderArgv("memory-demo"), " "); got != "config set memory.provider memory-demo" {
		t.Errorf("config set argv %q", got)
	}
	for _, s := range []string{"Blocked: dangerous verdict", "Update of x not applied: widens", "Error: boom"} {
		if !PluginUpdateRefused(s) {
			t.Errorf("%q not seen as a refusal", s)
		}
	}
	if PluginUpdateRefused("Plugin x updated.") {
		t.Error("success read as refusal")
	}
	if got := strings.Join(PluginUpdateArgv("x"), " "); got != "plugins update x" {
		t.Errorf("update argv %q", got)
	}
	if got := strings.Join(PluginEnableArgv("x"), " "); got != "plugins enable x" {
		t.Errorf("enable argv %q", got)
	}
}
