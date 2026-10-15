//go:build windows

package platform

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// winAccess probes access by opening the entry with the current token:
// CreateFile with an open-only disposition (OPEN_EXISTING), every share
// mode and backup semantics (so folders open). Nothing is created, renamed,
// deleted or written. The open IS the test; ACLs are never read or judged.
type winAccess struct{}

// File access rights not named by x/sys/windows.
const (
	fileAddFile         = 0x00000002 // FILE_ADD_FILE (folder)
	fileAddSubdirectory = 0x00000004 // FILE_ADD_SUBDIRECTORY (folder)
	accessDelete        = 0x00010000 // DELETE
)

// accessRights is the desired access for one probe.
func accessRights(mask AccessMask, dir bool) uint32 {
	switch mask {
	case AccessModify:
		r := uint32(accessDelete | windows.FILE_WRITE_ATTRIBUTES)
		if dir {
			r |= fileAddFile | fileAddSubdirectory
		}
		return r
	default:
		if dir {
			return windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES
		}
		return windows.FILE_READ_DATA | windows.FILE_READ_ATTRIBUTES
	}
}

func (winAccess) Probe(path string, mask AccessMask) AccessResult {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return AccessResult{Err: err}
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return classifyAccessErr(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return AccessResult{Reparse: true}
	}
	dir := attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	h, err := windows.CreateFile(p, accessRights(mask, dir),
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		r := classifyAccessErr(err)
		r.Dir = dir
		return r
	}
	_ = windows.CloseHandle(h)
	return AccessResult{Dir: dir}
}

func classifyAccessErr(err error) AccessResult {
	switch {
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return AccessResult{Missing: true}
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return AccessResult{Denied: true}
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return AccessResult{Busy: true}
	}
	return AccessResult{Err: err}
}

func (winAccess) Children(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, nil
}
