//go:build !windows

package main

import "fmt"

// spawnMirrorSetup has no "own window" on macOS/Linux terminals; the offer
// prints the command instead. Untested on a real macOS or Linux machine.
func spawnMirrorSetup(exe string) error {
	return fmt.Errorf("run `%s mirror setup` in another terminal", exe)
}
