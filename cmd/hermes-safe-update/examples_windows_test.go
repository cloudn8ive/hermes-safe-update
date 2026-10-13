package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The .cmd wrapper hands over to the interpreter named by
// HERMES_SAFE_UPDATE_PYTHON (a stand-in .cmd here), runs `merge` when it has
// no argument, passes its arguments otherwise, and says so when no Python is
// given.
func TestSkillMergeCmdWrapper(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "bundled-skill-merge.cmd")
	b, err := os.ReadFile(filepath.Join(skillMergeDir, "bundled-skill-merge.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, b, 0o644); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(dir, "fake python.cmd") // a space in the path on purpose
	if err := os.WriteFile(py, []byte("@echo off\r\necho fake python args: %*\r\necho PYTHONUTF8=%PYTHONUTF8%\r\nexit /b 3\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(pyEnv string, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("cmd.exe", append([]string{"/c", wrapper}, args...)...)
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(kv), "HERMES_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
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
		return strings.ReplaceAll(string(out), "\r\n", "\n"), code
	}

	out, code := run(py)
	if code != 3 || !strings.Contains(out, `fake python args: -W ignore "`+filepath.Join(dir, "bundled_skill_merge.py")+`" merge`+"\n") || !strings.Contains(out, "PYTHONUTF8=1\n") {
		t.Errorf("no arguments (the hook): exit %d, output %q", code, out)
	}
	out, code = run(py, "list", "--profile", "work")
	if code != 3 || !strings.Contains(out, `bundled_skill_merge.py" list --profile work`+"\n") {
		t.Errorf("list: exit %d, output %q", code, out)
	}
	out, code = run("")
	if code != 1 || !strings.HasPrefix(out, "bundled skills: skipped - HERMES_SAFE_UPDATE_PYTHON is not set") {
		t.Errorf("no python: exit %d, output %q", code, out)
	}
	out, code = run(filepath.Join(dir, "gone", "python.exe"))
	if code != 1 || !strings.HasPrefix(out, "bundled skills: skipped - ") {
		t.Errorf("python path does not exist: exit %d, output %q", code, out)
	}
}
