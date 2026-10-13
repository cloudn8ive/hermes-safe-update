package hermes

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// `hermes update` copies the rebuilt app over /Applications/Hermes.app and
// refuses while it runs, so a running installed copy is the desktop app.
func TestClassifyInstalledMacApp(t *testing.T) {
	const home, checkout = "/Users/u/.hermes", "/Users/u/.hermes/hermes-agent"
	markers := []string{"/contents/macos/hermes"}
	hints := []platform.PackagedHint{
		{Kind: "macos-app", Path: "/Applications/Hermes.app"},
		{Kind: "macos-app", Path: "/Users/u/Applications/Hermes.app"},
		{Kind: "docker", Path: "/.dockerenv"},
	}
	dirs := MacAppDirs(hints)
	if len(dirs) != 2 {
		t.Fatalf("MacAppDirs = %v (the docker hint must not count)", dirs)
	}
	cases := []struct {
		name string
		p    platform.Process
		want string
	}{
		{"/Applications main (ps: no Exe)", platform.Process{Name: "Hermes", Cmdline: "/Applications/Hermes.app/Contents/MacOS/Hermes"}, "desktop"},
		{"~/Applications main", platform.Process{Name: "Hermes", Cmdline: "/Users/u/Applications/Hermes.app/Contents/MacOS/Hermes"}, "desktop"},
		{"main with a known Exe", platform.Process{Name: "Hermes", Exe: "/Applications/Hermes.app/Contents/MacOS/Hermes", Cmdline: "Hermes"}, "desktop"},
		{"GPU helper", platform.Process{Name: "Hermes Helper (GPU)", Cmdline: "/Applications/Hermes.app/Contents/Frameworks/Hermes Helper (GPU).app/Contents/MacOS/Hermes Helper (GPU) --type=gpu-process"}, ""},
		{"renderer helper", platform.Process{Name: "Hermes Helper (Renderer)", Cmdline: "/Applications/Hermes.app/Contents/MacOS/Hermes --type=renderer"}, ""},
		{"helper without --type, under Frameworks", platform.Process{Name: "Hermes Helper", Exe: "/Applications/Hermes.app/Contents/Frameworks/Hermes Helper.app/Contents/MacOS/Hermes Helper", Cmdline: "x"}, ""},
		{"another app named Hermes elsewhere", platform.Process{Name: "Hermes", Cmdline: "/Volumes/usb/Hermes.app/Contents/MacOS/Hermes"}, ""},
		{"sibling folder prefix", platform.Process{Name: "Hermes", Cmdline: "/Applications/Hermes.app-old/Contents/MacOS/Hermes"}, ""},
		{"cmdline-derived path without the marker", platform.Process{Name: "Hermes", Cmdline: "/Applications/Hermes.app/Contents/Resources/other"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyIn(c.p, home, checkout, markers, dirs); got != c.want {
				t.Errorf("ClassifyIn = %q, want %q", got, c.want)
			}
		})
	}
	// without the bundle list nothing changes: the old answer for the same process
	main := platform.Process{Name: "Hermes", Cmdline: "/Applications/Hermes.app/Contents/MacOS/Hermes"}
	if got := Classify(main, home, checkout, markers); got != "" {
		t.Errorf("Classify (no bundle dirs) = %q, want \"\"", got)
	}
}
