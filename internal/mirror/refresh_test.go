package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

func TestAPIMainSHA(t *testing.T) {
	ctx := context.Background()
	sha := strings.Repeat("a1", 20)
	cases := []struct {
		name string
		main string
		want string
	}{
		{"ok", sha, sha},
		{"server error", "", ""},
		{"not a sha", "<html>rate limited</html>", ""},
		{"short sha", "abc123", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			api := newAPI(t)
			api.Main = c.main
			d := e.deps()
			d.APIBase = api.URL
			got, ok := New(d).APIMainSHA(ctx)
			if got != c.want || ok != (c.want != "") {
				t.Fatalf("got %q ok=%v", got, ok)
			}
			if len(api.UA) != 1 || api.UA[0] != "hermes-safe-update" {
				t.Fatalf("User-Agent = %v", api.UA)
			}
		})
	}
}

func TestAPICompare(t *testing.T) {
	ctx := context.Background()
	files := func(n int) string {
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, `{"filename":"f`+itoa(i)+`.py"}`)
		}
		return strings.Join(parts, ",")
	}
	cases := []struct {
		name     string
		body     string
		wantNil  bool
		commits  int
		nfiles   int
		complete bool
	}{
		{"ahead", `{"status":"ahead","ahead_by":3,"files":[` + files(2) + `]}`, false, 3, 2, true},
		{"identical", `{"status":"identical","ahead_by":0,"files":[]}`, false, 0, 0, true},
		{"299 files is complete", `{"status":"ahead","ahead_by":9,"files":[` + files(299) + `]}`, false, 9, 299, true},
		{"300 files is partial", `{"status":"ahead","ahead_by":9,"files":[` + files(300) + `]}`, false, 9, 300, false},
		{"diverged", `{"status":"diverged","ahead_by":1,"files":[]}`, true, 0, 0, false},
		{"ahead_by missing", `{"status":"ahead"}`, true, 0, 0, false},
		{"not json", `nope`, true, 0, 0, false},
		{"server error", ``, true, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			api := newAPI(t)
			api.Compare = c.body
			d := e.deps()
			d.APIBase = api.URL
			got := New(d).APICompare(ctx, "deadbeef")
			if c.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil || got.Commits != c.commits || len(got.Files) != c.nfiles || got.FilesComplete != c.complete {
				t.Fatalf("got %+v", got)
			}
			if len(api.Paths) != 1 || !strings.Contains(api.Paths[0], "/compare/deadbeef...main") {
				t.Fatalf("paths = %v", api.Paths)
			}
		})
	}
	t.Run("unreachable server", func(t *testing.T) {
		e := newEnv(t)
		api := newAPI(t)
		d := e.deps()
		d.APIBase = api.URL
		api.Close()
		if New(d).APICompare(ctx, "x") != nil {
			t.Fatal("want nil")
		}
	})
}

// mirrorOf makes a bare mirror clone of upstream and records it as this
// machine's mirror.
func mirrorOf(t *testing.T, e *env, upstream string) string {
	t.Helper()
	m := filepath.Join(t.TempDir(), "mirror.git")
	g(t, filepath.Dir(m), "clone", "--bare", "--single-branch", "--branch", "main", "--no-tags", upstream, m)
	saveMachine(t, e, m)
	return m
}

func TestRefreshFetchesNewCommitsAndRecordsState(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	upstream := newBare(t, "up")
	work := newWork(t, upstream)
	commit(t, work, "a.txt", "1")
	g(t, work, "push", "origin", "main")
	m := mirrorOf(t, e, upstream)

	tip := commit(t, work, "b.txt", "2")
	g(t, work, "tag", "v1.0")
	g(t, work, "push", "origin", "main", "--tags")

	if err := New(e.realDeps()).Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := g(t, m, "rev-parse", "refs/heads/main"); got != tip {
		t.Fatalf("mirror tip = %s, want %s", got, tip)
	}
	if got := g(t, m, "tag", "-l", "v*"); got != "v1.0" {
		t.Fatalf("tags = %q", got)
	}
	st := New(e.deps()).Status()
	if st.LastOK.IsZero() || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	if isFile(filepath.Join(e.home, "cache", lockFile)) {
		t.Fatal("lock left behind")
	}
}

func TestRefreshFailureRecordsErrorAndReleasesLock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 128, Output: "fatal: unable to access remote"})
	err := New(e.deps()).Refresh(ctx)
	if err == nil || !strings.Contains(err.Error(), "git fetch rc=128") {
		t.Fatalf("err = %v", err)
	}
	st := New(e.deps()).Status()
	if !strings.Contains(st.LastError, "unable to access") {
		t.Fatalf("LastError = %q", st.LastError)
	}
	if isFile(filepath.Join(e.home, "cache", lockFile)) {
		t.Fatal("lock left behind")
	}
}

func TestRefreshArgvAndEnv(t *testing.T) {
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 0, Output: strings.Repeat("c", 40)})
	if err := New(e.deps()).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var fetch execx.Cmd
	for _, c := range e.runner.Calls() {
		for _, a := range c.Argv {
			if a == "fetch" {
				fetch = c
			}
		}
	}
	want := []string{"git-under-test", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-C", m,
		"fetch", "--prune", "--no-tags", "origin", "+refs/heads/main:refs/heads/main", "+refs/tags/v*:refs/tags/v*"}
	if strings.Join(fetch.Argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q", fetch.Argv)
	}
	if fetch.Env["GIT_TERMINAL_PROMPT"] != "0" || fetch.ShowWindow {
		t.Fatalf("env/window = %v / %v", fetch.Env, fetch.ShowWindow)
	}
}

