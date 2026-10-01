package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// recRunner wraps the real runner and records every command.
type recRunner struct {
	inner execx.Runner
	mu    sync.Mutex
	calls []execx.Cmd
}

func (r *recRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	return r.inner.Run(ctx, c)
}

func (r *recRunner) Stream(ctx context.Context, c execx.Cmd, f func(string)) (execx.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	return r.inner.Stream(ctx, c, f)
}

// checkoutOf clones upstream and then points origin at the official URL, so
// it looks like a Hermes checkout whose only reachable copy is the mirror.
func checkoutOf(t *testing.T, upstream string) string {
	t.Helper()
	co := filepath.Join(t.TempDir(), "hermes-agent")
	g(t, filepath.Dir(co), "clone", upstream, co)
	g(t, co, "remote", "set-url", "origin", officialURL)
	return co
}

func TestSeedCopiesCommitsAndKeepsOrigin(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	upstream := newBare(t, "up")
	work := newWork(t, upstream)
	commit(t, work, "a.txt", "1")
	g(t, work, "push", "origin", "main")
	m := mirrorOf(t, e, upstream)
	co := checkoutOf(t, upstream)

	commit(t, work, "b.txt", "2")
	commit(t, work, "c.txt", "3")
	tip := g(t, work, "rev-parse", "HEAD")
	g(t, work, "tag", "v9.9")
	g(t, work, "push", "origin", "main", "--tags")

	api := newAPI(t)
	api.Main = tip // GitHub is ahead of the mirror: Seed must refresh first
	d := e.realDeps()
	d.APIBase = api.URL
	rec := &recRunner{inner: execx.New()}
	d.Runner = rec

	res := New(d).Seed(ctx, co)
	if !res.Used || res.Reason != "ok" {
		t.Fatalf("Seed = %+v (logs %v)", res, e.logs)
	}
	if res.Commits == nil || *res.Commits != 2 {
		t.Fatalf("Commits = %v", res.Commits)
	}
	if !res.Complete {
		t.Fatal("Complete = false: after seeding nothing should be left to download")
	}
	if got := g(t, co, "rev-parse", "origin/main"); got != tip {
		t.Fatalf("origin/main = %s, want %s", got, tip)
	}
	if got := g(t, co, "tag", "-l", "v9.9"); got != "v9.9" {
		t.Fatalf("tag missing: %q", got)
	}
	if got := g(t, co, "config", "--get", "remote.origin.url"); got != officialURL {
		t.Fatalf("origin URL changed to %q", got)
	}
	if got := g(t, m, "rev-parse", "refs/heads/main"); got != tip {
		t.Fatalf("mirror not refreshed: %s", got)
	}
	// The rewrite is on our own fetches only, as a -c option.
	var rewrites int
	for _, c := range rec.calls {
		j := strings.Join(c.Argv, " ")
		if strings.Contains(j, "insteadOf") {
			rewrites++
			if !strings.Contains(j, "url."+filepath.ToSlash(m)+".insteadOf="+officialURL) {
				t.Fatalf("rewrite argv = %v", c.Argv)
			}
		}
		for k, v := range c.Env {
			if strings.Contains(strings.ToLower(k+v), "insteadof") {
				t.Fatalf("insteadOf leaked into env: %s=%s", k, v)
			}
		}
	}
	if rewrites == 0 {
		t.Fatal("no fetch used the mirror")
	}
	// Nothing written to the checkout's own config either.
	cfg, _ := os.ReadFile(filepath.Join(co, ".git", "config"))
	if strings.Contains(strings.ToLower(string(cfg)), "insteadof") {
		t.Fatalf("insteadOf written to the checkout config:\n%s", cfg)
	}
}

func TestSeedAlreadyCurrentHasZeroCommits(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	upstream := newBare(t, "up")
	work := newWork(t, upstream)
	tip := commit(t, work, "a.txt", "1")
	g(t, work, "push", "origin", "main")
	mirrorOf(t, e, upstream)
	co := checkoutOf(t, upstream)
	api := newAPI(t)
	api.Main = tip
	d := e.realDeps()
	d.APIBase = api.URL
	res := New(d).Seed(ctx, co)
	if !res.Used || res.Commits == nil || *res.Commits != 0 {
		t.Fatalf("Seed = %+v", res)
	}
}

