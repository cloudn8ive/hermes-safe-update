package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

const fakeHead = "0123456789abcdef0123456789abcdef01234567"

func TestNewerThanUsesTheAPIFirst(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		body      string
		wantAhead bool
		wantN     int
	}{
		{"ahead by 7", `{"status":"ahead","ahead_by":7,"files":[]}`, true, 7},
		{"identical", `{"status":"identical","ahead_by":0,"files":[]}`, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			api := newAPI(t)
			api.Compare = c.body
			d := e.deps()
			d.APIBase = api.URL
			got, ok := New(d).NewerThan(ctx, "", fakeHead)
			if !ok || got.Ahead != c.wantAhead || got.Commits == nil || *got.Commits != c.wantN {
				t.Fatalf("got %+v ok=%v", got, ok)
			}
			if len(api.Paths) != 1 || !strings.Contains(api.Paths[0], "/compare/"+fakeHead+"...main") {
				t.Fatalf("paths = %v", api.Paths)
			}
			if len(e.runner.Calls()) != 0 {
				t.Fatalf("git ran although the API answered: %v", e.runner.Calls())
			}
		})
	}
}

func TestNewerThanRefusesAnOddHead(t *testing.T) {
	e := newEnv(t)
	for _, h := range []string{"", "abc", "HEAD", fakeHead + "0", strings.ToUpper(fakeHead)} {
		if _, ok := New(e.deps()).NewerThan(context.Background(), "", h); ok {
			t.Errorf("head %q accepted", h)
		}
	}
	if len(e.runner.Calls()) != 0 {
		t.Errorf("something ran: %v", e.runner.Calls())
	}
}

// newerFixture is an upstream with two commits; the checkout has the first.
// The GitHub API is down (500), so only git can answer.
func newerFixture(t *testing.T) (d Deps, co, first, second string) {
	t.Helper()
	e := newEnv(t)
	upstream := newBare(t, "up")
	work := newWork(t, upstream)
	first = commit(t, work, "a.txt", "1")
	g(t, work, "push", "origin", "main")
	co = checkoutOf(t, upstream)
	second = commit(t, work, "b.txt", "2")
	g(t, work, "push", "origin", "main")
	d = e.realDeps()
	d.CloneURL = upstream
	d.APIBase = newAPI(t).URL // Compare == "" -> 500
	return d, co, first, second
}

func TestNewerThanFallsBackToLsRemote(t *testing.T) {
	ctx := context.Background()
	t.Run("main moved: newer, no number", func(t *testing.T) {
		d, co, first, _ := newerFixture(t)
		got, ok := New(d).NewerThan(ctx, co, first)
		if !ok || !got.Ahead || got.Commits != nil {
			t.Fatalf("got %+v ok=%v", got, ok)
		}
	})
	t.Run("main did not move", func(t *testing.T) {
		d, co, _, second := newerFixture(t)
		got, ok := New(d).NewerThan(ctx, co, second)
		if !ok || got.Ahead {
			t.Fatalf("got %+v ok=%v", got, ok)
		}
	})
	t.Run("remote tip is inside our own history: not newer", func(t *testing.T) {
		// the checkout is one commit past the remote tip (main was rewound)
		d, co, _, second := newerFixture(t)
		g(t, co, "fetch", d.CloneURL, "main")
		work := filepath.Join(t.TempDir(), "w2")
		g(t, filepath.Dir(work), "clone", d.CloneURL, work)
		ahead := commit(t, work, "c.txt", "3")
		g(t, co, "fetch", work, "main")
		if got := g(t, co, "rev-parse", "FETCH_HEAD"); got != ahead {
			t.Fatalf("fixture: %s != %s", got, ahead)
		}
		got, ok := New(d).NewerThan(ctx, co, ahead)
		if !ok || got.Ahead {
			t.Fatalf("got %+v ok=%v (remote tip %s)", got, ok, second)
		}
	})
	t.Run("API answers garbage: still falls back", func(t *testing.T) {
		e := newEnv(t)
		upstream := newBare(t, "up")
		work := newWork(t, upstream)
		first := commit(t, work, "a.txt", "1")
		g(t, work, "push", "origin", "main")
		commit(t, work, "b.txt", "2")
		g(t, work, "push", "origin", "main")
		d := e.realDeps()
		d.CloneURL = upstream
		api := newAPI(t)
		api.Compare = "<html>rate limited</html>"
		d.APIBase = api.URL
		got, ok := New(d).NewerThan(ctx, "", first)
		if !ok || !got.Ahead || got.Commits != nil {
			t.Fatalf("got %+v ok=%v", got, ok)
		}
	})
}

func TestNewerThanCannotTell(t *testing.T) {
	ctx := context.Background()
	t.Run("API down and ls-remote fails", func(t *testing.T) {
		e := newEnv(t)
		d := e.deps()
		d.APIBase = newAPI(t).URL
		e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 128, Output: "fatal: unable to access"})
		if got, ok := New(d).NewerThan(ctx, "", fakeHead); ok {
			t.Fatalf("answered %+v", got)
		}
	})
	t.Run("ls-remote prints no main", func(t *testing.T) {
		e := newEnv(t)
		d := e.deps() // APIBase empty: git only
		e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Output: fakeHead + "\trefs/heads/other\n"})
		if got, ok := New(d).NewerThan(ctx, "", fakeHead); ok {
			t.Fatalf("answered %+v", got)
		}
	})
	t.Run("git cannot start", func(t *testing.T) {
		e := newEnv(t)
		d := e.deps()
		d.Git = filepath.Join(t.TempDir(), "no-such-git")
		d.Runner = execx.New()
		if got, ok := New(d).NewerThan(ctx, "", fakeHead); ok {
			t.Fatalf("answered %+v", got)
		}
	})
	t.Run("cancelled context returns at once", func(t *testing.T) {
		e := newEnv(t)
		api := newAPI(t)
		api.Compare = `{"status":"ahead","ahead_by":3}`
		d := e.deps()
		d.APIBase = api.URL
		e.runner.OnPrefix([]string{"git-under-test"}, execx.Result{Code: 128})
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		start := time.Now()
		if _, ok := New(d).NewerThan(cctx, "", fakeHead); ok {
			t.Fatal("answered on a cancelled context")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("took %v", time.Since(start))
		}
	})
	t.Run("the limits stay short", func(t *testing.T) {
		if newerAPITimeout+newerGitTimeout > 10*time.Second {
			t.Errorf("the courtesy check may hold the summary back for %v", newerAPITimeout+newerGitTimeout)
		}
	})
}

// The look must not change the checkout (HEAD, index, refs, working tree).
func TestNewerThanChangesNothing(t *testing.T) {
	d, co, first, _ := newerFixture(t)
	snap := func() string {
		return g(t, co, "rev-parse", "HEAD") + "|" + g(t, co, "status", "--porcelain") + "|" +
			g(t, co, "for-each-ref") + "|" + g(t, co, "ls-files", "-s")
	}
	idx, _ := os.ReadFile(filepath.Join(co, ".git", "index"))
	before := snap()
	if got, ok := New(d).NewerThan(context.Background(), co, first); !ok || !got.Ahead {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if after := snap(); after != before {
		t.Errorf("checkout changed:\n%s\n%s", before, after)
	}
	if idx2, _ := os.ReadFile(filepath.Join(co, ".git", "index")); string(idx) != string(idx2) {
		t.Error("index file changed")
	}
}
