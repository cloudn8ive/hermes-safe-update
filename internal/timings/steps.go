package timings

// Step lists (python-behaviour.md §3.2). Data owned by T1; I1b may add
// steps (and must keep hermes.UpdateTriggers in sync, rules doc #10).
// Changes vs Python: "tile" dropped (now a hook); "settings" (desktop
// settings migration) and "hooks" (user post-update hooks) added.

// UpdateSteps returns the step list of a real update run, seeded with
// defaults from config tunables.step_seconds.
func UpdateSteps(seed map[string]int) []Step {
	return withSeed(seed, []Step{
		{Key: "local", Group: "Check", Label: "Checking disk, checkout and gateway"},
		{Key: "remote", Group: "Check", Label: "Checking for updates", Cond: "source", Class: "network"},
		{Key: "procs", Group: "Check", Label: "Looking for running Hermes processes"},
		{Key: "wait", Group: "Close Hermes", Label: "Waiting for idle sessions / your OK"},
		{Key: "close", Group: "Close Hermes", Label: "Closing the desktop app"},
		{Key: "gateway_stop", Group: "Update", Label: "Pausing the gateway"},
		{Key: "fetch", Group: "Update", Label: "Fetching the new commits", Cond: "mirror", Class: "network"},
		{Key: "pull", Group: "Update", Label: "Pulling code, clearing caches", Cond: "mirror", Class: "network"},
		{Key: "pydeps", Group: "Update", Label: "Installing Python dependencies", Cond: "pydeps"},
		{Key: "frontend", Group: "Build", Label: "Building the TUI and web UI", Cond: "npm", Class: "machine"},
		{Key: "desktop", Group: "Build", Label: "Building the desktop app", Class: "machine"},
		{Key: "package", Group: "Build", Label: "Packaging the desktop app", Class: "machine"},
		{Key: "finalize", Group: "Finish", Label: "Syncing skills and config", Cond: "mirror", Class: "network"},
		{Key: "restart", Group: "Finish", Label: "Restarting the gateway"},
		{Key: "verify", Group: "Finish", Label: "Verifying the new version"},
		{Key: "settings", Group: "Finish", Label: "Migrating desktop settings"},
		{Key: "hooks", Group: "Finish", Label: "Running your hooks"},
		{Key: "relaunch", Group: "Finish", Label: "Reopening Hermes"},
		{Key: "cleanup", Group: "Finish", Label: "Removing old dependency sets"},
		{Key: "cua", Group: "Finish", Label: "Refreshing cua-driver"},
	})
}

// CheckSteps returns the step list of `check` / `--check`.
func CheckSteps(seed map[string]int) []Step {
	return withSeed(seed, []Step{
		{Key: "local", Group: "Local", Label: "Checking disk, checkout and gateway"},
		{Key: "remote", Group: "Updates", Label: "Checking for updates", Cond: "source", Class: "network"},
		{Key: "procs", Group: "Hermes", Label: "Looking for running Hermes processes"},
		{Key: "sessions", Group: "Hermes", Label: "Checking for active sessions"},
		{Key: "gc_preview", Group: "Housekeeping", Label: "Previewing dependency cleanup"},
		{Key: "cua_status", Group: "Housekeeping", Label: "Checking the cua-driver logon task"},
	})
}

// PreflightKeys are the steps a "nothing to update" run still measures.
var PreflightKeys = []string{"local", "remote", "procs"}

func withSeed(seed map[string]int, steps []Step) []Step {
	for i := range steps {
		steps[i].DefaultS = seed[steps[i].Key]
	}
	return steps
}
