package main

import "runtime/debug"

// readBuildInfo is debug.ReadBuildInfo; resolveVersion takes it as a
// parameter so tests can feed any build info.
func readBuildInfo() (*debug.BuildInfo, bool) { return debug.ReadBuildInfo() }

// resolveVersion is the version shown on screen, in --version and in the log.
//
// A build that was given a real version (-X main.version=..., which is what
// release builds and scripts/build-all.* do) reports exactly that string, so
// release binaries stay byte-identical wherever they are built. Only the
// placeholder "dev" (or nothing) is replaced: by the commit Go stamped into
// the binary ("dev+3a8a2c7", ".dirty" when the tree had local changes), or
// plain "dev" when the binary carries no VCS information (-buildvcs=false,
// a source archive, go run of a file).
func resolveVersion(ldflag string, info func() (*debug.BuildInfo, bool)) string {
	if ldflag != "" && ldflag != "dev" {
		return ldflag
	}
	bi, ok := info()
	if !ok || bi == nil {
		return "dev"
	}
	var rev string
	dirty := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	v := "dev+" + rev
	if dirty {
		v += ".dirty"
	}
	return v
}