func TestSeedAPIUnavailableRefreshesAnyway(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	upstream := newBare(t, "up")
	work := newWork(t, upstream)
	commit(t, work, "a.txt", "1")
	g(t, work, "push", "origin", "main")
	mirrorOf(t, e, upstream)
	co := checkoutOf(t, upstream)
	tip := commit(t, work, "b.txt", "2")
	g(t, work, "push", "origin", "main")
	api := newAPI(t) // Main == "" -> 500
	d := e.realDeps()
	d.APIBase = api.URL
	res := New(d).Seed(ctx, co)
	if !res.Used || res.Commits == nil || *res.Commits != 1 {
		t.Fatalf("Seed = %+v", res)
	}
	if got := g(t, co, "rev-parse", "origin/main"); got != tip {
		t.Fatalf("origin/main = %s", got)
	}
}

func TestSeedReasons(t *testing.T) {
	ctx := context.Background()
	t.Run("no mirror set up", func(t *testing.T) {
		e := newEnv(t)
		res := New(e.deps()).Seed(ctx, t.TempDir())
		if res.Used || res.Reason != "no mirror set up" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("mirror folder missing", func(t *testing.T) {
		e := newEnv(t)
		gone := filepath.Join(t.TempDir(), "gone.git")
		saveMachine(t, e, gone)
		res := New(e.deps()).Seed(ctx, t.TempDir())
		if res.Used || res.Reason != "mirror not found at "+gone {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("checkout origin is a fork", func(t *testing.T) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m.git")
		makeBareShape(t, m)
		saveMachine(t, e, m)
		e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Output: "https://github.com/someone/hermes-agent.git\n"})
		res := New(e.deps()).Seed(ctx, t.TempDir())
		if res.Used || res.Reason != "checkout origin is not the official repo" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("official URL spellings are accepted", func(t *testing.T) {
		for _, u := range []string{"https://github.com/NousResearch/hermes-agent", "https://github.com/NousResearch/hermes-agent/", "git@github.com:NousResearch/hermes-agent.git", " https://github.com/NousResearch/hermes-agent.git\n"} {
			if !isOfficial(u) {
				t.Errorf("%q should be official", u)
			}
		}
		if isOfficial("https://github.com/NousResearch/hermes-agent-fork.git") {
			t.Error("fork accepted")
		}
	})
	t.Run("mirror has no main", func(t *testing.T) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m.git")
		makeBareShape(t, m)
		saveMachine(t, e, m)
		api := newAPI(t)
		d := e.deps()
		d.APIBase = api.URL
		e.runner.OnFunc([]string{"git-under-test"}, func(c execx.Cmd) execx.Result {
			if strings.Contains(strings.Join(c.Argv, " "), "config --get remote.origin.url") {
				return execx.Result{Output: officialURL}
			}
			return execx.Result{Code: 128, Output: "fatal"}
		})
		res := New(d).Seed(ctx, t.TempDir())
		if res.Used {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("refresh fails", func(t *testing.T) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m.git")
		makeBareShape(t, m)
		saveMachine(t, e, m)
		api := newAPI(t)
		d := e.deps()
		d.APIBase = api.URL
		e.runner.OnFunc([]string{"git-under-test"}, func(c execx.Cmd) execx.Result {
			j := strings.Join(c.Argv, " ")
			switch {
			case strings.Contains(j, "config --get remote.origin.url"):
				return execx.Result{Output: officialURL}
			case strings.Contains(j, "config --get remote.origin.promisor"):
				return execx.Result{Code: 1}
			case strings.Contains(j, " fetch "):
				return execx.Result{Code: 128, Output: "fatal: unable to access"}
			case strings.Contains(j, "rev-parse refs/heads/main"):
				return execx.Result{Output: strings.Repeat("a", 40)}
			}
			return execx.Result{}
		})
		res := New(d).Seed(ctx, t.TempDir())
		if res.Used || !strings.HasPrefix(res.Reason, "mirror refresh failed (git fetch rc=128") {
			t.Fatalf("%+v", res)
		}
	})
}

// partialScript drives a fake git for the partial-clone paths.
type partialScript struct {
	checkout  string
	fetches   int
	crashOnce bool
	argvs     []string
	filter    string
}

func (p *partialScript) fn(c execx.Cmd) execx.Result {
	j := strings.Join(c.Argv, " ")
	p.argvs = append(p.argvs, j)
	switch {
	case strings.Contains(j, "config --get remote.origin.url"):
		return execx.Result{Output: officialURL + "\n"}
	case strings.Contains(j, "config --get remote.origin.promisor"):
		return execx.Result{Output: "true\n"}
	case strings.Contains(j, "config --get remote.origin.partialclonefilter"):
		return execx.Result{Output: p.filter + "\n"}
	case strings.Contains(j, "rev-parse refs/heads/main"):
		return execx.Result{Output: strings.Repeat("c", 40) + "\n"}
	case strings.Contains(j, "refs/remotes/origin/main"):
		p.fetches++
		if p.crashOnce && p.fetches == 1 {
			return execx.Result{Code: 128, Output: "fatal: " + packCrash}
		}
		return execx.Result{}
	case strings.Contains(j, "rev-list --count"):
		return execx.Result{Output: "4\n"}
	}
	return execx.Result{}
}

func TestSeedPartialCloneGit253PromisorRetry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	api := newAPI(t)
	api.Main = strings.Repeat("c", 40)
	co := t.TempDir()
	packDir := filepath.Join(co, ".git", "objects", "pack")
	_ = os.MkdirAll(packDir, 0o755)
	for _, n := range []string{"pack-aaa.pack", "pack-bbb.pack"} {
		_ = os.WriteFile(filepath.Join(packDir, n), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(packDir, "pack-bbb.promisor"), nil, 0o644) // already marked

	ps := &partialScript{checkout: co, crashOnce: true, filter: "blob:none"}
	e.runner.OnFunc([]string{"git-under-test"}, ps.fn)
	d := e.deps()
	d.APIBase = api.URL
	res := New(d).Seed(ctx, co)
	if !res.Used || res.Commits == nil || *res.Commits != 4 {
		t.Fatalf("Seed = %+v logs=%v", res, e.logs)
	}
	if ps.fetches != 2 {
		t.Fatalf("fetch attempts = %d, want 2", ps.fetches)
	}
	if !isFile(filepath.Join(packDir, "pack-aaa.promisor")) {
		t.Fatal("pack-aaa not marked as a partial-clone pack")
	}
	joined := strings.Join(ps.argvs, "\n")
	if !strings.Contains(joined, "--filter=blob:none") {
		t.Fatalf("fetch did not pass the partial clone filter:\n%s", joined)
	}
	for _, want := range []string{"ls-tree -r -t origin/main", "diff --binary HEAD origin/main", "refs/tags/v*:refs/tags/v*"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("partial path missing %q:\n%s", want, joined)
		}
	}
	found := false
	for _, l := range e.logs {
		if strings.Contains(l, "marked 1 pack(s)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no promisor log line: %v", e.logs)
	}
}

func TestSeedPartialCrashWithoutMarkerTextIsAFailure(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	api := newAPI(t)
	api.Main = strings.Repeat("c", 40)
	e.runner.OnFunc([]string{"git-under-test"}, func(c execx.Cmd) execx.Result {
		j := strings.Join(c.Argv, " ")
		if strings.Contains(j, "refs/remotes/origin/main") {
			return execx.Result{Code: 128, Output: "fatal: something else"}
		}
		return (&partialScript{}).fn(c)
	})
	d := e.deps()
	d.APIBase = api.URL
	res := New(d).Seed(ctx, t.TempDir())
	if res.Used || !strings.HasPrefix(res.Reason, "seeding from the mirror failed: ") {
		t.Fatalf("Seed = %+v", res)
	}
}
