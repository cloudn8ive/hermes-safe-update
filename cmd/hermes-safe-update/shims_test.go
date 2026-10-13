package main

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scriptsDir is the repository's scripts/ folder (tests run in the package dir).
const scriptsDir = "../../scripts"

// launcherShims lists the launcher files in scripts/ (everything named
// hermes-*, i.e. not the build scripts).
func launcherShims(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(scriptsDir, "hermes-*"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range m {
		out = append(out, filepath.Base(p))
	}
	if len(out) == 0 {
		t.Fatal("no launcher shims found in scripts/")
	}
	return out
}

// Every launcher shim must ship as a release asset from both build scripts,
// so the two lists cannot drift from scripts/ or from each other.
func TestEveryLauncherShimShipsFromBothBuildScripts(t *testing.T) {
	for _, build := range []string{"build-all.sh", "build-all.ps1"} {
		b, err := os.ReadFile(filepath.Join(scriptsDir, build))
		if err != nil {
			t.Fatal(err)
		}
		for _, shim := range launcherShims(t) {
			if !strings.Contains(string(b), "scripts/"+shim) {
				t.Errorf("%s does not copy scripts/%s into dist/", build, shim)
			}
		}
	}
}

// desktopEntry reads a .desktop file into group -> key -> value with the
// spec's string escapes (\s \n \t \r \\) already applied.
func desktopEntry(t *testing.T, path string) map[string]map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]map[string]string{}
	group := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			group = line[1 : len(line)-1]
			out[group] = map[string]string{}
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok || group == "" {
				t.Fatalf("%s: bad line %q", path, line)
			}
			out[group][strings.TrimSpace(k)] = strings.NewReplacer(`\s`, " ", `\n`, "\n", `\t`, "\t", `\r`, "\r", `\\`, `\`).Replace(strings.TrimSpace(v))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// execArgv splits a decoded Exec value the way the Desktop Entry spec says:
// whitespace separates arguments; inside double quotes \" \` \$ \\ escape.
func execArgv(t *testing.T, v string) []string {
	t.Helper()
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(v) && strings.IndexByte("\"`$\\", v[i+1]) >= 0:
			i++
			cur.WriteByte(v[i])
		case c == '"':
			inQuote, have = !inQuote, true
		case !inQuote && (c == ' ' || c == '\t'):
			if have || cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if inQuote {
		t.Fatalf("unterminated quote in Exec %q", v)
	}
	if have || cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// The Linux launcher opens a terminal and runs the shell shim from
// ~/.local/bin, for the main entry and the look-only "check" action.
func TestDesktopEntryRunsTheShellShims(t *testing.T) {
	d := desktopEntry(t, filepath.Join(scriptsDir, "hermes-safe-update.desktop"))
	main := d["Desktop Entry"]
	if main == nil {
		t.Fatal("no [Desktop Entry] group")
	}
	for k, want := range map[string]string{"Type": "Application", "Terminal": "true", "Actions": "check;"} {
		if main[k] != want {
			t.Errorf("%s=%q, want %q", k, main[k], want)
		}
	}
	if main["Name"] == "" {
		t.Error("Name is empty")
	}
	cases := []struct {
		group, shim string
	}{
		{"Desktop Entry", "hermes-safe-update.sh"},
		{"Desktop Action check", "hermes-update-check.sh"},
	}
	for _, c := range cases {
		g := d[c.group]
		if g == nil {
			t.Fatalf("no [%s] group", c.group)
		}
		got := execArgv(t, g["Exec"])
		want := []string{"sh", "-c", `exec sh "$HOME/.local/bin/` + c.shim + `"`}
		if !slices.Equal(got, want) {
			t.Errorf("[%s] Exec argv = %q, want %q", c.group, got, want)
		}
		if !slices.Contains(launcherShims(t), c.shim) {
			t.Errorf("[%s] runs %s, which is not in scripts/", c.group, c.shim)
		}
		if strings.Contains(g["Exec"], "%") {
			t.Errorf("[%s] Exec has a field code or a stray %%: %q", c.group, g["Exec"])
		}
	}
}
