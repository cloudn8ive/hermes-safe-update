package hermes

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// The rollback test of a release: with real git, is the release commit an
// ancestor of HEAD? (Hermes before v0.22.0 words a release behind the checkout
// as "available", so Safe Update has to ask git itself.)
func TestIsAncestorWithRealGit(t *testing.T) {
	s := newSandbox(t)
	r := s.repo()
	ctx := context.Background()
	// a commit that shares no history with c1..c3
	orphan := s.git("commit-tree", s.c1+"^{tree}", "-m", "unrelated")

	cases := []struct {
		name             string
		ancestor, descen string
		wantYes, wantOK  bool
	}{
		{"older commit, newer commit", s.c1, s.c3, true, true},
		{"the release tag's commit is behind main's tip", s.c2, s.c3, true, true},
		{"newer commit is not an ancestor of the older one", s.c3, s.c1, false, true},
		{"a commit is its own ancestor", s.c2, s.c2, true, true},
		{"HEAD as the descendant (HEAD is c1)", s.c1, "HEAD", true, true},
		{"a commit ahead of HEAD is not its ancestor", s.c2, "HEAD", false, true},
		{"unrelated histories, one way", orphan, s.c3, false, true},
		{"unrelated histories, the other way", s.c3, orphan, false, true},
		{"an object that is not in the repository", "0123456789abcdef0123456789abcdef01234567", s.c3, false, false},
		{"not a full sha", s.c2[:12], s.c3, false, false},
		{"an option instead of a sha", "--all", s.c3, false, false},
		{"an option as the descendant", s.c2, "--all", false, false},
		{"a branch name", "main", s.c3, false, false},
		{"upper-case sha", strings.ToUpper(s.c2), s.c3, false, false},
	}
	for _, c := range cases {
		yes, ok := r.IsAncestor(ctx, c.ancestor, c.descen)
		if yes != c.wantYes || ok != c.wantOK {
			t.Errorf("%s: IsAncestor = (%v, %v), want (%v, %v)", c.name, yes, ok, c.wantYes, c.wantOK)
		}
	}
}

func TestIsAncestorWithoutGitIsUnknown(t *testing.T) {
	r := &GitRepo{Checkout: `C:\ck`, Run: &execx.Fake{}}
	if yes, ok := r.IsAncestor(context.Background(), sha40, sha40); yes || ok {
		t.Errorf("got (%v, %v) without git", yes, ok)
	}
	if sh, ok := r.IsShallow(context.Background()); sh || ok {
		t.Errorf("IsShallow = (%v, %v) without git", sh, ok)
	}
}

// Exit codes decide: 0 yes, 1 no, anything else (a crash, a timeout) is "git
// cannot say", never "no". The call is offline and read-only.
func TestIsAncestorArgvAndExitCodes(t *testing.T) {
	other := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		res     execx.Result
		yes, ok bool
	}{
		{execx.Result{Code: 0}, true, true},
		{execx.Result{Code: 1}, false, true},
		{execx.Result{Code: 128, Output: "fatal: Not a valid commit name"}, false, false},
		{execx.Result{Code: execx.CodeTimeout, TimedOut: true}, false, false},
	} {
		f := (&execx.Fake{}).On(gitArgv("merge-base", "--is-ancestor", sha40, other), c.res)
		yes, ok := newRepo(f).IsAncestor(context.Background(), sha40, other)
		if yes != c.yes || ok != c.ok {
			t.Errorf("rc=%d timeout=%v: (%v, %v), want (%v, %v)", c.res.Code, c.res.TimedOut, yes, ok, c.yes, c.ok)
		}
		calls := f.Calls()
		if len(calls) != 1 || calls[0].Env["GIT_NO_LAZY_FETCH"] != "1" {
			t.Errorf("calls %v", calls)
		}
	}
}

func TestIsShallowOutputs(t *testing.T) {
	for _, c := range []struct {
		res        execx.Result
		shallow, k bool
	}{
		{execx.Result{Output: "true\n"}, true, true},
		{execx.Result{Output: "false\r\n"}, false, true},
		{execx.Result{Output: "--is-shallow-repository\n"}, false, false}, // git older than 2.15 echoes the flag
		{execx.Result{Code: 128, Output: "fatal: not a git repository"}, false, false},
		{execx.Result{Output: ""}, false, false},
	} {
		f := (&execx.Fake{}).On(gitArgv("rev-parse", "--is-shallow-repository"), c.res)
		sh, ok := newRepo(f).IsShallow(context.Background())
		if sh != c.shallow || ok != c.k {
			t.Errorf("%q rc=%d: (%v, %v), want (%v, %v)", c.res.Output, c.res.Code, sh, ok, c.shallow, c.k)
		}
	}
}

// A shallow clone lacks history (so a missing commit proves nothing); a full
// clone and a partial (blob:none) clone have every commit.
func TestIsShallowWithRealGit(t *testing.T) {
	s := newSandbox(t)
	ctx := context.Background()
	if sh, ok := s.repo().IsShallow(ctx); sh || !ok {
		t.Errorf("a full repository: (%v, %v)", sh, ok)
	}
	s.git("config", "uploadpack.allowFilter", "true")
	url := s.dir
	clone := func(name string, args ...string) *GitRepo {
		dir := filepath.Join(t.TempDir(), name)
		cmd := exec.Command(s.bin, append(append([]string{"clone", "-q", "--no-local"}, args...), url, dir)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git clone %v unavailable here: %v\n%s", args, err, out)
		}
		return &GitRepo{Git: s.bin, Checkout: dir, Run: execx.New()}
	}
	if sh, ok := clone("shallow", "--depth", "1", "--branch", "main").IsShallow(ctx); !sh || !ok {
		t.Errorf("a depth-1 clone: (%v, %v)", sh, ok)
	}
	if sh, ok := clone("partial", "--filter=blob:none", "--no-checkout").IsShallow(ctx); sh || !ok {
		t.Errorf("a partial clone has every commit: (%v, %v)", sh, ok)
	}
}
