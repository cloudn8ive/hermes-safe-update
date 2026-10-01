//go:build linux

package platform

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)

// New returns the Linux backends: /proc process table, SIGTERM/SIGKILL app
// control, setsid launch. Untested on a real Linux machine.
func New() *Platform {
	return newUnixPlatform("linux",
		func(ctx context.Context) ([]Process, error) { return readProcTable(ctx, "/proc") },
		func(pid int) bool { return procIsZombie("/proc", pid) },
		func() []string {
			b, err := os.ReadFile("/proc/mounts")
			if err != nil {
				return []string{"/"}
			}
			return parseMounts(string(b))
		})
}
