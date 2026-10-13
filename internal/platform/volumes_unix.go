package platform

// Pure helpers that decide which mounted volumes count as "fixed" (a place
// for a large, long-lived folder such as the git mirror). No build
// constraint so they are tested on every OS; the real statfs and /proc reads
// are in unixreal_unix.go, platform_linux.go and platform_darwin.go.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BSD statfs flags (sys/mount.h): MNT_LOCAL is set for volumes that are not
// network mounts; MNT_REMOVABLE marks removable media.
const (
	mntRemovable = 0x200
	mntLocal     = 0x1000
)

// darwinFixed reports whether a /Volumes entry with these statfs f_flags is
// a local, non-removable disk.
func darwinFixed(flags uint32) bool {
	return flags&mntLocal != 0 && flags&mntRemovable == 0
}

// blockRemovable reports whether the block device dev (a resolved /dev path
// such as /dev/sdb1) sits on removable or USB-attached media, read from the
// sysfs tree under sysRoot (/sys). Unknown devices (no sysfs entry, e.g.
// device-mapper names) count as fixed, as every volume did before.
func blockRemovable(sysRoot, dev string) bool {
	name := filepath.Base(dev)
	blk := sysRoot + "/class/block/" + name // no Clean: ".." must follow the symlink
	b, err := os.ReadFile(blk + "/removable")
	if err != nil {
		b, err = os.ReadFile(blk + "/../removable") // a partition: its disk's flag
	}
	if err == nil && strings.TrimSpace(string(b)) == "1" {
		return true
	}
	// A USB disk usually reports removable=0; the bus shows in the device
	// path (/devices/pci…/usb2/2-1/…). Judge only the part below sysRoot.
	if real, err := filepath.EvalSymlinks(blk); err == nil {
		if base, err := filepath.EvalSymlinks(sysRoot); err == nil {
			if rel, err := filepath.Rel(base, real); err == nil && usbBusRe.MatchString("/"+filepath.ToSlash(rel)) {
				return true
			}
		}
	}
	return false
}

var usbBusRe = regexp.MustCompile(`/usb[0-9]+(/|$)`)
