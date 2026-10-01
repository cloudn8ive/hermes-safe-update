//go:build !windows && !darwin && !linux

package platform

// newStub is used only by the OS files that have no real backend of their own.
func newStub(goos string, tested bool) *Platform {
	s := Stub{}
	return &Platform{
		OS: goos, Tested: tested,
		Paths: s, App: s, Procs: s, Console: s, Progress: s, Autostart: s, Disk: s,
	}
}
