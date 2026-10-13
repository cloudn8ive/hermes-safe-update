//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBinary is a stand-in for hermes-safe-update: it echoes its arguments
// and whether HERMES_HOME reached it, then exits 7.
const fakeBinary = `#!/bin/sh
echo "fake args: $*"
echo "HERMES_HOME=[${HERMES_HOME-unset}]"
exit 7
`

// shimDir copies the POSIX launcher shims into a fresh folder. Only mode
// 0644 on purpose: the shims call each other through sh, so a download
// without the exec bit still works when started as `sh <file>`.
func shimDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"hermes-safe-update.sh", "hermes-update-check.sh", "hermes-safe-update.command", "hermes-update-check.command"} {
		b, err := os.ReadFile(filepath.Join(scriptsDir, n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeFake(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hermes-safe-update"), []byte(fakeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
}

// testShells are the shells the shims are run with: the system sh (dash on
// Debian/Ubuntu) and, when present, bash in POSIX mode (what macOS's
// /bin/sh is).
func testShells() [][]string {
	out := [][]string{{"/bin/sh"}}
	if b, err := exec.LookPath("bash"); err == nil {
		out = append(out, []string{b, "--posix"})
	}
	return out
}

// runShim runs `<shell> <shim> args...` with a clean environment (no tty, so
// the shim must not wait for Enter) and returns its output and exit code.
func runShim(t *testing.T, shell []string, home, path, shim string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	argv := append(append(append([]string{}, shell[1:]...), shim), args...)
	cmd := exec.CommandContext(ctx, shell[0], argv...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + path, "HERMES_HOME=/must/not/reach/the/tool"}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s did not finish (waiting for a key or looping?): %s", shim, out)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestShellShims(t *testing.T) {
	const sysPath = "/usr/bin:/bin"
	cases := []struct {
		name     string
		shim     string
		args     []string
		fakeAt   string // "next", "local" (~/.local/bin), "path", "" (none)
		wantArgs string
		wantCode int
	}{
		{"binary next to the shim", "hermes-safe-update.sh", []string{"--plain", "a b"}, "next", "fake args: --plain a b", 7},
		{"binary in ~/.local/bin", "hermes-safe-update.sh", nil, "local", "fake args: ", 7},
		{"binary on PATH", "hermes-safe-update.sh", []string{"--yes"}, "path", "fake args: --yes", 7},
		{"check shim adds --check", "hermes-update-check.sh", []string{"--plain"}, "next", "fake args: --check --plain", 7},
		{"macOS .command runs the update shim", "hermes-safe-update.command", nil, "next", "fake args: ", 7},
		{"macOS check .command", "hermes-update-check.command", nil, "next", "fake args: --check", 7},
		{"no binary anywhere", "hermes-safe-update.sh", nil, "", "", 2},
	}
	for _, shell := range testShells() {
		for _, c := range cases {
			t.Run(filepath.Base(shell[0])+"/"+c.name, func(t *testing.T) {
				dir, home := shimDir(t), t.TempDir()
				path := sysPath
				switch c.fakeAt {
				case "next":
					writeFake(t, dir)
				case "local":
					writeFake(t, filepath.Join(home, ".local", "bin"))
				case "path":
					onPath := t.TempDir()
					writeFake(t, onPath)
					path = onPath + ":" + sysPath
				}
				out, code := runShim(t, shell, home, path, filepath.Join(dir, c.shim), c.args...)
				if code != c.wantCode {
					t.Fatalf("exit %d, want %d; output:\n%s", code, c.wantCode, out)
				}
				if c.wantCode == 2 {
					if !strings.Contains(out, "hermes-safe-update not found") {
						t.Errorf("no not-found message:\n%s", out)
					}
					return
				}
				for _, want := range []string{
					c.wantArgs + "\n",
					"HERMES_HOME=[unset]",
					"Finished with exit code 7. Log: " + home + "/.hermes/logs/safe-update.log",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("output lacks %q:\n%s", want, out)
					}
				}
				if strings.Contains(out, "Press Enter") {
					t.Errorf("asked for Enter without a terminal:\n%s", out)
				}
			})
		}
	}
}

// A shim renamed to plain `hermes-safe-update` and put on PATH must not run
// itself in a loop: with no real binary it stops with exit 2.
func TestShellShimRenamedOnPathDoesNotRunItself(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	b, err := os.ReadFile(filepath.Join(scriptsDir, "hermes-safe-update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(dir, "hermes-safe-update")
	if err := os.WriteFile(self, b, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shell := range testShells() {
		out, code := runShim(t, shell, home, dir+":/usr/bin:/bin", self)
		if code != 2 || !strings.Contains(out, "not found") {
			t.Fatalf("%s: exit %d, want 2 with a not-found message; output:\n%s", shell[0], code, out)
		}
	}
}
