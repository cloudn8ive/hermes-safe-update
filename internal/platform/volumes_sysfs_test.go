//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfs builds the part of /sys that blockRemovable reads: the device
// directories live under devices/, class/block/<name> are symlinks to them
// (as on a real system), and a partition reads its disk's removable flag
// through "..".
func fakeSysfs(t *testing.T, busDir, disk string, removable string, partitions ...string) string {
	t.Helper()
	root := t.TempDir()
	diskDir := filepath.Join(root, "devices", busDir, "block", disk)
	if err := os.MkdirAll(diskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(diskDir, "removable"), []byte(removable+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "class", "block"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(name, target string) {
		if err := os.Symlink(target, filepath.Join(root, "class", "block", name)); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
	}
	link(disk, diskDir)
	for _, p := range partitions {
		pd := filepath.Join(diskDir, p)
		if err := os.MkdirAll(pd, 0o755); err != nil {
			t.Fatal(err)
		}
		link(p, pd)
	}
	return root
}

func TestBlockRemovable(t *testing.T) {
	cases := []struct {
		name    string
		bus     string
		flag    string
		dev     string
		removed bool
	}{
		{"internal NVMe partition", "pci0000:00/0000:00:1d.0/nvme", "0", "/dev/nvme0n1p2", false},
		{"removable flag on the disk, asked for a partition", "pci0000:00/0000:00:14.0", "1", "/dev/sdb1", true},
		{"removable flag, asked for the disk itself", "pci0000:00/0000:00:14.0", "1", "/dev/sdb", true},
		{"USB hard disk that reports removable=0", "pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host6", "0", "/dev/sdb1", true},
		{"internal SATA disk", "pci0000:00/0000:00:17.0/ata1/host0", "0", "/dev/sdb1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := fakeSysfs(t, c.bus, "sdb", c.flag, "sdb1")
			if c.dev == "/dev/nvme0n1p2" {
				root = fakeSysfs(t, c.bus, "nvme0n1", c.flag, "nvme0n1p2")
			}
			if got := blockRemovable(root, c.dev); got != c.removed {
				t.Errorf("blockRemovable(%s) = %v, want %v", c.dev, got, c.removed)
			}
		})
	}
}
