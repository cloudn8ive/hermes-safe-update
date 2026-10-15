package hermes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

const sha40 = "4e7403130ee278bd99c450fcd9f73c6b32135c95"

func gitArgv(rest ...string) []string {
	return append([]string{`G:\git.exe`, "-c", "gc.auto=0", "--no-optional-locks", "-C", `C:\ck`}, rest...)
}

func newRepo(f *execx.Fake) *GitRepo {
	return &GitRepo{Git: `G:\git.exe`, Checkout: `C:\ck`, Run: f}
}

func TestRepoHead(t *testing.T) {
	cases := []struct {
		name         string
		short, full  execx.Result
		wantS, wantF string
		wantErr      bool
	}{
		{"ok", execx.Result{Output: "4e74031\n"}, execx.Result{Output: sha40 + "\r\n"}, "4e74031", sha40, false},
		{"full sha of wrong length is dropped", execx.Result{Output: "4e74031\n"}, execx.Result{Output: "4e74031\n"}, "4e74031", "", false},
		{"git fails", execx.Result{Code: 128, Output: "fatal: not a git repository"}, execx.Result{Code: 128}, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execx.Fake{}
			f.On(gitArgv("rev-parse", "--short", "HEAD"), c.short)
			f.On(gitArgv("rev-parse", "HEAD"), c.full)
			s, full, err := newRepo(f).Head(context.Background())
			if (err != nil) != c.wantErr || s != c.wantS || full != c.wantF {
				t.Errorf("got %q %q %v", s, full, err)
			}
		})
	}
}

func TestRepoDirtyCount(t *testing.T) {
	cases := []struct {
		out  string
		code int
		want int
		err  bool
	}{
		{"", 0, 0, false},
		{" M a.go\n M b.go\n\n", 0, 2, false},
		{" M a.go\r\n?? c\r\n", 0, 2, false},
		{"fatal", 128, 0, true},
	}
	for _, c := range cases {
		f := &execx.Fake{}
		f.On(gitArgv("status", "--porcelain", "--untracked-files=no"), execx.Result{Output: c.out, Code: c.code})
		n, err := newRepo(f).DirtyCount(context.Background())
		if n != c.want || (err != nil) != c.err {
			t.Errorf("%q: got %d, %v", c.out, n, err)
		}
	}
}

func TestRepoDiffKindsUsesExitCodesOnly(t *testing.T) {
	b := func(v bool) *bool { return &v }
	pyArgv := gitArgv(append([]string{"diff", "--quiet", "HEAD", "origin/main", "--"}, PyFilesPathspecs...)...)
	npmArgv := gitArgv("diff", "--quiet", "HEAD", "origin/main", "--", ":(glob)**/package.json", ":(glob)**/package-lock.json")
	cases := []struct {
		name         string
		npm, py      execx.Result
		wantN, wantP *bool
	}{
		{"both unchanged", execx.Result{Code: 0}, execx.Result{Code: 0}, b(false), b(false)},
		{"npm changed", execx.Result{Code: 1}, execx.Result{Code: 0}, b(true), b(false)},
		{"py changed with noisy stderr", execx.Result{Code: 0}, execx.Result{Code: 1, Output: "warning: packfile foo"}, b(false), b(true)},
		{"timeout is unknown", execx.Result{Code: 124, TimedOut: true}, execx.Result{Code: 128}, nil, nil},
		{"exit 0 with noise is still unchanged", execx.Result{Code: 0, Output: "warning: noise"}, execx.Result{Code: 0, Output: "warning: noise"}, b(false), b(false)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execx.Fake{}
			f.On(npmArgv, c.npm)
			f.On(pyArgv, c.py)
			n, p := newRepo(f).DiffKinds(context.Background(), MainRef)
			eq := func(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
			if !eq(n, c.wantN) || !eq(p, c.wantP) {
				t.Errorf("got %v %v", n, p)
			}
		})
	}
}

func TestRepoAlwaysDisablesAutoGC(t *testing.T) {
	f := &execx.Fake{}
	f.OnPrefix([]string{`G:\git.exe`}, execx.Result{})
	r := newRepo(f)
	_, _, _ = r.Head(context.Background())
	_, _ = r.DirtyCount(context.Background())
	r.DiffKinds(context.Background(), MainRef)
	for _, c := range f.Calls() {
		if strings.Join(c.Argv[1:6], " ") != `-c gc.auto=0 --no-optional-locks -C C:\ck` {
			t.Errorf("argv = %v", c.Argv)
		}
	}
}

func TestRepoWithoutGit(t *testing.T) {
	r := &GitRepo{Checkout: `C:\ck`, Run: &execx.Fake{}}
	if _, _, err := r.Head(context.Background()); err == nil {
		t.Error("want error without git")
	}
	if n, p := r.DiffKinds(context.Background(), MainRef); n != nil || p != nil {
		t.Error("want unknown without git")
	}
}

