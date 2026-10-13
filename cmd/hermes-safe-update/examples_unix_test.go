//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakePython stands in for the Hermes Python: it prints what it was started
// with, so the wrapper's argument handling can be seen.
const fakePython = `#!/bin/sh
echo "fake python args: $*"
echo "PYTHONUTF8=${PYTHONUTF8-unset}"
exit 3
`

func TestSkillMergeShellWrapper(t *testing.T) {
	dir := t.TempDir()
	// only mode 0644 for the wrapper: the documented snippet starts it as `/bin/sh file`
	wrapper := filepath.Join(dir, "bundled-skill-merge.sh")
	b, err := os.ReadFile(filepath.Join(skillMergeDir, "bundled-skill-merge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, b, 0o644); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(dir, "fake python", "python3") // a space in the path on purpose
	if err := os.MkdirAll(filepath.Dir(py), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(py, []byte(fakePython), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, shell := range testShells() {
		run := func(pyEnv string, args ...string) (string, int) {
			t.Helper()
			cmd := exec.Command(shell[0], append(append(append([]string{}, shell[1:]...), wrapper), args...)...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			if pyEnv != "" {
				cmd.Env = append(cmd.Env, "HERMES_SAFE_UPDATE_PYTHON="+pyEnv)
			}
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			return string(out), code
		}
		name := strings.Join(shell, " ")

		out, code := run(py)
		if code != 3 || !strings.Contains(out, "fake python args: -W ignore ") || !strings.Contains(out, "bundled_skill_merge.py merge\n") ||
			!strings.Contains(out, "PYTHONUTF8=1") {
			t.Errorf("%s, no arguments (the hook): exit %d, output %q (want the script with `merge`, exit code passed through)", name, code, out)
		}
		out, code = run(py, "keep", "some skill")
		if code != 3 || !strings.Contains(out, "bundled_skill_merge.py keep some skill\n") {
			t.Errorf("%s, keep: exit %d, output %q", name, code, out)
		}
		out, code = run("")
		if code != 1 || !strings.HasPrefix(out, "bundled skills: skipped - HERMES_SAFE_UPDATE_PYTHON is not set") {
			t.Errorf("%s, no python: exit %d, output %q", name, code, out)
		}
		out, code = run(filepath.Join(dir, "gone", "python3"))
		if code != 1 || !strings.HasPrefix(out, "bundled skills: skipped - ") {
			t.Errorf("%s, python path does not exist: exit %d, output %q", name, code, out)
		}
	}
}
