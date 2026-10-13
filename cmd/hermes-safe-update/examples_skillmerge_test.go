package main

import (
	"context"
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
)

// skillMergePython finds a Python 3 on PATH or skips the test.
func skillMergePython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if out, err := exec.Command(p, "--version").Output(); err == nil && strings.HasPrefix(string(out), "Python 3") {
			return p
		}
	}
	t.Skip("no python 3 on PATH")
	return ""
}

// runSkillMergeScript runs the example script in dir with the HERMES_* and
// PYTHON* variables of the parent removed (the developer's shell may point at
// a real Hermes home), plus env. It returns the combined output (trimmed,
// \n line ends) and the exit code.
func runSkillMergeScript(t *testing.T, py, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(skillMergeDir, "bundled_skill_merge.py"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, append([]string{"-B", script}, args...)...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HERMES_") && !strings.HasPrefix(kv, "PYTHON") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PYTHONUTF8=1")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), code
}

// A stand-in for the part of Hermes' tools/skills_sync.py the script imports
// (its private hash and discovery helpers). Synthetic: the hash is a plain
// sha256 over the files. Two environment switches let a test observe or
// break it: FAKE_RECORD_HOME names a file that receives the HERMES_HOME the
// script had set when the core was imported, FAKE_HASH_FAIL makes hashing a
// path that contains the given text fail like a full disk would.
const fakeSkillsSync = `import hashlib
import os
from pathlib import Path

if os.environ.get("FAKE_RECORD_HOME"):
    Path(os.environ["FAKE_RECORD_HOME"]).write_text(os.environ.get("HERMES_HOME", ""), encoding="utf-8")


def _is_runtime_cache(p, root):
    return "__pycache__" in p.relative_to(root).parts


def _dir_hash(d):
    d = Path(d)
    fail = os.environ.get("FAKE_HASH_FAIL")
    if fail and fail in str(d):
        raise OSError("simulated disk failure")
    h = hashlib.sha256()
    for p in sorted(d.rglob("*")):
        if p.is_file() and not _is_runtime_cache(p, d):
            h.update(p.relative_to(d).as_posix().encode() + b"\0" + p.read_bytes() + b"\0")
    return h.hexdigest()


def _matches_origin_hash(dest, origin, user_hash):
    return user_hash == origin


def _discover_bundled_skills(b):
    return [(p.parent.name, p.parent) for p in sorted(Path(b).rglob("SKILL.md"))]


def _get_bundled_dir():
    return Path(os.environ["FAKE_BUNDLED"])
`

// The same hash as the stand-in, for flat folders of files.
func fakeTreeHash(t *testing.T, dir string) string {
	t.Helper()
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			r, _ := filepath.Rel(dir, p)
			rels = append(rels, filepath.ToSlash(r))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rels)
	h := sha256.New()
	for _, r := range rels {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(r)))
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(r + "\x00"))
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

const (
	smBase     = "# alpha\nline 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\n"
	smMine     = "# alpha\nline 1\nline 2 (mine)\nline 3\nline 4\nline 5\nline 6\nline 7\n"
	smUpstream = "# alpha\nline 1\nline 2\nline 3\nline 4\nline 5\nline 6 (upstream)\nline 7\n"
	smClash    = "# alpha\nline 1\nline 2 (upstream)\nline 3\nline 4\nline 5\nline 6\nline 7\n"
)

type skillMergeWorld struct {
	home, checkout, bundled string
	manifest, state         string // file contents before the run
}

func smWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func smRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// newSkillMergeWorld builds a synthetic Hermes home with one default-profile
// skill, "alpha": the user's copy (mine), the bundled copy (upstream), the
// manifest entry for the old bundled copy (smBase) and a saved snapshot of it
// (so the script needs no git history, only `git merge-file`). A stand-in
// tools/skills_sync.py is the "checkout".
func newSkillMergeWorld(t *testing.T, mine, upstream string) skillMergeWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	w := skillMergeWorld{home: filepath.Join(root, "home"), checkout: filepath.Join(root, "checkout"),
		bundled: filepath.Join(root, "bundled")}
	smWrite(t, filepath.Join(w.checkout, "tools", "__init__.py"), "")
	smWrite(t, filepath.Join(w.checkout, "tools", "skills_sync.py"), fakeSkillsSync)
	smWrite(t, filepath.Join(w.bundled, "alpha", "SKILL.md"), upstream)
	smWrite(t, filepath.Join(w.home, "skills", "alpha", "SKILL.md"), mine)
	snap := filepath.Join(w.home, ".bundled-merge-base", "alpha")
	smWrite(t, filepath.Join(snap, "SKILL.md"), smBase)
	origin := fakeTreeHash(t, snap)
	w.manifest = "alpha:" + origin + "\n"
	smWrite(t, filepath.Join(w.home, "skills", ".bundled_manifest"), w.manifest)
	w.state = `{"alpha": {"base_hash": "` + origin + `"}}` + "\n"
	smWrite(t, filepath.Join(w.home, ".bundled-merge-base", "state.json"), w.state)
	return w
}

// run starts the script against the world: --home/--agent-dir/--bundled as a
// manual run gives them, and a parent HERMES_HOME that must not matter.
func (w skillMergeWorld) run(t *testing.T, py string, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	env := append([]string{"FAKE_BUNDLED=" + w.bundled}, extraEnv...)
	global := []string{"--home", w.home, "--agent-dir", w.checkout, "--bundled", w.bundled}
	return runSkillMergeScript(t, py, filepath.Dir(w.home), env, append(global, args...)...)
}

// The backup folders of a world, oldest first.
func (w skillMergeWorld) backups(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(w.home, "backups", "skill-merge-*"))
	sort.Strings(m)
	return m
}

func smLastLine(s string) string { return s[strings.LastIndex(s, "\n")+1:] }

// A merge replaces three bookkeeping files besides the skill folder: the
// manifest, the per-profile state file and the saved snapshot of the base.
// All three must be copied into the run's backup folder first, next to the
// copy of the user's skill (<backup>/<profile>/<skill>).
func TestSkillMergeBacksUpWhatItEdits(t *testing.T) {
	py := skillMergePython(t)
	w := newSkillMergeWorld(t, smMine, smUpstream)
	out, code := w.run(t, py, nil, "merge")
	if code != 0 || !strings.Contains(out, "alpha: merged") {
		t.Fatalf("merge: exit %d, output %q", code, out)
	}
	if got := smRead(t, filepath.Join(w.home, "skills", "alpha", "SKILL.md")); !strings.Contains(got, "line 2 (mine)") || !strings.Contains(got, "line 6 (upstream)") {
		t.Fatalf("merged skill lacks one of the edits:\n%s", got)
	}
	b := w.backups(t)
	if len(b) != 1 {
		t.Fatalf("backup folders = %v, want exactly one", b)
	}
	for _, c := range []struct{ rel, want string }{
		{"default/alpha/SKILL.md", smMine},
		{".bookkeeping/default/skills/.bundled_manifest", w.manifest},
		{".bookkeeping/default/.bundled-merge-base/state.json", w.state},
		{".bookkeeping/default/.bundled-merge-base/alpha/SKILL.md", smBase},
	} {
		p := filepath.Join(b[0], filepath.FromSlash(c.rel))
		if data, err := os.ReadFile(p); err != nil {
			t.Errorf("backup lacks %s: %v", c.rel, err)
		} else if string(data) != c.want {
			t.Errorf("%s = %q, want the content from before the run %q", c.rel, data, c.want)
		}
	}
	if now := smRead(t, filepath.Join(w.home, "skills", ".bundled_manifest")); now == w.manifest {
		t.Error("the manifest was not updated, so the backup test proves nothing")
	}
}

