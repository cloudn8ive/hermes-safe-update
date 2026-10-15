package hermes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// toolsHome builds a synthetic Hermes home with the named tool folders, each
// holding the executable at rel.
func toolsHome(t *testing.T, rel string, folders ...string) string {
	t.Helper()
	home := t.TempDir()
	for _, f := range folders {
		touch(t, filepath.Join(home, "tools", f, rel), "")
	}
	return home
}

func writeFacts(t *testing.T, home, body string) {
	t.Helper()
	touch(t, filepath.Join(home, "tools", ToolsFactsFile), body)
}

// factsFor is the shape of Hermes' tools/facts.json (synthetic values).
func factsFor(pkg, entry string) string {
	return `{"packages": {"` + pkg + `": {"entry": "` + entry + `", "version": "x", "target": "win32-x64"}, "node": {"entry": "node-1.0.0-win32-x64"}}}`
}

func pyGlob(home string) []string {
	return []string{filepath.Join(home, "tools", "python-*", "python.exe")}
}

func TestPickManagedVersionAwareFallback(t *testing.T) {
	cases := []struct {
		name    string
		folders []string
		want    string
	}{
		{"3.14.10 beats 3.14.7 (plain name order says the opposite)", []string{"python-3.14.7+20260901-win32-x64", "python-3.14.10+20261201-win32-x64"}, "python-3.14.10+20261201-win32-x64"},
		{"3.14 beats 3.9", []string{"python-3.9.1", "python-3.14.0"}, "python-3.14.0"},
		{"3.100 beats 3.99", []string{"python-3.99.0", "python-3.100.0"}, "python-3.100.0"},
		{"leading zeros do not matter", []string{"python-3.014.0", "python-3.13.9"}, "python-3.014.0"},
		{"single match", []string{"python-3.13.5"}, "python-3.13.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := toolsHome(t, "python.exe", c.folders...)
			got := pickManaged(home, "python", pyGlob(home))
			if want := filepath.Join(home, "tools", c.want, "python.exe"); got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
	empty := t.TempDir()
	if got := pickManaged(empty, "python", pyGlob(empty)); got != "" {
		t.Errorf("no tools: got %q", got)
	}
}

func TestPickManagedPrefersFactsEntry(t *testing.T) {
	both := []string{"python-3.14.7+20260901-win32-x64", "python-3.14.10+20261201-win32-x64"}

	t.Run("facts names the older folder: it wins (it is the live one)", func(t *testing.T) {
		home := toolsHome(t, "python.exe", both...)
		writeFacts(t, home, factsFor("python", both[0]))
		if got, want := pickManaged(home, "python", pyGlob(home)), filepath.Join(home, "tools", both[0], "python.exe"); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
	t.Run("facts names the newer folder", func(t *testing.T) {
		home := toolsHome(t, "python.exe", both...)
		writeFacts(t, home, factsFor("python", both[1]))
		if got, want := pickManaged(home, "python", pyGlob(home)), filepath.Join(home, "tools", both[1], "python.exe"); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
	// every one of these must fall back to the version-aware pick (3.14.10)
	newest := func(home string) string { return filepath.Join(home, "tools", both[1], "python.exe") }
	for name, facts := range map[string]string{
		"entry folder is not on disk":        factsFor("python", "python-3.15.0-win32-x64"),
		"entry exists but has no python.exe": factsFor("python", "python-3.0.0-empty"),
		"entry is another tool":              factsFor("python", "node-1.0.0-win32-x64"),
		"entry escapes the tools folder":     factsFor("python", `..\\\\elsewhere`),
		"entry is a path":                    factsFor("python", "python-3.14.7/../python-3.14.10"),
		"entry is empty":                     factsFor("python", ""),
		"no python key":                      `{"packages": {"node": {"entry": "node-1.0.0"}}}`,
		"corrupt json":                       `{"packages": `,
		"packages is not an object":          `{"packages": []}`,
		"empty file":                         ``,
		"only git is recorded":               factsFor("git", both[0]),
	} {
		t.Run("fallback: "+name, func(t *testing.T) {
			home := toolsHome(t, "python.exe", both...)
			touch(t, filepath.Join(home, "tools", "python-3.0.0-empty", "readme.txt"), "")
			writeFacts(t, home, facts)
			if got := pickManaged(home, "python", pyGlob(home)); got != newest(home) {
				t.Errorf("got %s, want %s", got, newest(home))
			}
		})
	}
	t.Run("facts.json absent", func(t *testing.T) {
		home := toolsHome(t, "python.exe", both...)
		if got := pickManaged(home, "python", pyGlob(home)); got != newest(home) {
			t.Errorf("got %s", got)
		}
	})
}

func TestPickManagedGitUsesItsOwnFactsKey(t *testing.T) {
	rel := filepath.Join("mingw64", "bin", "git.exe")
	home := toolsHome(t, rel, "git-2.53.0+3-win32-x64", "git-2.100.0+1-win32-x64")
	touch(t, filepath.Join(home, "tools", "python-3.14.7", "python.exe"), "")
	glob := []string{filepath.Join(home, "tools", "git-*", rel)}
	if got, want := pickManaged(home, "git", glob), filepath.Join(home, "tools", "git-2.100.0+1-win32-x64", rel); got != want {
		t.Errorf("no facts: got %s, want %s", got, want)
	}
	writeFacts(t, home, `{"packages": {"git": {"entry": "git-2.53.0+3-win32-x64"}, "python": {"entry": "python-3.14.7"}}}`)
	if got, want := pickManaged(home, "git", glob), filepath.Join(home, "tools", "git-2.53.0+3-win32-x64", rel); got != want {
		t.Errorf("facts: got %s, want %s", got, want)
	}
}

func TestPickManagedPosixLayoutPicksFirstPatternThatExists(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "tools", "python-3.14.7", "bin", "python"), "")
	pats := []string{
		filepath.Join(home, "tools", "python-*", "bin", "python3"),
		filepath.Join(home, "tools", "python-*", "bin", "python"),
	}
	writeFacts(t, home, factsFor("python", "python-3.14.7"))
	if got, want := pickManaged(home, "python", pats), filepath.Join(home, "tools", "python-3.14.7", "bin", "python"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNaturalCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"python-3.14.7", "python-3.14.10", -1},
		{"python-3.14.10", "python-3.14.7", 1},
		{"python-3.9", "python-3.14", -1},
		{"a1", "a1", 0},
		{"a1", "a01", 0},
		{"a", "a1", -1},
		{"git-2.53.0+3", "git-2.53.0+12", -1},
		{"x-20260901", "x-20261201", -1},
		{"12345678901234567890123", "2", 1}, // longer than any int
		{"abc", "abd", -1},
		{"", "a", -1},
		{"", "", 0},
	} {
		if got := naturalCompare(c.a, c.b); got != c.want {
			t.Errorf("naturalCompare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestWithWildcardFolder(t *testing.T) {
	p := filepath.Join("h", "tools", "python-*", "python.exe")
	if got, want := withWildcardFolder(p, "python-3.14.7"), filepath.Join("h", "tools", "python-3.14.7", "python.exe"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := withWildcardFolder(p, "node-1.0"); got != "" {
		t.Errorf("a folder the wildcard does not match must not be used: %q", got)
	}
	if got := withWildcardFolder(filepath.Join("h", "tools", "python-3", "python.exe"), "python-3"); got != "" {
		t.Errorf("a pattern without wildcard: %q", got)
	}
}

// ManagedPython is asked again after `hermes update` (hook env): it must see
// the interpreter the update made live.
func TestManagedPythonFollowsFactsAfterABump(t *testing.T) {
	old, bumped := "python-3.14.7+20260901-win32-x64", "python-3.14.10+20261201-win32-x64"
	home := toolsHome(t, "python.exe", old)
	paths := &platform.FakePaths{Home: home, PythonGlobs: pyGlob(home)}
	writeFacts(t, home, factsFor("python", old))
	if got := ManagedPython(paths, home); !strings.HasSuffix(got, filepath.Join(old, "python.exe")) {
		t.Fatalf("before the bump: %s", got)
	}
	touch(t, filepath.Join(home, "tools", bumped, "python.exe"), "")
	writeFacts(t, home, factsFor("python", bumped))
	if got := ManagedPython(paths, home); !strings.HasSuffix(got, filepath.Join(bumped, "python.exe")) {
		t.Fatalf("after the bump: %s", got)
	}
	if got := ManagedPython(paths, ""); got != "" {
		t.Errorf("no home: %q", got)
	}
}

func TestLocateUsesFactsForPythonAndGit(t *testing.T) {
	f := newFixture(t)
	f.makeCheckout(t)
	launcher := touch(t, filepath.Join(f.home, "bin", "hermes.exe"), "")
	f.paths.Launchers = []string{launcher}
	f.run.On([]string{launcher, "--version"}, versionOut(f.checkout))
	rel := filepath.Join("cmd", "git.exe")
	livePy := touch(t, filepath.Join(f.home, "tools", "python-3.14.7", "python.exe"), "")
	touch(t, filepath.Join(f.home, "tools", "python-3.14.10", "python.exe"), "")
	liveGit := touch(t, filepath.Join(f.home, "tools", "git-2.53.0", rel), "")
	touch(t, filepath.Join(f.home, "tools", "git-2.100.0", rel), "")
	writeFacts(t, f.home, `{"packages": {"python": {"entry": "python-3.14.7"}, "git": {"entry": "git-2.53.0"}}}`)
	f.paths.PythonGlobs = pyGlob(f.home)
	f.paths.GitGlobs = []string{filepath.Join(f.home, "tools", "git-*", rel)}

	got, err := f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Python != livePy || got.Git != liveGit {
		t.Errorf("Python %s, Git %s; want %s, %s", got.Python, got.Git, livePy, liveGit)
	}
	// without the record the highest version wins, not the highest name
	if err := os.Remove(filepath.Join(f.home, "tools", ToolsFactsFile)); err != nil {
		t.Fatal(err)
	}
	got, err = f.loc.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Python, "python-3.14.10") || !strings.Contains(got.Git, "git-2.100.0") {
		t.Errorf("fallback: Python %s, Git %s", got.Python, got.Git)
	}
}
