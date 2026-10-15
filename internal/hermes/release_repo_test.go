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

// sandbox is a real git history in a temp dir:
//
//	c1 (base)  <- HEAD of the checkout
//	c2         package.json changed, tagged v0.9.0 (the release)
//	c3         pyproject.toml changed (main's tip, origin/main)
type sandbox struct {
	t          *testing.T
	bin, dir   string
	c1, c2, c3 string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	s := &sandbox{t: t, bin: bin, dir: t.TempDir()}
	s.git("init", "-q", "-b", "main")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", "one\n")
	write("pyproject.toml", "one\n")
	write("README.md", "one\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "base")
	s.c1 = s.git("rev-parse", "HEAD")
	write("package.json", "two\n")
	s.git("commit", "-q", "-am", "node packages change")
	s.c2 = s.git("rev-parse", "HEAD")
	s.git("tag", "v0.9.0")
	// decoys that must never be taken for a release
	s.git("tag", "v2026.9.24")
	s.git("tag", "v0.9.0+canary.20261001T000000Z")
	write("pyproject.toml", "two\n")
	s.git("commit", "-q", "-am", "python deps change")
	s.c3 = s.git("rev-parse", "HEAD")
	s.git("update-ref", "refs/remotes/origin/main", s.c3)
	s.git("checkout", "-q", "--detach", s.c1)
	return s
}

func (s *sandbox) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command(s.bin, args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (s *sandbox) repo() *GitRepo {
	return &GitRepo{Git: s.bin, Checkout: s.dir, Run: execx.New()}
}

func tri(p *bool) string {
	switch {
	case p == nil:
		return "unknown"
	case *p:
		return "changed"
	}
	return "unchanged"
}

// The prediction is made against the target the update installs. The same
// checkout answers differently for main's tip and for the release: only the
// release carries the node-packages change.
func TestDiffKindsAgainstTag(t *testing.T) {
	s := newSandbox(t)
	r := s.repo()
	ctx := context.Background()
	for _, c := range []struct {
		name, ref string
		npm, py   string
	}{
		{"main tip", MainRef, "changed", "changed"},
		{"release tag", "refs/tags/v0.9.0", "changed", "unchanged"},
		{"release commit by full sha", s.c2, "changed", "unchanged"},
		{"HEAD against itself", s.c1, "unchanged", "unchanged"},
	} {
		npm, py := r.DiffKinds(ctx, c.ref)
		if tri(npm) != c.npm || tri(py) != c.py {
			t.Errorf("%s (%s): npm=%s py=%s, want npm=%s py=%s", c.name, c.ref, tri(npm), tri(py), c.npm, c.py)
		}
	}
}

// A ref that is not one of the three shapes is unknown, and git is not even
// started for it: an option, a sha prefix, a CalVer or canary tag, a revision
// expression.
func TestDiffKindsRefusesOddRefs(t *testing.T) {
	f := &execx.Fake{}
	f.OnPrefix([]string{`G:\git.exe`}, execx.Result{Code: 1})
	r := newRepo(f)
	for _, ref := range []string{
		"", "--output=x", "-p", "HEAD", "origin/main^", "origin/main~3", "main", "refs/heads/main",
		"refs/tags/v2026.9.24", "refs/tags/v0.22.0+canary.20261001T000000Z", "refs/tags/rc.1-v0.22.1",
		"refs/tags/v0.22.1^{commit}", "v0.22.1", "abcdef012345", strings.ToUpper(sha40), sha40 + "0",
	} {
		if n, p := r.DiffKinds(context.Background(), ref); n != nil || p != nil {
			t.Errorf("DiffKinds(%q) = %s/%s, want unknown", ref, tri(n), tri(p))
		}
	}
	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("git ran for a refused ref: %v", calls[0].Argv)
	}
}

// The argv for a release target is `diff --quiet HEAD <ref> -- <paths>`:
// the ref sits where origin/main sat, then `--`, so no path can be read as a
// revision (exit codes only, rule 18).
func TestDiffKindsArgvForATarget(t *testing.T) {
	for _, ref := range []string{MainRef, "refs/tags/v0.22.1", sha40} {
		f := &execx.Fake{}
		f.On(gitArgv(append([]string{"diff", "--quiet", "HEAD", ref, "--"}, PyFilesPathspecs...)...), execx.Result{Code: 1})
		f.On(gitArgv("diff", "--quiet", "HEAD", ref, "--", ":(glob)**/package.json", ":(glob)**/package-lock.json"), execx.Result{Code: 0})
		n, p := newRepo(f).DiffKinds(context.Background(), ref)
		if tri(n) != "unchanged" || tri(p) != "changed" {
			t.Errorf("%s: npm=%s py=%s", ref, tri(n), tri(p))
		}
	}
}

func TestResolveReleaseFromTheLocalTag(t *testing.T) {
	s := newSandbox(t)
	r := s.repo()
	ctx := context.Background()
	for _, c := range []struct {
		name, tag, sha12, wantRef, wantSHA string
	}{
		{"tag here, no commit named", "v0.9.0", "", "refs/tags/v0.9.0", s.c2},
		{"tag here and it is the commit named", "v0.9.0", s.c2[:12], "refs/tags/v0.9.0", s.c2},
		// the tag points elsewhere than Hermes says: the commit Hermes named wins
		{"tag disagrees, the named commit is here", "v0.9.0", s.c3[:12], s.c3, s.c3},
		{"no tag, commit named and here", "", s.c3[:12], s.c3, s.c3},
		{"tag missing, commit named and here", "v0.9.1", s.c2[:12], s.c2, s.c2},
		// nothing local: unknown, never a guess
		{"tag missing, commit missing", "v0.9.1", "0123456789ab", "", ""},
		{"tag disagrees, commit missing", "v0.9.0", "0123456789ab", "", ""},
		{"nothing named", "", "", "", ""},
		// only strict stable tags are ever looked up
		{"calver tag is not a release", "v2026.9.24", "", "", ""},
		{"canary tag is not a release", "v0.9.0+canary.20261001T000000Z", "", "", ""},
		{"option-shaped tag", "--all", "", "", ""},
		{"non-hex commit", "", "--all", "", ""},
		{"too short a prefix", "", s.c2[:4], "", ""},
	} {
		ref, sha := r.ResolveRelease(ctx, c.tag, c.sha12)
		if ref != c.wantRef || sha != c.wantSHA {
			t.Errorf("%s: ResolveRelease(%q, %q) = %q, %q; want %q, %q", c.name, c.tag, c.sha12, ref, sha, c.wantRef, c.wantSHA)
		}
	}
}

// An abbreviation that two commits share is ambiguous: unknown, not the
// first match.
func TestResolveReleaseAmbiguousPrefixIsUnknown(t *testing.T) {
	// real git says this for a prefix two objects share
	f := &execx.Fake{}
	f.On(gitArgv("rev-parse", "--verify", "--quiet", "abcdef0^{commit}"),
		execx.Result{Code: 128, Output: "error: short object ID abcdef0 is ambiguous"})
	if ref, sha := newRepo(f).ResolveRelease(context.Background(), "", "abcdef0"); ref != "" || sha != "" {
		t.Errorf("ambiguous prefix resolved to %q %q", ref, sha)
	}
}

// The lookup is offline: git is told not to fetch a missing object from the
// partial clone's remote.
func TestResolveReleaseNeverLazyFetches(t *testing.T) {
	f := &execx.Fake{}
	f.OnPrefix([]string{`G:\git.exe`}, execx.Result{Code: 1})
	newRepo(f).ResolveRelease(context.Background(), "v0.22.1", "abcdef012345")
	calls := f.Calls()
	if len(calls) == 0 {
		t.Fatal("git never ran")
	}
	for _, c := range calls {
		if c.Env["GIT_NO_LAZY_FETCH"] != "1" {
			t.Errorf("%v runs with env %v", c.Argv, c.Env)
		}
		if strings.Contains(strings.Join(c.Argv, " "), "fetch") {
			t.Errorf("a fetch ran: %v", c.Argv)
		}
	}
}

func TestResolveReleaseWithoutGit(t *testing.T) {
	r := &GitRepo{Checkout: `C:\ck`, Run: &execx.Fake{}}
	if ref, sha := r.ResolveRelease(context.Background(), "v0.22.1", "abcdef012345"); ref != "" || sha != "" {
		t.Errorf("got %q %q without git", ref, sha)
	}
}

func TestReleaseTagIsStrict(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"0.22.1", "v0.22.1"},
		{"v0.22.1", "v0.22.1"},
		{"1.0.0", "v1.0.0"},
		{"999.0.12", "v999.0.12"},
		{"", ""},
		{"2026.9.24", ""}, // CalVer: four-digit major
		{"0.22.1+canary.20261001T000000Z", ""},
		{"0.22.1-rc.1", ""},
		{"0.22", ""},
		{"0.22.1.2", ""},
		{"01.2.3", ""},
		{"0.22.x", ""},
	} {
		if got := ReleaseTag(c.in); got != c.want {
			t.Errorf("ReleaseTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
