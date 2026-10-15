package platform

// Pre-flight probes (v0.4.0). Every one of them only reads: a byte-range
// try-lock that is given back at once, a token query, open-only access
// checks. None creates, renames, deletes or changes anything, so a stop
// based on them happens before the run touched Hermes.

// LockState is what a lock probe found.
type LockState int

const (
	// LockFree: the file does not exist, or nothing holds the byte range.
	LockFree LockState = iota
	// LockHeld: some process holds (part of) the byte range.
	LockHeld
	// LockUnopenable: the file exists but cannot be opened for reading.
	// Hermes' own probe answers "held" for it (nothing can prove it free).
	LockUnopenable
)

// LockInfo is the answer of LockProbe.ProbeLock.
type LockInfo struct {
	State LockState
	// Record is the start of the file (the holder record Hermes writes
	// before the locked range), nil when it could not be read.
	Record []byte
	// Detail is the OS error text for LockUnopenable.
	Detail string
}

// LockProbe asks whether another process holds a byte range of a file.
type LockProbe interface {
	// ProbeLock tries to take [offset, offset+length) of the file at path
	// exclusively without waiting and gives it back at once. It never
	// creates the file. An error means the question could not be asked on
	// this OS or for this file; the caller treats that as "unknown", never
	// as held.
	ProbeLock(path string, offset, length int64) (LockInfo, error)
}

// Elevation tells whether this process runs with an elevated (full
// administrator) token.
type Elevation interface {
	// Elevated is true only when a filtered, non-elevated sibling of this
	// token exists and this is the full one (Windows UAC). A standard user,
	// or an administrator with UAC off, is not "elevated": there is no
	// normal-user session to protect. An error means unknown.
	Elevated() (bool, error)
}

// AccessMask selects what AccessProbe.Probe asks the operating system for.
type AccessMask int

const (
	// AccessRead: list/read the entry and read its attributes.
	AccessRead AccessMask = iota + 1
	// AccessModify: delete it and write its attributes (for a folder also
	// add files and sub-folders): what a rename or replace needs.
	AccessModify
)

// AccessResult is the outcome of one open-only probe.
type AccessResult struct {
	Missing bool  // the path does not exist
	Reparse bool  // a symlink or junction: not followed, not judged
	Dir     bool  // the entry is a folder
	Denied  bool  // the operating system refused the requested access
	Busy    bool  // sharing violation: in use, says nothing about rights
	Err     error // anything else (the probe could not decide)
}

// Access probes access rights by really opening an entry with the current
// token. The open is the test: reading ACLs and guessing from "owner" or
// "missing user ACE" is not (a healthy entry can look just like a damaged one).
type Access interface {
	// Probe opens path for the access mask and closes it again. It never
	// creates, renames, deletes or writes anything.
	Probe(path string, mask AccessMask) AccessResult
	// Children lists the base names directly inside dir.
	Children(dir string) ([]string, error)
}

// ---- stubs (macOS, Linux: nothing to probe) ----

func (Stub) ProbeLock(string, int64, int64) (LockInfo, error) {
	return LockInfo{}, ErrNotSupported
}
func (Stub) Elevated() (bool, error)               { return false, ErrNotSupported }
func (Stub) Probe(string, AccessMask) AccessResult { return AccessResult{Err: ErrNotSupported} }
func (Stub) Children(string) ([]string, error)     { return nil, ErrNotSupported }

var (
	_ LockProbe = Stub{}
	_ Elevation = Stub{}
	_ Access    = Stub{}
)
