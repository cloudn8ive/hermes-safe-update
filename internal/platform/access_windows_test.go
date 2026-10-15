//go:build windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/sys/windows"
)

func TestElevationAnswersAndMatchesTheToken(t *testing.T) {
	got, err := winElevation{}.Elevated()
	if err != nil {
		t.Fatalf("Elevated: %v", err)
	}
	// "Elevated" means the full half of a split token, which is always an
	// elevated token. (The reverse does not hold: UAC off is elevated but
	// not "Full", and must not be reported.)
	if got && !windows.GetCurrentProcessToken().IsElevated() {
		t.Error("reported elevated, but the token is not elevated")
	}
}

func TestAccessMissingAndHealthy(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := winAccess{}
	if r := a.Probe(filepath.Join(dir, "nope"), AccessRead); !r.Missing {
		t.Errorf("missing path: %+v", r)
	}
	for _, mask := range []AccessMask{AccessRead, AccessModify} {
		if r := a.Probe(file, mask); r.Denied || r.Busy || r.Err != nil || r.Missing || r.Dir {
			t.Errorf("healthy file, mask %d: %+v", mask, r)
		}
		if r := a.Probe(dir, mask); r.Denied || r.Busy || r.Err != nil || r.Missing || !r.Dir {
			t.Errorf("healthy dir, mask %d: %+v", mask, r)
		}
	}
	names, err := a.Children(dir)
	if err != nil || len(names) != 1 || names[0] != "f.txt" {
		t.Errorf("Children = %v, %v", names, err)
	}
}

// The probe is open-only: after probing, the entry is unchanged and nothing
// new appeared next to it.
func TestAccessProbeChangesNothing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	a := winAccess{}
	for _, mask := range []AccessMask{AccessRead, AccessModify} {
		a.Probe(file, mask)
		a.Probe(dir, mask)
	}
	after, err := os.Stat(file)
	if err != nil || after.Size() != 1 || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("file changed: %v %v", after, err)
	}
	names, _ := a.Children(dir)
	if len(names) != 1 {
		t.Errorf("entries after probing: %v", names)
	}
}

// denyEveryone applies a DENY ACE for Everyone with icacls and removes it in
// cleanup (before TempDir's own removal, which runs last).
func denyEveryone(t *testing.T, path, rights string) {
	t.Helper()
	t.Cleanup(func() {
		_ = exec.Command("icacls", path, "/remove:d", "*S-1-1-0").Run()
	})
	if out, err := exec.Command("icacls", path, "/deny", "*S-1-1-0:"+rights).CombinedOutput(); err != nil {
		t.Skipf("icacls could not set a deny ACE here: %v: %s", err, out)
	}
}

func TestAccessReadDeniedByDACL(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "inner.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	denyEveryone(t, locked, "(R)")
	a := winAccess{}
	if r := a.Probe(locked, AccessRead); !r.Denied {
		t.Errorf("read of a folder with a deny-read ACE: %+v", r)
	}
	if r := a.Probe(dir, AccessRead); r.Denied {
		t.Errorf("the parent must stay readable: %+v", r)
	}
}

func TestAccessModifyDeniedWhileReadIsFine(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	denyEveryone(t, file, "(DE,WA)")
	a := winAccess{}
	if r := a.Probe(file, AccessModify); !r.Denied {
		t.Errorf("modify with deny delete/write-attributes: %+v", r)
	}
	if r := a.Probe(file, AccessRead); r.Denied || r.Err != nil {
		t.Errorf("read must still work: %+v", r)
	}
}

// A file another process holds without sharing is "busy", never "denied":
// a sharing violation says nothing about rights.
func TestAccessSharingViolationIsBusy(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _ := windows.UTF16PtrFromString(file)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	a := winAccess{}
	for _, mask := range []AccessMask{AccessRead, AccessModify} {
		if r := a.Probe(file, mask); !r.Busy || r.Denied {
			t.Errorf("mask %d on an exclusively opened file: %+v", mask, r)
		}
	}
}

func TestAccessJunctionIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v: %s", err, out)
	}
	if r := (winAccess{}).Probe(link, AccessRead); !r.Reparse {
		t.Errorf("junction: %+v", r)
	}
	names, _ := winAccess{}.Children(dir)
	sort.Strings(names)
	if len(names) != 2 {
		t.Errorf("Children = %v", names)
	}
}
