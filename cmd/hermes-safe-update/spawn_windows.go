//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// spawnMirrorSetup starts `mirror setup --yes --pause` in its own console
// window, so the 10-20 min download never holds up the update window.
func spawnMirrorSetup(exe string) error {
	if exe == "" {
		return fmt.Errorf("own executable path unknown")
	}
	c := exec.Command(exe, "mirror", "setup", "--yes", "--pause")
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE | windows.CREATE_NEW_PROCESS_GROUP}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
