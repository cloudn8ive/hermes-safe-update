package settings

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// userDataArgRe finds Chromium's --user-data-dir switch (quoted or bare).
var userDataArgRe = regexp.MustCompile(`--user-data-dir=(?:"([^"]*)"|(\S+))`)

func userDataArg(cmdline string) string {
	m := userDataArgRe.FindStringSubmatch(cmdline)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	// A bare value can still carry a stray quote from the surrounding
	// command line; never let that turn "ours" into "another dir".
	return strings.Trim(m[2], `"'`)
}

func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// usesOtherUserData reports whether desktop main process main provably runs
// on a userData dir other than ours. The main process' command line usually
// has no --user-data-dir (the app sets it with app.setPath), but Chromium
// passes it to every helper child (--type=renderer/gpu/utility). Proof
// needs at least one child (created after main, so not a reused PPID) with
// the switch and no child or main switch naming ours. Anything else counts
// as "may be ours", so the caller refuses: an unknown instance is never
// guessed to be someone else's.
func usesOtherUserData(main platform.Process, all []platform.Process, ours string) bool {
	if ours == "" {
		return false
	}
	if d := userDataArg(main.Cmdline); d != "" {
		return !sameDir(d, ours)
	}
	other := false
	for _, c := range all {
		if c.PPID != main.PID || c.PID == main.PID {
			continue
		}
		if !main.Created.IsZero() && !c.Created.IsZero() && c.Created.Before(main.Created) {
			continue // PPID reused: not a child of this main process
		}
		d := userDataArg(c.Cmdline)
		if d == "" {
			continue
		}
		if sameDir(d, ours) {
			return false
		}
		other = true
	}
	return other
}