// keep and unkeep edit skill-merge.json, and keep also re-baselines the
// manifest: both are backed up, a dry run and `list` write nothing at all.
func TestSkillMergeKeepAndUnkeepBackUpTheirFiles(t *testing.T) {
	py := skillMergePython(t)
	w := newSkillMergeWorld(t, smMine, smUpstream)
	keepFile := filepath.Join(w.home, "skill-merge.json")
	const original = "{\n  \"keep_mine\": [\n    \"other\"\n  ]\n}\n"
	smWrite(t, keepFile, original)

	for _, args := range [][]string{{"list"}, {"merge", "--dry-run"}} {
		if out, code := w.run(t, py, nil, args...); code != 0 {
			t.Fatalf("%v: exit %d, output %q", args, code, out)
		}
	}
	if b := w.backups(t); len(b) != 0 {
		t.Fatalf("list / --dry-run created backups %v", b)
	}

	if out, code := w.run(t, py, nil, "keep", "alpha"); code != 0 || !strings.Contains(out, "alpha: kept") {
		t.Fatalf("keep: exit %d, output %q", code, out)
	}
	b := w.backups(t)
	if len(b) != 1 {
		t.Fatalf("after keep: backup folders = %v, want one", b)
	}
	if got := smRead(t, filepath.Join(b[0], ".bookkeeping", "skill-merge.json")); got != original {
		t.Errorf("keep: backed-up keep list = %q, want %q", got, original)
	}
	if got := smRead(t, filepath.Join(b[0], ".bookkeeping", "default", "skills", ".bundled_manifest")); got != w.manifest {
		t.Errorf("keep: backed-up manifest = %q, want %q", got, w.manifest)
	}
	if got := smRead(t, filepath.Join(b[0], ".bookkeeping", "default", ".bundled-merge-base", "state.json")); got != w.state {
		t.Errorf("keep: backed-up state = %q, want %q", got, w.state)
	}
	if got := smRead(t, filepath.Join(w.home, "skills", "alpha", "SKILL.md")); got != smMine {
		t.Errorf("keep touched the skill: %q", got)
	}
	afterKeep := smRead(t, keepFile)

	if out, code := w.run(t, py, nil, "unkeep", "alpha"); code != 0 {
		t.Fatalf("unkeep: exit %d, output %q", code, out)
	}
	b = w.backups(t)
	if len(b) != 2 {
		t.Fatalf("after unkeep: backup folders = %v, want two", b)
	}
	if got := smRead(t, filepath.Join(b[1], ".bookkeeping", "skill-merge.json")); got != afterKeep || !strings.Contains(got, "alpha") {
		t.Errorf("unkeep: backed-up keep list = %q, want the one from before it, %q", got, afterKeep)
	}
}

// The swap succeeded, then writing the new base failed: the skill IS merged.
// The old message said "ERROR ...; unchanged" and exited 0. It must say it
// was merged, point at the backup and be a warning (exit 1).
func TestSkillMergeBookkeepingFailureAfterTheSwapIsReportedAsMerged(t *testing.T) {
	py := skillMergePython(t)
	w := newSkillMergeWorld(t, smMine, smUpstream)
	out, code := w.run(t, py, []string{"FAKE_HASH_FAIL=snap-alpha"}, "merge")
	if code != 1 {
		t.Errorf("exit %d, want 1 (a warning on the summary card); output %q", code, out)
	}
	if !strings.Contains(out, "alpha: ERROR merged, bookkeeping failed (") || !strings.Contains(out, "simulated disk failure") {
		t.Errorf("output lacks 'merged, bookkeeping failed' with the cause: %q", out)
	}
	b := w.backups(t)
	if len(b) != 1 || !strings.Contains(out, "backup at ") || !strings.Contains(out, filepath.Join(filepath.Base(b[0]), "default", "alpha")) {
		t.Errorf("output does not name the backup %v: %q", b, out)
	}
	if strings.Contains(out, "unchanged") {
		t.Errorf("the skill was merged, yet the output says unchanged: %q", out)
	}
	if got := smRead(t, filepath.Join(w.home, "skills", "alpha", "SKILL.md")); !strings.Contains(got, "line 2 (mine)") || !strings.Contains(got, "line 6 (upstream)") {
		t.Errorf("the skill should be merged:\n%s", got)
	}
	if got := smRead(t, filepath.Join(b[0], "default", "alpha", "SKILL.md")); got != smMine {
		t.Errorf("backup of your copy = %q", got)
	}
	if !strings.HasPrefix(smLastLine(out), "bundled skills: ") || !strings.Contains(smLastLine(out), "1 failed") {
		t.Errorf("last line (the summary card row) = %q, want a summary that counts the failure", smLastLine(out))
	}
}

