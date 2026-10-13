package main

import (
	"os/exec"
	"strings"
	"testing"
)

// README and CONTRIBUTING run scripts/build-all.sh directly, and the shims
// are started by double click: the committed mode must carry the exec bit
// (git update-index --chmod=+x). The tests that run the shims deliberately
// copy them without it; this checks what git records.
func TestShellScriptsAreCommittedExecutable(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	for _, f := range []string{"build-all.sh", "dev-version.sh", "hermes-safe-update.sh", "hermes-update-check.sh", "hermes-safe-update.command", "hermes-update-check.command"} {
		out, err := exec.Command(git, "-C", scriptsDir, "ls-files", "-s", "--", f).Output()
		if err != nil || len(out) == 0 {
			t.Skipf("not a git work tree (%v)", err)
		}
		if mode := strings.Fields(string(out))[0]; mode != "100755" {
			t.Errorf("%s is committed as %s, want 100755 (git update-index --chmod=+x scripts/%s)", f, mode, f)
		}
	}
}
