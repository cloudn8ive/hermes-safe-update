//go:build darwin || linux

package platform

// Real OS seams for macOS and Linux. Everything here talks to a live OS and
// was NOT run on a real macOS or Linux machine; the decision logic around it
// lives in appctl_unix.go, procparse_unix.go and keys_unix.go (unit-tested).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// newUnixPlatform assembles the backends for goos ("darwin" or "linux").
func newUnixPlatform(goos string, list func(context.Context) ([]Process, error), zombie func(int) bool, vols func() []string) *Platform {
	sig := realSignaller{}
	procs := &unixProcs{list: list, sig: sig, self: os.Getpid(), isZombie: zombie}
	app := &unixApp{
		goos: goos, sig: sig, procs: procs, run: runCommand,
		getenv: os.Getenv, exists: fileExists,
		launcher:            launchDetached,
		chromeSandboxSetuid: isRootSetuid,
		userNamespacesWork:  userNamespacesWork,
	}
	return &Platform{
		OS: goos, Tested: false,
		Paths:     newUnixPaths(goos, runtime.GOARCH),
		App:       app,
		Procs:     procs,
		Console:   &unixConsole{},
		Progress:  newOSCProgress(os.Getenv("HERMES_SAFE_UPDATE_OSC_PROGRESS") == "1"),
		Autostart: unixAutostart{},
		Disk:      unixDisk{mounts: vols},
	}
}

type realSignaller struct{}

func (realSignaller) Term(pid int) error { return unix.Kill(pid, unix.SIGTERM) }
func (realSignaller) Kill(pid int) error { return unix.Kill(pid, unix.SIGKILL) }
func (realSignaller) Exists(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// runCommand runs a short helper with a hard timeout and returns its combined
// output. The args are never joined into a shell string.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// launchDetached starts exe in its own session (setsid), stdio detached, and
// confirms the child is still alive 1.5 s later (upstream posix.sh).
func launchDetached(ctx context.Context, exe string, args []string) error {
	cmd := exec.Command(exe, args...) // not CommandContext: the app must outlive us
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return fmt.Errorf("%s exited right after start: %v", exe, err)
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// isRootSetuid reports whether p is a root-owned file with the setuid bit.
func isRootSetuid(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.Mode()&os.ModeSetuid == 0 {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && sys.Uid == 0
}

// userNamespacesWork runs `unshare --user --map-root-user true`.
func userNamespacesWork() bool {
	p, err := exec.LookPath("unshare")
	if err != nil {
		return false
	}
	_, err = runCommand(context.Background(), p, "--user", "--map-root-user", "true")
	return err == nil
}

// unixConsole is the terminal. VT sequences are native, so EnableVT is a
// no-op.
type unixConsole struct{}

var _ Console = (*unixConsole)(nil)

// IsTerminal reports whether stdout is a terminal. Untested on a real macOS
// or Linux machine.
func (*unixConsole) IsTerminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// EnableVT does nothing: macOS and Linux terminals parse VT natively.
// Untested on a real macOS or Linux machine.
func (*unixConsole) EnableVT() (func(), error) { return func() {}, nil }

// Size returns the terminal size of stdout, falling back to stderr.
// Untested on a real macOS or Linux machine.
func (*unixConsole) Size() (int, int, bool) {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w, h, true
		}
	}
	return 0, 0, false
}

// SetTitle sets the terminal title with OSC 0 (only on a terminal).
// Untested on a real macOS or Linux machine.
func (c *unixConsole) SetTitle(title string) error {
	if !c.IsTerminal() {
		return nil
	}
	title = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, title)
	_, err := fmt.Fprintf(os.Stdout, "\x1b]0;%s\a", title)
	return err
}

// SetIdentity does nothing outside Windows. Untested on a real macOS or
// Linux machine.
func (*unixConsole) SetIdentity(string, []string) string { return "skipped: Windows only" }

// IsForeground is always true: there is no portable way to ask a terminal.
// Untested on a real macOS or Linux machine.
func (*unixConsole) IsForeground() bool { return true }