// Exit codes the README documents: 0 when the run completed (conflicts and
// unknown bases are decisions for the user, shown in the summary line), 1
// whenever something went wrong that the user should see as a warning.
func TestSkillMergeExitCodes(t *testing.T) {
	py := skillMergePython(t)

	t.Run("a conflict is reported, not a failure", func(t *testing.T) {
		w := newSkillMergeWorld(t, smMine, smClash)
		out, code := w.run(t, py, nil, "merge")
		if code != 0 || !strings.Contains(out, "alpha: CONFLICT") || !strings.Contains(smLastLine(out), "1 need a decision") {
			t.Errorf("exit %d, output %q", code, out)
		}
		if got := smRead(t, filepath.Join(w.home, "skills", "alpha", "SKILL.md")); got != smMine {
			t.Errorf("a conflict changed the skill: %q", got)
		}
	})
	t.Run("an unreadable keep list is a warning", func(t *testing.T) {
		w := newSkillMergeWorld(t, smMine, smUpstream)
		smWrite(t, filepath.Join(w.home, "skill-merge.json"), "{not json")
		out, code := w.run(t, py, nil, "merge")
		if code != 1 || !strings.Contains(out, "bundled skills: not merged - ") || !strings.Contains(out, "skill-merge.json") {
			t.Errorf("exit %d, output %q", code, out)
		}
		if got := smRead(t, filepath.Join(w.home, "skills", "alpha", "SKILL.md")); got != smMine {
			t.Errorf("the skill changed: %q", got)
		}
	})
	t.Run("keep of a name that is not bundled", func(t *testing.T) {
		w := newSkillMergeWorld(t, smMine, smUpstream)
		out, code := w.run(t, py, nil, "keep", "nope")
		if code != 1 || !strings.Contains(out, "'nope' is not a bundled skill") {
			t.Errorf("exit %d, output %q", code, out)
		}
	})
	t.Run("a clean merge", func(t *testing.T) {
		w := newSkillMergeWorld(t, smMine, smUpstream)
		out, code := w.run(t, py, nil, "merge")
		if code != 0 || !strings.Contains(smLastLine(out), "merged upstream changes into 1 edited skill") {
			t.Errorf("exit %d, output %q", code, out)
		}
	})
}

// With --home the script must act on that home only. The shell of someone
// who works on another Hermes home has HERMES_HOME set (hermes-safe-update
// strips it from hooks, a manual run does not); Hermes' own code reads it, so
// the script has to point it at the home it resolved, not keep the old one.
func TestSkillMergeSetsHermesHomeToTheResolvedHome(t *testing.T) {
	py := skillMergePython(t)
	w := newSkillMergeWorld(t, smMine, smUpstream)
	record := filepath.Join(t.TempDir(), "hermes_home_seen")
	other := t.TempDir()
	out, code := w.run(t, py, []string{"HERMES_HOME=" + other, "FAKE_RECORD_HOME=" + record}, "list")
	if code != 0 {
		t.Fatalf("list: exit %d, output %q", code, out)
	}
	seen := smRead(t, record)
	a, errA := os.Stat(seen)
	b, errB := os.Stat(w.home)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Errorf("Hermes' core saw HERMES_HOME=%q, want the home %q (the shell had %q)", seen, w.home, other)
	}
}
