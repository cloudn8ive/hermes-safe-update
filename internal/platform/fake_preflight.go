package platform

import (
	"sort"
	"strings"
	"sync"
)

// Fakes of the pre-flight probes (see preflight.go). They never touch the
// real OS.

// FakeLockProbe answers every ProbeLock with Info (the zero value is
// "free"), or Err, and records the path it was asked about.
type FakeLockProbe struct {
	Info LockInfo
	Err  error

	mu     sync.Mutex
	asked  []string
	ranges [][2]int64
}

func (f *FakeLockProbe) ProbeLock(path string, offset, length int64) (LockInfo, error) {
	f.mu.Lock()
	f.asked = append(f.asked, path)
	f.ranges = append(f.ranges, [2]int64{offset, length})
	f.mu.Unlock()
	return f.Info, f.Err
}

// Ranges returns the (offset, length) of every probe, in order.
func (f *FakeLockProbe) Ranges() [][2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int64(nil), f.ranges...)
}

// Asked returns the paths probed, in order.
func (f *FakeLockProbe) Asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// FakeElevation reports a fixed token.
type FakeElevation struct {
	IsElevated bool
	Err        error
}

func (f FakeElevation) Elevated() (bool, error) { return f.IsElevated, f.Err }

// FakeAccess scripts open-only probes. A path not listed in Results is
// healthy (exists, opens). Results are per mask; a path listed under
// AccessRead only is healthy for AccessModify.
type FakeAccess struct {
	// Results[mask][path] is the outcome for that probe.
	Results map[AccessMask]map[string]AccessResult
	// Dirs maps a folder to its children's base names.
	Dirs map[string][]string
	// Absent lists paths that do not exist (Missing for both masks).
	Absent map[string]bool

	mu     sync.Mutex
	probes []string // "read <path>" / "modify <path>", in order
	once   map[AccessMask]map[string]AccessResult
}

// DenyOnce makes the FIRST probe of path for mask refuse; later probes see
// the path's normal result (a passing refusal, such as a file that is
// delete-pending for a moment).
func (f *FakeAccess) DenyOnce(mask AccessMask, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.once == nil {
		f.once = map[AccessMask]map[string]AccessResult{}
	}
	if f.once[mask] == nil {
		f.once[mask] = map[string]AccessResult{}
	}
	f.once[mask][path] = AccessResult{Denied: true}
}

// Deny makes path refuse the mask.
func (f *FakeAccess) Deny(mask AccessMask, path string) {
	f.set(mask, path, AccessResult{Denied: true})
}

// Set scripts one result.
func (f *FakeAccess) Set(mask AccessMask, path string, r AccessResult) { f.set(mask, path, r) }

func (f *FakeAccess) set(mask AccessMask, path string, r AccessResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Results == nil {
		f.Results = map[AccessMask]map[string]AccessResult{}
	}
	if f.Results[mask] == nil {
		f.Results[mask] = map[string]AccessResult{}
	}
	f.Results[mask][path] = r
}

func (f *FakeAccess) Probe(path string, mask AccessMask) AccessResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := "read "
	if mask == AccessModify {
		name = "modify "
	}
	f.probes = append(f.probes, name+path)
	if f.Absent[path] {
		return AccessResult{Missing: true}
	}
	if r, ok := f.once[mask][path]; ok {
		delete(f.once[mask], path)
		return r
	}
	if r, ok := f.Results[mask][path]; ok {
		return r
	}
	_, isDir := f.Dirs[path]
	return AccessResult{Dir: isDir}
}

func (f *FakeAccess) Children(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Dirs[dir]...), nil
}

// Probes returns every probe made, in order ("read <path>", "modify <path>").
func (f *FakeAccess) Probes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.probes...)
}

// ProbedPaths returns the distinct paths probed, sorted.
func (f *FakeAccess) ProbedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for _, p := range f.probes {
		if i := strings.IndexByte(p, ' '); i >= 0 {
			seen[p[i+1:]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var (
	_ LockProbe = (*FakeLockProbe)(nil)
	_ Elevation = FakeElevation{}
	_ Access    = (*FakeAccess)(nil)
)
