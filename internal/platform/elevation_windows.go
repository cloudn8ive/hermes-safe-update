//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// tokenElevationTypeFull is TOKEN_ELEVATION_TYPE TokenElevationTypeFull: the
// elevated half of a split token. The other values are Default (1: UAC off,
// or a standard user: no split token) and Limited (3: the filtered half).
const tokenElevationTypeFull = 2

// winElevation reads the elevation type of this process's token. It
// deliberately ignores TokenElevation (which is also true for an
// administrator with UAC off, or the built-in Administrator account): there
// is no normal-user session next to that token to leave damaged files for, so
// those users must not be locked out.
type winElevation struct{}

func (winElevation) Elevated() (bool, error) {
	var kind, ret uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevationType,
		(*byte)(unsafe.Pointer(&kind)), uint32(unsafe.Sizeof(kind)), &ret)
	if err != nil {
		return false, err
	}
	return kind == tokenElevationTypeFull, nil
}
