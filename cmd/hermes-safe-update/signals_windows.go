//go:build windows

package main

import "os"

// shutdownSignals on Windows: only console Ctrl+C / Ctrl+Break arrive as
// os.Interrupt. SIGTERM and SIGHUP are never delivered there.
var shutdownSignals = []os.Signal{os.Interrupt}
