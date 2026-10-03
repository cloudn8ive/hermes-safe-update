package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
)

// sandboxGit runs the real git in dir. Every remote here is a local path.
func sandboxGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command(bin, append([]string{"-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeSandboxFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot maps every regular file under root (slash paths) to a content
// hash. skip decides which relative paths to leave out.
func snapshot(t *testing.T, root string, skip func(rel string) bool) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		m[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// changed lists the paths added, removed or modified between two snapshots.
func changed(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if before[k] != v {
			out = append(out, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestCheckLeavesHeadIndexAndWorkingTreeAlone runs a whole --check against a
// sandbox: a fake upstream, a local mirror one commit behind it, and a
// checkout one commit behind the mirror, with a stale index stat cache (so a
// plain `git status` would rewrite .git/index) and one locally modified
// tracked file. It pins what --check may write (design.md "What --check
// writes") and what it must never touch: HEAD, the branch, the index, the
// working tree, the git config.
func TestCheckLeavesHeadIndexAndWorkingTreeAlone(t *testing.T) {
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream.git")
	sandboxGit(t, root, "init", "--bare", "--initial-branch=main", upstream)
	work := filepath.Join(root, "work")
	sandboxGit(t, root, "clone", upstream, work)
	sandboxGit(t, work, "checkout", "-B", "main")
	commit := func(name, content string) string {
		writeSandboxFile(t, filepath.Join(work, name), content)
		sandboxGit(t, work, "add", name)
		sandboxGit(t, work, "commit", "-m", "add "+name)
		return sandboxGit(t, work, "rev-parse", "HEAD")
	}
	commit("a.txt", "one\n")
	commit("pyproject.toml", "[project]\nname = \"x\"\n")
	sandboxGit(t, work, "push", "origin", "main")

	mirrorDir := filepath.Join(root, "mirror", "hermes-agent.git")
	sandboxGit(t, root, "clone", "--bare", "--single-branch", "--branch", "main", "--no-tags", upstream, mirrorDir)
	checkout := filepath.Join(root, "hermes-agent")
	sandboxGit(t, root, "clone", upstream, checkout)
	// Looks like a Hermes checkout whose only reachable copy is the mirror.
	sandboxGit(t, checkout, "remote", "set-url", "origin", "https://github.com/NousResearch/hermes-agent.git")

	// Upstream moves on; the mirror is refreshed by --check, not before.
	commit("b.txt", "two\n")
	tip := commit("c.txt", "three\n")
	sandboxGit(t, work, "tag", "v9.9")
	sandboxGit(t, work, "push", "origin", "main", "--tags")

	// A local edit, and a stale stat cache: git status would refresh the index.
	writeSandboxFile(t, filepath.Join(checkout, "a.txt"), "edited locally\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(checkout, "pyproject.toml"), old, old); err != nil {
		t.Fatal(err)
	}

	home := filepath.Join(root, "home")
	machine := filepath.Join(home, "safe-update.json")
	writeSandboxFile(t, machine, `{"mirror": {"path": `+quoteJSON(mirrorDir)+`, "set_up": "2026-10-01T12:00:00+00:00"}, "keep": 1}`)
	gitBin, _ := exec.LookPath("git")

	h := newHarness(t)
	h.mode = ModeCheck
	h.timings = filepath.Join(home, "logs", "safe-update-timings.json")
	h.install.Home = home
	h.install.Checkout = checkout
	h.install.Git = gitBin
	h.realRepo = &hermes.GitRepo{Git: gitBin, Checkout: checkout, Run: execx.New()}
	h.mirror = mirror.New(mirror.Deps{
		Runner: execx.New(), Git: gitBin, Home: home, MachinePath: machine, Checkout: checkout,
	})

	notGit := func(rel string) bool { return rel == ".git" || strings.HasPrefix(rel, ".git/") }
	headBefore := sandboxGit(t, checkout, "rev-parse", "HEAD")
	branchBefore := sandboxGit(t, checkout, "symbolic-ref", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(checkout, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	treeBefore := snapshot(t, checkout, notGit)
	gitBefore := snapshot(t, filepath.Join(checkout, ".git"), nil)
	mirrorBefore := snapshot(t, mirrorDir, nil)
	homeBefore := snapshot(t, home, nil)

	if err := h.run(); err != nil {
		t.Fatalf("check: %v\n%s", err, h.output())
	}
	h.mustContain("2 new commit(s)", "Hermes was not touched")

	// Never touched: HEAD, the branch, the index (byte for byte), the
	// working tree (including the local edit), the git config.
	if got := sandboxGit(t, checkout, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, got)
	}
	if got := sandboxGit(t, checkout, "symbolic-ref", "HEAD"); got != branchBefore {
		t.Errorf("HEAD now points at %s, was %s", got, branchBefore)
	}
	if got := sandboxGit(t, checkout, "rev-parse", "refs/heads/main"); got != headBefore {
		t.Errorf("local main moved to %s", got)
	}
	indexAfter, err := os.ReadFile(filepath.Join(checkout, ".git", "index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Errorf("the index was rewritten (err=%v)", err)
	}
	if d := changed(treeBefore, snapshot(t, checkout, notGit)); len(d) != 0 {
		t.Errorf("working tree changed: %v", d)
	}

	// What --check does write into .git: fetch-like data only.
	allowed := []string{"objects/", "refs/remotes/origin/", "refs/tags/", "logs/refs/remotes/origin/", "FETCH_HEAD", "packed-refs"}
	for _, p := range changed(gitBefore, snapshot(t, filepath.Join(checkout, ".git"), nil)) {
		ok := false
		for _, a := range allowed {
			ok = ok || strings.HasPrefix(p, a)
		}
		if !ok {
			t.Errorf(".git/%s changed: --check may only fetch objects, origin/main and tags", p)
		}
	}
	if got := sandboxGit(t, checkout, "rev-parse", "origin/main"); got != tip {
		t.Errorf("origin/main = %s, want the fetched tip %s", got, tip)
	}
	if got := sandboxGit(t, checkout, "tag", "-l", "v9.9"); got != "v9.9" {
		t.Errorf("tags = %q: the v* tags are fetched too", got)
	}
	if got := sandboxGit(t, checkout, "config", "--get", "remote.origin.url"); !strings.Contains(got, "github.com/NousResearch") {
		t.Errorf("origin URL changed to %q", got)
	}
	if st := sandboxGit(t, checkout, "status", "--porcelain", "--untracked-files=no"); st != "M a.txt" {
		t.Errorf("status = %q, want only the local edit", st)
	}

	// The mirror is refreshed (fetch into its bare repo) and its state file
	// under HOME/cache records it; the timings history is saved.
	if got := sandboxGit(t, mirrorDir, "rev-parse", "refs/heads/main"); got != tip {
		t.Errorf("mirror not refreshed: %s", got)
	}
	if len(changed(mirrorBefore, snapshot(t, mirrorDir, nil))) == 0 {
		t.Error("mirror unchanged: expected a refresh")
	}
	homeChanged := changed(homeBefore, snapshot(t, home, nil))
	if len(homeChanged) == 0 || !contains(homeChanged, "logs/safe-update-timings.json") {
		t.Errorf("home files changed = %v, want the timings file among them", homeChanged)
	}
	for _, p := range homeChanged {
		if p != "logs/safe-update-timings.json" && p != "logs/update-mirror.log" && !strings.HasPrefix(p, "cache/") {
			t.Errorf("home/%s written by --check", p)
		}
	}
}

func quoteJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + s + `"`
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
