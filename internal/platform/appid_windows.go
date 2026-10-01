//go:build windows

package platform

import (
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ApplyAppID is the hidden `--appid <hwnd>` child mode (python-behaviour
// §6.3): it gives the console window its own AppUserModelID and blocks
// pinning (no relaunch command is registered). It runs in a short-lived
// child so a COM fault cannot take the updater down. Exit codes: 0 ok,
// 1 COM/property failure, 2 not a window.
func ApplyAppID(appID, hwndText string) int {
	v, err := strconv.ParseUint(hwndText, 10, 64)
	if err != nil || v == 0 {
		return 2
	}
	hwnd := windows.HWND(uintptr(v))
	if r, _, _ := procIsWindowW.Call(uintptr(hwnd)); r == 0 {
		return 2
	}
	if windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED) != nil {
		return 1
	}
	defer windows.CoUninitialize()
	var store *comObject
	r, _, _ := procSHGetPropertyStoreForWindow.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&store)))
	if r != 0 || store == nil {
		return 1
	}
	defer vcall(store, slotRelease)
	id, err := windows.UTF16PtrFromString(appID)
	if err != nil {
		return 1
	}
	idVar := propVariant{vt: 31, val: uint64(uintptr(unsafe.Pointer(id)))} // VT_LPWSTR
	pinVar := propVariant{vt: 11, val: 0xFFFF}                             // VT_BOOL VARIANT_TRUE
	keyID := propertyKey{fmtid: pkeyAppUserModel, pid: 5}
	keyPin := propertyKey{fmtid: pkeyAppUserModel, pid: 9}
	if vcall(store, slotSetValue, uintptr(unsafe.Pointer(&keyID)), uintptr(unsafe.Pointer(&idVar))) != 0 {
		return 1
	}
	if vcall(store, slotSetValue, uintptr(unsafe.Pointer(&keyPin)), uintptr(unsafe.Pointer(&pinVar))) != 0 {
		return 1
	}
	if vcall(store, slotCommit) != 0 {
		return 1
	}
	return 0
}

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is the 24-byte (amd64/arm64) PROPVARIANT with an 8-byte value.
type propVariant struct {
	vt       uint16
	_        [3]uint16
	val      uint64
	reserved uint64
}

const (
	slotSetValue = 6
	slotCommit   = 7
)

var (
	procIsWindowW                   = user32.NewProc("IsWindow")
	procSHGetPropertyStoreForWindow = shell32.NewProc("SHGetPropertyStoreForWindow")
	iidPropertyStore                = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	pkeyAppUserModel                = windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}
)
