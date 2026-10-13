package platform

import (
	"reflect"
	"testing"
)

func TestDarwinFixed(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags uint32
		want  bool
	}{
		{"local internal disk", mntLocal, true},
		{"local plus other flags", mntLocal | 0x4000 | 0x1, true},
		{"network share (not MNT_LOCAL)", 0x1, false},
		{"local but removable media", mntLocal | mntRemovable, false},
		{"no flags", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := darwinFixed(c.flags); got != c.want {
				t.Errorf("darwinFixed(%#x) = %v, want %v", c.flags, got, c.want)
			}
		})
	}
}

func TestParseMountDevsKeepsTheDevice(t *testing.T) {
	in := "/dev/nvme0n1p2 / ext4 rw 0 0\n" +
		"//nas/share /mnt/nas cifs rw 0 0\n" +
		"/dev/sdb1 /media/u/STICK vfat rw 0 0\n" +
		"/dev/loop3 /snap/x squashfs ro 0 0\n"
	want := []mountDev{{"/", "/dev/nvme0n1p2"}, {"/media/u/STICK", "/dev/sdb1"}}
	if got := parseMountDevs(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A device sysfs does not know (device-mapper names, a missing /sys) stays
// fixed, as every volume was before.
func TestBlockRemovableUnknownDeviceIsFixed(t *testing.T) {
	if blockRemovable(t.TempDir(), "/dev/mapper/vg-root") {
		t.Error("an unknown device must count as fixed")
	}
}