func TestChangedFilesKinds(t *testing.T) {
	b := func(v bool) *bool { return &v }
	eq := func(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	cases := []struct {
		name         string
		files        []string
		complete     bool
		wantN, wantP *bool
	}{
		{"nothing relevant, complete", []string{"a.go", "docs/x.md"}, true, b(false), b(false)},
		{"nothing relevant, partial list proves nothing", []string{"a.go"}, false, nil, nil},
		{"nested package.json", []string{"apps/desktop/package.json"}, false, b(true), nil},
		{"package-lock", []string{"package-lock.json"}, true, b(true), b(false)},
		{"uv.lock at root", []string{"uv.lock"}, false, nil, b(true)},
		{"pyproject nested does not count", []string{"sub/pyproject.toml"}, true, b(false), b(false)},
		{"both", []string{"pyproject.toml", "web/package.json"}, false, b(true), b(true)},
		// Hermes' package manager also re-resolves when pm/lock.json or the manifest of a
		// plugin that declares Python packages changes (a plugin moving out of core)
		{"plugin manifest deleted", []string{"plugins/memory/holographic/plugin.yaml"}, true, b(false), b(true)},
		{"plugin manifest at the top", []string{"plugins/x/plugin.yml"}, false, nil, b(true)},
		{"plugin pyproject", []string{"plugins/x/y/pyproject.toml"}, true, b(false), b(true)},
		{"plugin json manifest", []string{"plugins/x/plugin.json"}, true, b(false), b(true)},
		{"pm lock", []string{"pm/lock.json"}, true, b(false), b(true)},
		{"plugin code alone", []string{"plugins/x/__init__.py", "plugins/x/README.md", "plugins/x/plugin.yaml.bak"}, true, b(false), b(false)},
		{"look-alikes elsewhere", []string{"apps/plugins/x/plugin.yaml", "plugin-catalog/x.yaml", "pm/uv.lock", "pm/pyproject.toml"}, true, b(false), b(false)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, p := ClassifyChanged(c.files, c.complete)
			if !eq(n, c.wantN) || !eq(p, c.wantP) {
				t.Errorf("got %v %v", n, p)
			}
		})
	}
}

func TestInterpretUpdateCheck(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		code     int
		behind   *bool
		commits  *int
		wantNilB bool
	}{
		{"behind", "Update available: 58 commits behind — run 'hermes update'", 0, ptrB(true), ptrI(58), false},
		{"one commit", "1 commit behind", 0, ptrB(true), ptrI(1), false},
		{"up to date", "Hermes is up to date", 0, ptrB(false), nil, false},
		{"available without count", "An update is available", 0, ptrB(true), nil, false},
		{"rc!=0 means the check itself failed", "boom", 1, nil, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := InterpretUpdateCheck(c.out, c.code)
			if u.Source != "git" {
				t.Errorf("source = %q", u.Source)
			}
			if (u.Behind == nil) != c.wantNilB || (u.Behind != nil && *u.Behind != *c.behind) {
				t.Errorf("behind = %v", u.Behind)
			}
			if (u.Commits == nil) != (c.commits == nil) || (u.Commits != nil && *u.Commits != *c.commits) {
				t.Errorf("commits = %v", u.Commits)
			}
			if !strings.Contains(u.Detail, "update --check (rc=") {
				t.Errorf("detail = %q", u.Detail)
			}
		})
	}
}

func ptrB(v bool) *bool { return &v }
func ptrI(v int) *int   { return &v }

// The git pathspecs (used by `git diff --quiet`) and the regex (used for the
// API file list) must agree: run both against a real repository.
func TestPyFilesPathspecsAgreeWithPattern(t *testing.T) {
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	paths := []string{
		"uv.lock", "pyproject.toml", "pm/lock.json", "pm/uv.lock", "sub/pyproject.toml", "package.json",
		"plugins/plugin.yaml", "plugins/a/plugin.yaml", "plugins/a/plugin.yml", "plugins/a/plugin.json",
		"plugins/memory/b/pyproject.toml", "plugins/memory/b/store.py", "plugins/a/plugin.yaml.bak",
		"apps/plugins/a/plugin.yaml", "plugin-catalog/a.yaml",
	}
	git("init", "-q", "-b", "main")
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	repo := &GitRepo{Git: bin, Checkout: dir, Run: execx.New()}
	for _, p := range paths {
		git("checkout", "-q", "-f", "--detach", base)
		if p == "plugins/a/plugin.yaml" { // a deleted manifest counts too
			git("rm", "-q", p)
		} else if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte("two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", "change "+p)
		git("update-ref", "refs/remotes/origin/main", "HEAD")
		git("checkout", "-q", "-f", "--detach", base)
		_, py := repo.DiffKinds(context.Background(), MainRef)
		_, want := ClassifyChanged([]string{p}, true)
		if py == nil || want == nil || *py != *want {
			t.Errorf("%s: git pathspecs say %s, PyFilesRe says %s", p, ptrStr(py), ptrStr(want))
		}
	}
}

func TestRepoCommitCount(t *testing.T) {
	const other = "0123456789abcdef0123456789abcdef01234567"
	rl := gitArgv("rev-list", "--count", sha40+".."+other)
	cases := []struct {
		name     string
		from, to string
		res      execx.Result
		want     int
		ok       bool
	}{
		{"ok", sha40, other, execx.Result{Output: "92\r\n"}, 92, true},
		{"zero is a real answer", sha40, other, execx.Result{Output: "0\n"}, 0, true},
		{"git fails (missing object)", sha40, other, execx.Result{Code: 128, Output: "fatal: bad revision"}, 0, false},
		{"timed out", sha40, other, execx.Result{TimedOut: true}, 0, false},
		{"git failed but printed a number", sha40, other, execx.Result{Code: 1, Output: "7\n"}, 0, false},
		{"not a number", sha40, other, execx.Result{Output: "warning: x\n"}, 0, false},
		{"short sha is refused without running git", "abc", other, execx.Result{Output: "5"}, 0, false},
		{"empty sha is refused", sha40, "", execx.Result{Output: "5"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execx.Fake{}
			f.On(rl, c.res)
			n, ok := newRepo(f).CommitCount(context.Background(), c.from, c.to)
			if n != c.want || ok != c.ok {
				t.Errorf("got %d, %v", n, ok)
			}
			if len(c.from) != 40 || len(c.to) != 40 {
				if len(f.Calls()) != 0 {
					t.Errorf("git ran for a malformed sha: %v", f.Calls())
				}
			}
		})
	}
}
