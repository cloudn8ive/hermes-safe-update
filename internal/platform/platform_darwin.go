//go:build darwin

package platform

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)

// New returns the macOS backends: `ps` process table, AppleScript quit then
// SIGTERM/SIGKILL, `open` launch. Untested on a real macOS machine.
func New() *Platform {
	return newUnixPlatform("darwin", listPS, nil, func() []mountPoint {
		roots := []mountPoint{{Root: "/", Fixed: true}}
		if ents, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range ents {
				p := filepath.Join("/Volumes", e.Name())
				if real, err := filepath.EvalSymlinks(p); err == nil && real == "/" {
					continue // the boot volume's symlink
				}
				var st unix.Statfs_t
				fixed := unix.Statfs(p, &st) == nil && darwinFixed(st.Flags)
				roots = append(roots, mountPoint{Root: p, Fixed: fixed})
			}
		}
		return roots
	})
}

// listPS reads the process table with two `ps` calls (pure Go, no cgo).
func listPS(ctx context.Context) ([]Process, error) {
	comm, err := runCommand(ctx, "/bin/ps", "-axwwo", "pid=,ppid=,comm=")
	if err != nil {
		return nil, err
	}
	cmd, err := runCommand(ctx, "/bin/ps", "-axwwo", "pid=,command=")
	if err != nil {
		return nil, err
	}
	return parsePSTable(comm, cmd), nil
}
