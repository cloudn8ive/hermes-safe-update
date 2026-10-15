package update

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
)

// TestCheckStableLeavesTheCheckoutAlone runs a whole --check on a release
// channel against a real sandbox repository: a release tag sits on a commit
// the checkout lacks, the mirror lacks the tag too, and the release changes
// the Node packages. The check may do what it does for main (refresh the
// mirror, bring objects and the release tag into the checkout's .git so the
// prediction is exact), but it must not touch HEAD, the index, the working
// tree or the git config, must not seed origin/main (main's business) and
// must predict against the RELEASE, not main.
func TestCheckStableLeavesTheCheckoutAlone(t *testing.T) {
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream.git")
	sandboxGit(t, root, "init", "--bare", "--initial-branch=main", upstream)
	work := filepath.Join(root, "work")
	sandboxGit(t, root, "clone", upstream, work)
	sandboxGit(t, work, "checkout", "-B", "main")
	writeSandboxFile(t, filepath.Join(work, "a.txt"), "one\n")
	sandboxGit(t, work, "add", "a.txt")
	sandboxGit(t, work, "commit", "-m", "one")
	sandboxGit(t, work, "push", "origin", "main")

	mirrorDir := filepath.Join(root, "mirror", "hermes-agent.git")
	sandboxGit(t, root, "clone", "--bare", "--single-branch", "--branch", "main", "--no-tags", upstream, mirrorDir)
	checkout := filepath.Join(root, "hermes-agent")
	sandboxGit(t, root, "clone", upstream, checkout)
	sandboxGit(t, checkout, "remote", "set-url", "origin", "https://github.com/NousResearch/hermes-agent.git")

	// upstream moves on: the release changes the Node packages; main goes on
	// to change Python dependencies, which the release does not have
	writeSandboxFile(t, filepath.Join(work, "package.json"), "{}\n")
	sandboxGit(t, work, "add", "package.json")
	sandboxGit(t, work, "commit", "-m", "release")
	release := sandboxGit(t, work, "rev-parse", "HEAD")
	sandboxGit(t, work, "tag", "v9.9.9")
	writeSandboxFile(t, filepath.Join(work, "pyproject.toml"), "[project]\nname = \"x\"\n")
	sandboxGit(t, work, "add", "pyproject.toml")
	sandboxGit(t, work, "commit", "-m", "main goes on")
	sandboxGit(t, work, "push", "origin", "main", "--tags")
	writeSandboxFile(t, filepath.Join(checkout, "a.txt"), "edited locally\n")

	home := filepath.Join(root, "home")
	machine := filepath.Join(home, "safe-update.json")
	writeSandboxFile(t, machine, `{"mirror": {"path": `+quoteJSON(mirrorDir)+`, "set_up": "2026-10-01T12:00:00+00:00"}, "keep": 1}`)
	gitBin, _ := exec.LookPath("git")

	h := newHarness(t)
	h.mode = ModeCheck
	h.cfg.Update.Channel = config.ChannelStable
	h.timings = filepath.Join(home, "logs", "safe-update-timings.json")
	h.install.Home = home
	h.install.Checkout = checkout
	h.install.Git = gitBin
	h.realRepo = &hermes.GitRepo{Git: gitBin, Checkout: checkout, Run: execx.New()}
	h.mirror = mirror.New(mirror.Deps{Runner: execx.New(), Git: gitBin, Home: home, MachinePath: machine, Checkout: checkout})
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: "→ Update channel: stable\n→ Selected release available: stable v9.9.9 (" + release[:12] + ")\n  Run `hermes update` to install it.\n"},
		"update", "--check", "--channel", "stable")

	headBefore := sandboxGit(t, checkout, "rev-parse", "HEAD")
	branchBefore := sandboxGit(t, checkout, "symbolic-ref", "HEAD")
	originMainBefore := sandboxGit(t, checkout, "rev-parse", "refs/remotes/origin/main")
	indexBefore, err := os.ReadFile(filepath.Join(checkout, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	notGit := func(rel string) bool { return rel == ".git" || strings.HasPrefix(rel, ".git/") }
	treeBefore := snapshot(t, checkout, notGit)
	gitBefore := snapshot(t, filepath.Join(checkout, ".git"), nil)
	homeBefore := snapshot(t, home, nil)

	if err := h.run(); err != nil {
		t.Fatalf("check: %v\n%s", err, h.output())
	}
	h.mustContain("release v9.9.9 available, 1 commit", "Hermes was not touched")
	// predicted against the release: its package.json change shows, main's
	// pyproject.toml change does not
	if got := cardRowTight(h.output(), "Node packages"); !strings.HasPrefix(got, "changed") {
		t.Errorf("Node packages = %q, want the release's change\n%s", got, h.output())
	}
	if got := cardRow(h.output(), "Python deps"); got != "unchanged" {
		t.Errorf("Python deps = %q: the prediction looked at main, not at the release\n%s", got, h.output())
	}
	if got := cardRow(h.output(), "Available"); got != "release v9.9.9 (1 commit)" {
		t.Errorf("Available = %q", got)
	}

	if got := sandboxGit(t, checkout, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, got)
	}
	if got := sandboxGit(t, checkout, "symbolic-ref", "HEAD"); got != branchBefore {
		t.Errorf("HEAD now points at %s, was %s", got, branchBefore)
	}
	if after, err := os.ReadFile(filepath.Join(checkout, ".git", "index")); err != nil || !bytes.Equal(indexBefore, after) {
		t.Errorf("the index was rewritten (err=%v)", err)
	}
	if d := changed(treeBefore, snapshot(t, checkout, notGit)); len(d) != 0 {
		t.Errorf("working tree changed: %v", d)
	}
	// main's seed did not run; only objects and the release tag came
	if got := sandboxGit(t, checkout, "rev-parse", "refs/remotes/origin/main"); got != originMainBefore {
		t.Errorf("origin/main moved to %s: the main seed ran on a release check", got)
	}
	for _, p := range changed(gitBefore, snapshot(t, filepath.Join(checkout, ".git"), nil)) {
		if !strings.HasPrefix(p, "objects/") && !strings.HasPrefix(p, "refs/tags/") && p != "FETCH_HEAD" && p != "packed-refs" {
			t.Errorf(".git/%s changed: a release check may only bring the release's objects and tag", p)
		}
	}
	if got := sandboxGit(t, checkout, "rev-parse", "refs/tags/v9.9.9^{commit}"); got != release {
		t.Errorf("the release tag in the checkout = %s, want %s", got, release)
	}
	if got := sandboxGit(t, checkout, "config", "--get", "remote.origin.url"); !strings.Contains(got, "github.com/NousResearch") {
		t.Errorf("origin URL changed to %q", got)
	}
	if st := sandboxGit(t, checkout, "status", "--porcelain", "--untracked-files=no"); st != "M a.txt" {
		t.Errorf("status = %q, want only the local edit", st)
	}
	// the mirror got the release (refreshed), the home only the usual files
	if got := sandboxGit(t, mirrorDir, "rev-parse", "refs/tags/v9.9.9^{commit}"); got != release {
		t.Errorf("mirror's v9.9.9 = %s", got)
	}
	for _, p := range changed(homeBefore, snapshot(t, home, nil)) {
		if p != "logs/safe-update-timings.json" && p != "logs/update-mirror.log" && !strings.HasPrefix(p, "cache/") {
			t.Errorf("home/%s written by --check", p)
		}
	}
}