// Flash rings the terminal bell. Untested on a real macOS or Linux machine.
func (c *unixConsole) Flash() {
	if c.IsTerminal() {
		_, _ = os.Stdout.WriteString("\a")
	}
}

// ReadKey waits for one key press with the terminal in cbreak mode (no echo,
// no line buffering, no ISIG so Ctrl+C arrives as a key; output processing
// stays on so the UI can keep drawing). The original mode is restored on
// every return path. Without a terminal on stdin it only waits for ctx.
// Untested on a real macOS or Linux machine.
func (*unixConsole) ReadKey(ctx context.Context) (Key, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		<-ctx.Done()
		return Key{}, ctx.Err()
	}
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return Key{}, fmt.Errorf("read terminal mode: %w", err)
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return Key{}, fmt.Errorf("set terminal mode: %w", err)
	}
	defer func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }()

	buf := make([]byte, 32)
	for {
		if err := ctx.Err(); err != nil {
			return Key{}, err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return Key{}, fmt.Errorf("wait for a key: %w", err)
		}
		if n == 0 {
			continue
		}
		m, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return Key{}, fmt.Errorf("read a key: %w", err)
		}
		if m == 0 {
			<-ctx.Done() // stdin closed
			return Key{}, ctx.Err()
		}
		return parseKey(buf[:m]), nil
	}
}

// oscProgressSink shows progress through OSC 9;4 on stderr, only when the
// user opted in (HERMES_SAFE_UPDATE_OSC_PROGRESS=1); otherwise it is a no-op.
type oscProgressSink struct {
	enabled bool
	w       *os.File
}

func newOSCProgress(enabled bool) Progress {
	return &oscProgressSink{enabled: enabled && term.IsTerminal(int(os.Stderr.Fd())), w: os.Stderr}
}

// Set draws the progress state if enabled. Untested on a real macOS or Linux
// machine.
func (p *oscProgressSink) Set(state ProgressState, fraction float64) {
	if p.enabled {
		_, _ = p.w.WriteString(oscProgress(state, fraction))
	}
}

// Close clears the progress indicator if one was drawn. Untested on a real
// macOS or Linux machine.
func (p *oscProgressSink) Close() { p.Set(ProgressNone, 0) }

// unixAutostart: logon tasks are Windows-only; callers print an info row.
type unixAutostart struct{}

// Supported is false: there is no logon-task equivalent we manage. Untested
// on a real macOS or Linux machine.
func (unixAutostart) Supported() bool { return false }

// TaskExecutable always returns ErrNotSupported. Untested on a real macOS or
// Linux machine.
func (unixAutostart) TaskExecutable(context.Context, string) (string, bool, error) {
	return "", false, ErrNotSupported
}

// RunElevated always returns ErrNotSupported. Untested on a real macOS or
// Linux machine.
func (unixAutostart) RunElevated(context.Context, []string) (int, string, error) {
	return -1, "", ErrNotSupported
}

// unixDisk reports free space with statfs.
type unixDisk struct {
	mounts func() []string
}

// Free returns the bytes available to an unprivileged user on the volume
// holding path (the nearest existing parent if path does not exist yet).
// Untested on a real macOS or Linux machine.
func (unixDisk) Free(p string) (uint64, error) {
	for {
		var st unix.Statfs_t
		err := unix.Statfs(p, &st)
		if err == nil {
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		}
		parent := filepath.Dir(p)
		if !errors.Is(err, unix.ENOENT) || parent == p {
			return 0, fmt.Errorf("statfs %s: %w", p, err)
		}
		p = parent
	}
}

// Volumes lists local mounted volumes with free space. Untested on a real
// macOS or Linux machine.
func (d unixDisk) Volumes() ([]Volume, error) {
	roots := []string{"/"}
	if d.mounts != nil {
		roots = d.mounts()
	}
	var out []Volume
	for _, r := range roots {
		free, err := d.Free(r)
		if err != nil {
			continue
		}
		out = append(out, Volume{Root: r, Free: free, Fixed: true, System: r == "/"})
	}
	if len(out) == 0 {
		return nil, errors.New("no readable volumes")
	}
	return out, nil
}
