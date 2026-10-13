package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
)

// hooksExamplesDir is the repository's examples/hooks folder (tests run in
// the package dir).
const hooksExamplesDir = "../../examples/hooks"

const skillMergeDir = hooksExamplesDir + "/bundled-skill-merge"

// Every example hook folder ships safe-update.*.yaml snippets. They must load
// strictly (a typo fails the load), define a post-update hook, and name a
// file that is in that folder.
func TestExampleHookSnippetsLoadAndPointAtShippedFiles(t *testing.T) {
	snippets, _ := filepath.Glob(filepath.Join(hooksExamplesDir, "*", "safe-update.*.yaml"))
	if len(snippets) < 2 {
		t.Fatalf("found %d example snippets, want the Windows and the POSIX one: %v", len(snippets), snippets)
	}
	for _, s := range snippets {
		t.Run(filepath.Base(filepath.Dir(s))+"/"+filepath.Base(s), func(t *testing.T) {
			cfg, err := config.Load(config.LoadOptions{YAMLPath: s, YAMLRequired: true, Getenv: func(string) string { return "" }})
			if err != nil {
				t.Fatalf("snippet must load strictly: %v", err)
			}
			if len(cfg.Hooks) != 1 {
				t.Fatalf("hooks = %+v, want exactly one", cfg.Hooks)
			}
			h := cfg.Hooks[0]
			if h.When != config.HookPostUpdate || h.Name == "" || h.Timeout() <= 0 {
				t.Errorf("hook = %+v", h)
			}
			script := h.Run[len(h.Run)-1]
			base := script[strings.LastIndexAny(script, `/\`)+1:]
			if _, err := os.Stat(filepath.Join(filepath.Dir(s), base)); err != nil {
				t.Errorf("the hook runs %q but %s is not in the example folder: %v", script, base, err)
			}
			win := strings.Contains(s, ".windows.")
			if win && (h.Run[0] != "cmd.exe" || !strings.HasSuffix(base, ".cmd")) || !win && (h.Run[0] != "/bin/sh" || !strings.HasSuffix(base, ".sh")) {
				t.Errorf("run = %q: the Windows snippet runs the .cmd through cmd.exe, the POSIX one the .sh through /bin/sh", h.Run)
			}
		})
	}
}

// The example works from its own folder with no helper of the author's
// machine: the files the README promises are all there.
func TestSkillMergeExampleFilesExist(t *testing.T) {
	for _, f := range []string{"README.md", "bundled_skill_merge.py", "bundled-skill-merge.cmd", "bundled-skill-merge.sh",
		"safe-update.windows.yaml", "safe-update.posix.yaml"} {
		if st, err := os.Stat(filepath.Join(skillMergeDir, f)); err != nil || st.Size() == 0 {
			t.Errorf("%s missing or empty: %v", f, err)
		}
	}
}

// The POSIX wrapper is started as `/bin/sh file` by the documented snippet,
// but a user may run it directly: git must record the exec bit
// (git update-index --chmod=+x).
func TestSkillMergeShellWrapperIsCommittedExecutable(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	out, err := exec.Command(git, "-C", skillMergeDir, "ls-files", "-s", "--", "bundled-skill-merge.sh").Output()
	if err != nil || len(out) == 0 {
		t.Skipf("not a git work tree (%v)", err)
	}
	if mode := strings.Fields(string(out))[0]; mode != "100755" {
		t.Errorf("bundled-skill-merge.sh is committed as %s, want 100755", mode)
	}
}

// Run without Hermes the script says what is wrong in one line and exits 1
// (a warning on the summary card), after changing nothing: no home, and a
// checkout whose Hermes code cannot be imported. Needs a working python.
func TestSkillMergeScriptSkipsClearlyWithoutHermes(t *testing.T) {
	py := skillMergePython(t)
	empty := t.TempDir()
	run := func(env []string, args ...string) (string, int) {
		t.Helper()
		return runSkillMergeScript(t, py, empty, env, args...)
	}

	out, code := run(nil, "merge")
	if code != 1 || !strings.Contains(out, "bundled skills: skipped - no Hermes home") || strings.Contains(out, "Traceback") {
		t.Errorf("no home: exit %d, output %q", code, out)
	}
	out, code = run([]string{"HERMES_SAFE_UPDATE_HOME=" + empty, "HERMES_SAFE_UPDATE_CHECKOUT=" + empty}, "merge")
	last := out[strings.LastIndex(out, "\n")+1:]
	if code != 1 || !strings.HasPrefix(last, "bundled skills: skipped - cannot import Hermes' skill code from ") || strings.Contains(out, "Traceback") {
		t.Errorf("no Hermes code: exit %d, output %q", code, out)
	}
	// the same through --home / --agent-dir, for manual use
	out, code = run(nil, "--home", empty, "--agent-dir", empty, "list")
	if code != 1 || !strings.Contains(out, "skipped - cannot import Hermes' skill code") {
		t.Errorf("--home/--agent-dir: exit %d, output %q", code, out)
	}
	entries, _ := os.ReadDir(empty)
	if len(entries) != 0 {
		t.Errorf("the script wrote %d entries into the home it could not use", len(entries))
	}
}
