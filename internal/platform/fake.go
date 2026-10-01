package platform

import (
	"context"
	"fmt"
	"sync"
)

// Test fakes, importable by every package's tests (this file is not a
// _test.go file on purpose). They never touch the real OS.

// NewFake returns a Platform made of fakes with empty state.
func NewFake() *Platform {
	procs := NewFakeProcs()
	return &Platform{
		OS: "fake", Tested: true,
		Paths:     &FakePaths{},
		App:       &FakeApp{Procs: procs, CloseBehaviour: CloseExits},
		Procs:     procs,
		Console:   &FakeConsole{Cols: 100, Rows: 30},
		Progress:  &FakeProgress{},
		Autostart: &FakeAutostart{},
		Disk:      &FakeDisk{FreeBytes: 100e9},
	}
}

// FakePaths returns its fields verbatim.
type FakePaths struct {
	Home, UserDataDir string
	Launchers         []string
	DesktopApps       []string
	Markers           []string
	PythonGlobs       []string
	GitGlobs          []string
	Electron          string
	Packaged          []PackagedHint
}

func (f *FakePaths) HermesHome(Env) (string, error) { return f.Home, nil }
func (f *FakePaths) UserData(Env) (string, error)   { return f.UserDataDir, nil }
func (f *FakePaths) LauncherCandidates(Env, string, string) []string {
	return f.Launchers
}
func (f *FakePaths) DesktopAppCandidates(string) []string    { return f.DesktopApps }
func (f *FakePaths) DesktopProcessMarkers() []string         { return f.Markers }
func (f *FakePaths) ManagedPythonGlobs(string) []string      { return f.PythonGlobs }
func (f *FakePaths) BundledGitGlobs(string) []string         { return f.GitGlobs }
func (f *FakePaths) ElectronLeaf() string                    { return f.Electron }
func (f *FakePaths) PackagedInstallHints(Env) []PackagedHint { return f.Packaged }

// FakeProcs is an in-memory process table.
type FakeProcs struct {
	mu      sync.Mutex
	procs   map[int]Process
	order   []int
	killed  []int
	ListErr error
}

// NewFakeProcs creates a table holding procs.
func NewFakeProcs(procs ...Process) *FakeProcs {
	f := &FakeProcs{procs: map[int]Process{}}
	for _, p := range procs {
		f.Add(p)
	}
	return f
}

// Add inserts or replaces a process.
func (f *FakeProcs) Add(p Process) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.procs[p.PID]; !ok {
		f.order = append(f.order, p.PID)
	}
	f.procs[p.PID] = p
}

// Remove deletes pid and its descendants (as if it exited).
func (f *FakeProcs) Remove(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeTree(pid)
}

func (f *FakeProcs) removeTree(pid int) {
	for _, p := range f.procs {
		if p.PPID == pid && p.PID != pid {
			f.removeTree(p.PID)
		}
	}
	delete(f.procs, pid)
}

func (f *FakeProcs) List(context.Context) ([]Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var out []Process
	for _, pid := range f.order {
		if p, ok := f.procs[pid]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *FakeProcs) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.procs[pid]
	return ok
}

func (f *FakeProcs) KillTree(_ context.Context, p Process) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []Process
	for _, pid := range f.order {
		if q, ok := f.procs[pid]; ok {
			all = append(all, q)
		}
	}
	if err := CheckIdentity(all, p); err != nil {
		return fmt.Errorf("kill tree: refusing to stop pid %d: %w", p.PID, err)
	}
	f.killed = append(f.killed, p.PID)
	f.removeTree(p.PID)
	return nil
}

// Killed returns the pids passed to KillTree, in order.
func (f *FakeProcs) Killed() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.killed...)
}

// CloseBehaviour scripts how FakeApp reacts to RequestClose.
type CloseBehaviour int

const (
	CloseExits   CloseBehaviour = iota // the app exits at once
	CloseIgnored                       // the app stays (e.g. a quit dialog)
)

// FakeApp records calls and edits a FakeProcs table.
type FakeApp struct {
	Procs          *FakeProcs
	CloseBehaviour CloseBehaviour
	Blocker        string
	LaunchErr      error
	NoLaunch       string // non-empty: CanLaunch returns false with this reason

	mu    sync.Mutex
	calls []string
}

func (a *FakeApp) record(s string) {
	a.mu.Lock()
	a.calls = append(a.calls, s)
	a.mu.Unlock()
}

