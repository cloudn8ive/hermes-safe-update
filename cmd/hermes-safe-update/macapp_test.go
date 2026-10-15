package main

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// A Hermes.app running from /Applications is what `hermes update` refuses to
// overwrite; the flow must see it as the desktop app (and so close it).
func TestCheckSeesAnInstalledMacAppAsTheDesktop(t *testing.T) {
	s := newSynthHome(t)
	s.run.On([]string{s.launcher, "update", "--check", "--branch", "main"}, execx.Result{Output: "2 commits behind origin/main"})
	fp := s.plat.Paths.(*platform.FakePaths)
	fp.Markers = []string{"/contents/macos/hermes"}
	fp.Packaged = []platform.PackagedHint{{Kind: "macos-app", Path: "/Applications/Hermes.app"}}
	procs := s.plat.Procs.(*platform.FakeProcs)
	procs.Add(platform.Process{PID: 91, Name: "Hermes", Cmdline: "/Applications/Hermes.app/Contents/MacOS/Hermes"})
	procs.Add(platform.Process{PID: 92, Name: "Hermes Helper (GPU)", Cmdline: "/Applications/Hermes.app/Contents/Frameworks/Hermes Helper (GPU).app/Contents/MacOS/Hermes Helper (GPU) --type=gpu-process"})
	if code := s.cli("--plain", "check"); code != 0 {
		t.Fatalf("exit %d\n%s", code, s.errb.String())
	}
	if out := s.errb.String(); !strings.Contains(out, "running: desktop") || strings.Contains(out, "desktop 2") || strings.Contains(out, "desktop (2") {
		t.Errorf("want exactly one desktop process (the main one, not its helper):\n%s", out)
	}
}
