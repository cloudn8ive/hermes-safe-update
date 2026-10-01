//go:build !windows

package execx

import "os/exec"

func setSysProcAttr(*exec.Cmd, bool) {}
