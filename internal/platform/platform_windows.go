//go:build windows

package platform

import (
	"os"
	"runtime"
)

// New returns the Windows backends. Windows is the tested platform.
// Progress uses OSC 9;4 inside Windows Terminal (WT_SESSION set) and
// ITaskbarList3 on a classic console window; it is a no-op without one.
func New() *Platform {
	con := newWinConsole()
	var prog Progress
	switch {
	case os.Getenv("WT_SESSION") != "" && con.IsTerminal():
		prog = &wtProgress{w: os.Stdout}
	case con.IsTerminal():
		prog = newTaskbarProgress(consoleHWND())
	default:
		prog = Stub{}
	}
	procs := winProcs{}
	return &Platform{
		OS:        "windows",
		Tested:    true,
		Paths:     winPaths{goarch: runtime.GOARCH, knownFolder: realKnownFolder},
		App:       newWinApp(procs),
		Procs:     procs,
		Console:   con,
		Progress:  prog,
		Autostart: winAutostart{},
		Disk:      winDisk{getenv: os.Getenv},
	}
}
