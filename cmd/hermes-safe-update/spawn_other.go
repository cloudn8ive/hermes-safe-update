//go:build !windows

package main

import "fmt"

// spawnsOwnWindow is false here: there is no console window of its own to
// open on macOS/Linux terminals, so the update flow offers the command
// instead of promising a window (see update.Deps.SpawnMirrorSetup).
const spawnsOwnWindow = false

// spawnMirrorSetup has no "own window" on macOS/Linux terminals; the offer
// prints the command instead. Untested on a real macOS or Linux machine.
func spawnMirrorSetup(exe string) error {
	return fmt.Errorf("run `%s mirror setup` in another terminal", exe)
}
