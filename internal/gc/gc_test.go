package gc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

func newCollector(t *testing.T, r execx.Runner) (*Service, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(Deps{Runner: r, Python: "py-under-test", Home: home, Checkout: filepath.Join(home, "hermes-agent"), Applicable: true}), home
}

func TestRunSkips(t *testing.T) {
	ctx := context.Background()
	t.Run("not applicable on this OS", func(t *testing.T) {
		f := &execx.Fake{}
		c, _ := newCollector(t, f)
		c.d.Applicable = false
		res, err := c.Run(ctx, true)
		if err != nil || res.Skipped == "" || !strings.Contains(res.Skipped, "not needed") {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if len(f.Calls()) != 0 {
			t.Fatal("ran something")
		}
	})
	t.Run("managed Python missing (D7): skipped, no error, nothing run", func(t *testing.T) {
		f := &execx.Fake{}
		c, _ := newCollector(t, f)
		c.d.Python = ""
		res, err := c.Run(ctx, false)
		if err != nil || res.Skipped != "managed Python not found" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if len(f.Calls()) != 0 {
			t.Fatal("ran something")
		}
	})
}

func TestDryRunParsesSizeReport(t *testing.T) {
	f := &execx.Fake{}
	c, home := newCollector(t, f)
	out := "  aaaa1111: keep (selected)\n  bbbb2222: REMOVE (~120 MB unique)\n" +
		"  cccc3333: keep (younger than 24 h: 3 h)\n  dddd4444: REMOVE (~30 MB unique)\n  trash batches waiting: 1\n"
	f.OnPrefix([]string{"py-under-test"}, execx.Result{Output: out})
	res, err := c.Run(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 2 || res.MB != 150 {
		t.Fatalf("res = %+v", res)
	}
	if res.Line != "2 old dependency set(s), ~150 MB to free" || len(res.Lines) != 5 {
		t.Fatalf("Line=%q Lines=%d", res.Line, len(res.Lines))
	}
	argv := f.Calls()[0].Argv
	if argv[1] != "-I" || !strings.HasSuffix(filepath.ToSlash(argv[2]), "cache/safe-update-gc.py") ||
		argv[3] != "--home" || argv[4] != home || argv[5] != "--checkout" || argv[len(argv)-1] != "--dry-run" {
		t.Fatalf("argv = %q", argv)
	}
	if !isFile(argv[2]) {
		t.Fatal("script not written")
	}
}

func TestDryRunNothingToRemove(t *testing.T) {
	f := &execx.Fake{}
	c, _ := newCollector(t, f)
	f.OnPrefix([]string{"py-under-test"}, execx.Result{Output: "  aaaa: keep (selected)\n  trash batches waiting: 0\n"})
	res, err := c.Run(context.Background(), true)
	if err != nil || res.Count != 0 || res.MB != 0 || res.Line != "nothing to remove yet" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestRealRunParsing(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		code  int
		count int
		mb    int
		line  string
		err   string
	}{
		{"removed and freed",
			"removed aaaa1111 (2 locked file(s) moved to trash)\ngeneration cleanup: 2 dependency generation(s), 1 PM runtime generation(s) removed; disk free change +340 MB\n",
			0, 2, 340, "removed 2 old dependency set(s), freed 340 MB", ""},
		{"negative change clamps to 0 MB",
			"generation cleanup: 1 dependency generation(s), 0 PM runtime generation(s) removed; disk free change -12 MB\n",
			0, 1, 0, "removed 1 old dependency set(s), freed 0 MB", ""},
		{"nothing removed",
			"generation cleanup: 0 dependency generation(s), 0 PM runtime generation(s) removed; disk free change +0 MB\n",
			0, 0, 0, "nothing to remove", ""},
		{"trash line is kept as detail",
			"trash: purged 1 batch(es), 0 still locked (kept for a later run)\ngeneration cleanup: 0 dependency generation(s), 0 PM runtime generation(s) removed; disk free change +5 MB\n",
			0, 0, 5, "nothing to remove", ""},
		{"collector error", "generation cleanup error: PermissionError: x\n", 1, 0, 0, "", "generation cleanup rc=1"},
		{"helpers cannot load", "generation cleanup: cannot load Hermes helpers (No module named 'pm'); skipped\n", 1, 0, 0, "", "cannot load Hermes helpers"},
		{"unrecognised output", "something else entirely\n", 0, 0, 0, "", "output not recognised"},
		{"timeout", "timed out after 15m0s", 124, 0, 0, "", "generation cleanup rc=124"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execx.Fake{}
			col, _ := newCollector(t, f)
			f.OnPrefix([]string{"py-under-test"}, execx.Result{Output: c.out, Code: c.code})
			res, err := col.Run(context.Background(), false)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				if !strings.Contains(err.Error(), "the update itself is unaffected") && c.code != 0 {
					t.Fatalf("error should reassure: %v", err)
				}
				return
			}
			if err != nil || res.Count != c.count || res.MB != c.mb || res.Line != c.line {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			for _, a := range f.Calls()[0].Argv {
				if a == "--dry-run" {
					t.Fatal("real run passed --dry-run")
				}
			}
		})
	}
}

func TestRunUsesGenerousTimeout(t *testing.T) {
	f := &execx.Fake{}
	c, _ := newCollector(t, f)
	f.OnPrefix([]string{"py-under-test"}, execx.Result{})
	_, _ = c.Run(context.Background(), false)
	if got := f.Calls()[0].Timeout.Minutes(); got != 15 {
		t.Fatalf("timeout = %v min", got)
	}
	if f.Calls()[0].ShowWindow {
		t.Fatal("helper must not show a window")
	}
}

// ---- the embedded script itself, under a real Python and a fake Hermes ----

func python(t *testing.T) string {
	t.Helper()
	for _, n := range []string{"python3", "python"} {
		if p, err := exec.LookPath(n); err == nil {
			out, err := exec.Command(p, "-c", "import sys; print(sys.version_info >= (3, 12))").Output()
			if err == nil && strings.TrimSpace(string(out)) == "True" {
				return p
			}
		}
	}
	t.Skip("no Python >= 3.12 on PATH")
	return ""
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const fakeRuntimeState = `
import shutil, pathlib
def collect_generations(checkout):
    root = pathlib.Path(checkout).parent / "installs" / "x" / "environments"
    removed = []
    for gen in sorted(root.iterdir()):
        if gen.name.startswith("old"):
            shutil.rmtree(gen)
            removed.append(gen.name)
    return removed
def leases_held(gen):
    return gen.name == "old-leased"
`

// fakeHermes builds <home>/hermes-agent with importable hermes_cli and pm
// packages and three generations: selected, old-a (removable), old-leased.
func fakeHermes(t *testing.T) (home string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	co := filepath.Join(home, "hermes-agent")
	writeFile(t, filepath.Join(co, "hermes_cli", "__init__.py"), "")
	writeFile(t, filepath.Join(co, "hermes_cli", "runtime_state.py"), fakeRuntimeState)
	writeFile(t, filepath.Join(co, "pm", "__init__.py"), "")
	writeFile(t, filepath.Join(co, "pm", "runtime.py"), "def collect_runtime_generations(p):\n    return []\n")
	writeFile(t, filepath.Join(co, "pm", "environments.py"), `
import pathlib
def install_state_dir(checkout):
    return pathlib.Path(checkout).parent / "installs" / "x"
def selected_venv(checkout):
    return pathlib.Path(checkout).parent / "installs" / "x" / "environments" / "sel" / "venv"
`)
	envs := filepath.Join(home, "installs", "x", "environments")
	for _, g := range []string{"sel", "old-a", "old-leased"} {
		writeFile(t, filepath.Join(envs, g, ".lease-managed"), "")
		writeFile(t, filepath.Join(envs, g, "venv", "lib.txt"), strings.Repeat("z", 2048))
		old := filepath.Join(envs, g, ".lease-managed")
		// older than 24 h
		if err := os.Chtimes(old, fileEpoch, fileEpoch); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func realCollector(t *testing.T, home string) *Service {
	return New(Deps{Runner: execx.New(), Python: python(t), Home: home, Checkout: filepath.Join(home, "hermes-agent"), Applicable: true})
}

func TestScriptDryRunAgainstFakeHermes(t *testing.T) {
	home := fakeHermes(t)
	res, err := realCollector(t, home).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("res = %+v lines=%q", res, res.Lines)
	}
	joined := strings.Join(res.Lines, "\n")
	for _, want := range []string{"sel: keep (selected)", "old-leased: keep (lease held by a running process)", "old-a: REMOVE"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	if !isDir(filepath.Join(home, "installs", "x", "environments", "old-a")) {
		t.Fatal("dry run deleted something")
	}
}

func TestScriptRealRunRemovesGenerationAndKeepsLeased(t *testing.T) {
	home := fakeHermes(t)
	res, err := realCollector(t, home).Run(context.Background(), false)
	if err != nil {
		t.Fatalf("err = %v lines=%q", err, res.Lines)
	}
	envs := filepath.Join(home, "installs", "x", "environments")
	if isDir(filepath.Join(envs, "old-a")) {
		t.Fatal("old-a not removed")
	}
	if !isDir(filepath.Join(envs, "sel")) {
		t.Fatal("selected generation removed")
	}
	if res.Count != 2 { // the fake collector removes every "old*" dir, leased or not
		t.Fatalf("res = %+v", res)
	}
}

func TestScriptReadOnlyFileIsRecovered(t *testing.T) {
	home := fakeHermes(t)
	ro := filepath.Join(home, "installs", "x", "environments", "old-a", "venv", "lib.txt")
	if err := os.Chmod(ro, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := realCollector(t, home).Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if isDir(filepath.Join(home, "installs", "x", "environments", "old-a")) {
		t.Fatal("read-only generation not removed")
	}
}

// A file the OS refuses to delete (a loaded .pyd on Windows) is moved to the
// trash dir instead, and a later run purges it. The refusal is simulated by
// patching os.unlink inside the script's own process.
func TestScriptLockedFileIsQuarantinedThenPurged(t *testing.T) {
	py := python(t)
	home := fakeHermes(t)
	script := filepath.Join(t.TempDir(), "gc.py")
	if err := os.WriteFile(script, []byte(Script), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "drive.py")
	writeFile(t, driver, `
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("gcmod", sys.argv[1])
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
real_unlink = os.unlink
def deny(p, *a, **k):
    if str(p).endswith("lib.txt"):
        raise PermissionError(5, "Access is denied", str(p))
    return real_unlink(p, *a, **k)
os.unlink = deny
sys.argv = ["gc.py", "--home", sys.argv[2]]
print("RC", m.main())
os.unlink = real_unlink
print("RC2", m.main())
`)
	out, err := exec.Command(py, "-I", driver, script, home).CombinedOutput()
	if err != nil {
		t.Fatalf("driver: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "removed old-a (1 locked file(s) moved to trash)") || !strings.Contains(s, "RC 0") {
		t.Fatalf("first run:\n%s", s)
	}
	if !strings.Contains(s, "trash: purged 2 batch(es), 0 still locked") {
		t.Fatalf("second run did not purge the trash:\n%s", s)
	}
	if isDir(filepath.Join(home, "cache", "safe-update-trash")) {
		t.Fatal("trash dir not removed once empty")
	}
}

func TestScriptCannotLoadHermes(t *testing.T) {
	home := t.TempDir()
	res, err := realCollector(t, home).Run(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "cannot load Hermes helpers") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

var fileEpoch = time.Unix(1_000_000_000, 0)

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
