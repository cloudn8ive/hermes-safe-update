//go:build linux

package platform

import (
	"context"
	"os"
	"path/filepath"

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
		func() []mountPoint {
			b, err := os.ReadFile("/proc/mounts")
			if err != nil {
				return []mountPoint{{Root: "/", Fixed: true}}
			}
			var out []mountPoint
			for _, m := range parseMountDevs(string(b)) {
				dev := m.Dev
				if real, err := filepath.EvalSymlinks(dev); err == nil {
					dev = real // /dev/disk/by-uuid/... and /dev/mapper/... are symlinks
				}
				out = append(out, mountPoint{Root: m.Root, Fixed: m.Root == "/" || !blockRemovable("/sys", dev)})
			}
			return out
		})
}
