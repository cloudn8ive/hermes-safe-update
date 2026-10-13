//go:build linux || darwin

package platform

import "testing"

// Volumes passes the fixed flag through, so the mirror location never lands
// on a stick or a share (mirror.Location skips Fixed == false).
func TestUnixDiskVolumesReportFixed(t *testing.T) {
	root := t.TempDir()
	d := unixDisk{mounts: func() []mountPoint {
		return []mountPoint{{Root: "/", Fixed: true}, {Root: root, Fixed: false}}
	}}
	vols, err := d.Volumes()
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("volumes = %+v", vols)
	}
	if !vols[0].Fixed || !vols[0].System {
		t.Errorf("/ must be the fixed system volume: %+v", vols[0])
	}
	if vols[1].Fixed || vols[1].System {
		t.Errorf("a removable/network root must not be fixed: %+v", vols[1])
	}
}