// Calls returns "close <pid>", "force <pid>", "launch <exe>" in order.
func (a *FakeApp) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *FakeApp) IsRunning(pid int) bool { return a.Procs.Alive(pid) }

func (a *FakeApp) RequestClose(_ context.Context, pid int) error {
	a.record(fmt.Sprintf("close %d", pid))
	if a.CloseBehaviour == CloseExits {
		a.Procs.Remove(pid)
	}
	return nil
}

func (a *FakeApp) ForceClose(ctx context.Context, p Process) error {
	a.record(fmt.Sprintf("force %d", p.PID))
	return a.Procs.KillTree(ctx, p)
}

func (a *FakeApp) CloseBlocker(int) string { return a.Blocker }

func (a *FakeApp) Launch(_ context.Context, exe string, _ []string) error {
	a.record("launch " + exe)
	return a.LaunchErr
}

func (a *FakeApp) CanLaunch(string) (bool, string) {
	if a.NoLaunch != "" {
		return false, a.NoLaunch
	}
	return true, ""
}

// FakeConsole delivers scripted keys; after the script it waits for ctx.
type FakeConsole struct {
	Terminal   bool
	Cols, Rows int
	Background bool // IsForeground() returns !Background
	Keys       []Key

	mu      sync.Mutex
	Titles  []string
	Flashes int
}

func (c *FakeConsole) IsTerminal() bool                    { return c.Terminal }
func (c *FakeConsole) EnableVT() (func(), error)           { return func() {}, nil }
func (c *FakeConsole) Size() (int, int, bool)              { return c.Cols, c.Rows, c.Cols > 0 }
func (c *FakeConsole) SetIdentity(string, []string) string { return "fake" }
func (c *FakeConsole) IsForeground() bool                  { return !c.Background }

func (c *FakeConsole) SetTitle(t string) error {
	c.mu.Lock()
	c.Titles = append(c.Titles, t)
	c.mu.Unlock()
	return nil
}

func (c *FakeConsole) Flash() {
	c.mu.Lock()
	c.Flashes++
	c.mu.Unlock()
}

func (c *FakeConsole) ReadKey(ctx context.Context) (Key, error) {
	c.mu.Lock()
	if len(c.Keys) > 0 {
		k := c.Keys[0]
		c.Keys = c.Keys[1:]
		c.mu.Unlock()
		return k, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return Key{}, ctx.Err()
}

// FakeProgress records every Set.
type FakeProgress struct {
	mu        sync.Mutex
	States    []ProgressState
	Fractions []float64
	Closed    bool
}

func (p *FakeProgress) Set(s ProgressState, f float64) {
	p.mu.Lock()
	p.States = append(p.States, s)
	p.Fractions = append(p.Fractions, f)
	p.mu.Unlock()
}

func (p *FakeProgress) Close() {
	p.mu.Lock()
	p.Closed = true
	p.mu.Unlock()
}

// FakeAutostart maps task names to executables.
type FakeAutostart struct {
	Tasks       map[string]string // name -> exe
	ElevateCode int
	ElevateOut  string
	ElevateErr  error
	OnElevate   func(argv []string) // e.g. update Tasks to simulate success
	Elevated    [][]string
}

func (a *FakeAutostart) Supported() bool { return a.Tasks != nil }

func (a *FakeAutostart) TaskExecutable(_ context.Context, name string) (string, bool, error) {
	exe, ok := a.Tasks[name]
	return exe, ok, nil
}

func (a *FakeAutostart) RunElevated(_ context.Context, argv []string) (int, string, error) {
	a.Elevated = append(a.Elevated, argv)
	if a.OnElevate != nil {
		a.OnElevate(argv)
	}
	return a.ElevateCode, a.ElevateOut, a.ElevateErr
}

// FakeDisk reports fixed numbers.
type FakeDisk struct {
	FreeBytes uint64
	Vols      []Volume
	Err       error
}

func (d *FakeDisk) Free(string) (uint64, error) { return d.FreeBytes, d.Err }
func (d *FakeDisk) Volumes() ([]Volume, error)  { return d.Vols, d.Err }

var (
	_ Paths      = (*FakePaths)(nil)
	_ Procs      = (*FakeProcs)(nil)
	_ AppControl = (*FakeApp)(nil)
	_ Console    = (*FakeConsole)(nil)
	_ Progress   = (*FakeProgress)(nil)
	_ Autostart  = (*FakeAutostart)(nil)
	_ Disk       = (*FakeDisk)(nil)
)