func TestRefreshWithoutMirror(t *testing.T) {
	e := newEnv(t)
	if err := New(e.deps()).Refresh(context.Background()); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("err = %v", err)
	}
}

func TestCronRefresh(t *testing.T) {
	ctx := context.Background()
	sha := strings.Repeat("b2", 20)
	setup := func(t *testing.T) (*env, *apiServer, string) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m.git")
		makeBareShape(t, m)
		saveMachine(t, e, m)
		api := newAPI(t)
		return e, api, m
	}
	t.Run("not set up here: exit 0, nothing run", func(t *testing.T) {
		e := newEnv(t)
		if code := New(e.deps()).CronRefresh(ctx); code != 0 {
			t.Fatalf("code = %d", code)
		}
		if len(e.runner.Calls()) != 0 || len(e.out) != 0 {
			t.Fatalf("ran %v, printed %v", e.runner.Calls(), e.out)
		}
	})
	t.Run("update running: exit 0 without git", func(t *testing.T) {
		e, _, _ := setup(t)
		e.alive[777] = true
		_ = os.WriteFile(filepath.Join(e.home, ".hermes-update-in-progress"), []byte("777\n1790000000\n"), 0o644)
		if code := New(e.deps()).CronRefresh(ctx); code != 0 || len(e.runner.Calls()) != 0 {
			t.Fatalf("code = %d calls = %v", code, e.runner.Calls())
		}
	})
	t.Run("dead marker pid does not block", func(t *testing.T) {
		e, api, _ := setup(t)
		d := e.deps()
		d.APIBase = api.URL
		_ = os.WriteFile(filepath.Join(e.home, ".hermes-update-in-progress"), []byte("777\n1790000000\n"), 0o644)
		e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 0, Output: sha})
		if code := New(d).CronRefresh(ctx); code != 0 {
			t.Fatalf("code = %d", code)
		}
		if len(e.runner.Calls()) == 0 {
			t.Fatal("expected a git refresh")
		}
	})
	t.Run("API says current: no fetch", func(t *testing.T) {
		e, api, _ := setup(t)
		api.Main = sha
		d := e.deps()
		d.APIBase = api.URL
		e.runner.On([]string{"git-under-test", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-C", e.mirrorPathForTest(), "rev-parse", "refs/heads/main"},
			execx.Result{Output: sha + "\n"})
		if code := New(d).CronRefresh(ctx); code != 0 {
			t.Fatalf("code = %d", code)
		}
		for _, c := range e.runner.Calls() {
			for _, a := range c.Argv {
				if a == "fetch" {
					t.Fatalf("fetched: %v", c.Argv)
				}
			}
		}
	})
	t.Run("fetch fails: exit 1, silent, logged", func(t *testing.T) {
		e, api, _ := setup(t)
		d := e.deps()
		d.APIBase = api.URL
		e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 128, Output: "fatal: boom"})
		if code := New(d).CronRefresh(ctx); code != 1 {
			t.Fatalf("code = %d", code)
		}
		if len(e.out) != 0 {
			t.Fatalf("printed %v", e.out)
		}
		if len(e.logs) == 0 || !strings.Contains(e.logs[len(e.logs)-1], "refresh FAILED") {
			t.Fatalf("logs = %v", e.logs)
		}
	})
	t.Run("another refresh running: exit 0", func(t *testing.T) {
		e, api, _ := setup(t)
		d := e.deps()
		d.APIBase = api.URL
		e.alive[4242] = true
		_ = os.MkdirAll(filepath.Join(e.home, "cache"), 0o755)
		_ = os.WriteFile(filepath.Join(e.home, "cache", lockFile), []byte(fmtLock(4242, e.now.Now().Unix())), 0o644)
		if code := New(d).CronRefresh(ctx); code != 0 {
			t.Fatalf("code = %d", code)
		}
	})
}

// mirrorPathForTest reads back the path saveMachine wrote.
func (e *env) mirrorPathForTest() string {
	e.t.Helper()
	return New(e.deps()).mirrorPath()
}

// RT-4F: the Go mirror writes logs/update-mirror.log ("<ISO time> <message>")
// as the docs say, without failing the refresh and without console output.
func TestMirrorWritesItsLogFile(t *testing.T) {
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 0, Output: strings.Repeat("c", 40)})
	if err := New(e.deps()).Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(e.home, "logs", "update-mirror.log")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no mirror log: %v", err)
	}
	line := strings.TrimRight(string(b), "\r\n")
	if !strings.HasPrefix(line, "2026-10-01T12:00:00Z refresh ok: refreshed in ") || strings.ContainsAny(line, "\r\n") {
		t.Fatalf("log line = %q", line)
	}
	if len(e.out) != 0 {
		t.Fatalf("console output from a refresh: %v", e.out)
	}
	// a second line appends
	New(e.deps()).logf("second")
	b, _ = os.ReadFile(logPath)
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Fatalf("want 2 lines, got %q", b)
	}
}

// An unwritable log location must never fail the refresh.
func TestMirrorLogFailureIsHarmless(t *testing.T) {
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	// "logs" is a regular file, so the log directory cannot be created
	if err := os.WriteFile(filepath.Join(e.home, "logs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 0, Output: strings.Repeat("c", 40)})
	if err := New(e.deps()).Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed because of the log: %v", err)
	}
}
