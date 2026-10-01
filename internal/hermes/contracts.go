package hermes

import (
	"regexp"
	"strings"
)

// Contracts with Hermes' own wording (D14). Hermes output is scraped in
// these places only; when upstream rewords, update this file and its
// fixtures (contracts_test.go, testdata/contracts/). Callers that see none
// of the expected markers must log WARN "Hermes output changed: <what>"
// rather than assume success.
//
// Values come from python-behaviour.md §2.3-§2.11 and §3.1 (Hermes at
// 4e74031, 2026-10-01). Owner: I1a (T1 seeded the values).

// MarkerFile is the update-in-progress marker in HERMES_HOME: "<pid>\n<epoch>\n".
const MarkerFile = ".hermes-update-in-progress"

// MarkerStaleAfterS is when Hermes treats the marker as stale.
const MarkerStaleAfterS = 20 * 60

// GatewayStateFile is read for code_sha and pid.
const GatewayStateFile = "gateway_state.json"

// UpdateArgv is the only way the tool runs the update (rule 6: no --gateway).
var UpdateArgv = []string{"update", "--yes", "--keep-stash", "--branch", "main"}

// VerifyModuleArgv verifies the desktop build (D17).
var VerifyModuleArgv = []string{"--run-module", "hermes_cli.desktop_update_verify"}

// Trigger maps a substring of `hermes update` output to a step key.
type Trigger struct {
	Substr string
	Step   string
}

// UpdateTriggers: first matching substring wins (note the Unicode … and →).
var UpdateTriggers = []Trigger{
	{"Stopping Windows gateway", "gateway_stop"},
	{"→ Fetching updates", "fetch"},
	{"→ Pulling updates", "pull"},
	{"Installing Python dependencies…", "pydeps"},
	{"Updating Python dependencies", "pydeps"},
	{"Preparing Node dependencies…", "frontend"},
	{"Building the TUI…", "frontend"},
	{"Building desktop packaged app…", "desktop"},
	{"Packaging the desktop app…", "package"},
	{"Code updated!", "finalize"},
	{"Update complete!", "restart"},
}

// TriggerFor returns the step for an output line, or "".
func TriggerFor(line string) string {
	for _, t := range UpdateTriggers {
		if strings.Contains(line, t.Substr) {
			return t.Step
		}
	}
	return ""
}

const (
	// FetchingMarker in the output means `hermes update` got past its first second.
	FetchingMarker = "→ Fetching updates"
	// KnownFalseFleet: exit 1 with this text is OK when code_sha and the
	// desktop verify both pass (#93406 / #112558).
	KnownFalseFleet = "Fleet version check returned no rows"
	// CuaDeferred in update output triggers the cua-driver refresh step.
	CuaDeferred = "cua-driver refresh deferred"
	// CuaManualHint lines are hidden from the screen (we do it for the user).
	CuaManualHint = "computer-use install --upgrade"
	// CuaTaskName is the Windows logon task of the cua-driver daemon.
	CuaTaskName = "cua-driver-serve"
	// GateProbeTag prefixes the probe's single JSON result line.
	GateProbeTag = "SAFE-UPDATE-GATE "
)

// PauseGateErr matches the Windows gateway-mapping errors that mean "the
// update would abort before changing anything".
var PauseGateErr = regexp.MustCompile(`Could not (?:map Windows gateway PIDs to profiles|determine Windows gateway service ownership|discover Windows gateway PIDs before update|prepare Windows gateway pause for update)`)

// PauseGateRe finds that error in `hermes update` output.
var PauseGateRe = regexp.MustCompile(`RuntimeError: (` + PauseGateErr.String() + `[^\n]*)`)

// UpdateCompleteRe captures "<old> → <new>" from the final update line.
var UpdateCompleteRe = regexp.MustCompile(`Update complete! \((.+?) → (.+?)\)`)

// CheckCommitsRe reads the commit count from `hermes update --check`.
var CheckCommitsRe = regexp.MustCompile(`(\d+) commits? behind`)

// CuaInstalledRe reads the binary path from `hermes computer-use status`,
// e.g. "cua-driver: installed at <dir>\cua-driver.exe (0.21.0)": a trailing
// "(version)" is not part of the path (the Python has no end anchor).
var CuaInstalledRe = regexp.MustCompile(`(?i)installed at (.+?cua-driver\.exe|.+?cua-driver)(?:\s+\(.*\))?\s*$`)

// GCRemoveRe / GCDoneRe parse the generation-cleanup script output.
var (
	GCRemoveRe = regexp.MustCompile(`REMOVE \(~(\d+) MB`)
	GCDoneRe   = regexp.MustCompile(`(\d+) dependency generation\(s\).*?disk free change ([+-]\d+) MB`)
	GCLockedRe = regexp.MustCompile(`dependency generation cleanup skipped: \[WinError (?:5|32)\].*environments[\\/]([0-9a-f]{8})`)
)

// CheckSaysBehind interprets `hermes update --check` output (rc must be 0).
func CheckSaysBehind(out string) bool {
	l := strings.ToLower(out)
	return (strings.Contains(l, "behind") || strings.Contains(l, "available")) && !strings.Contains(l, "up to date")
}

// Changed-file classification for the API/mirror paths (§2.3.1).
var (
	NPMFilesRe = regexp.MustCompile(`(^|/)package(-lock)?\.json$`)
	PyFilesRe  = regexp.MustCompile(`^(uv\.lock|pyproject\.toml)$`)
)

// HiddenUpdateLines are upstream tips dropped from the screen (log keeps them).
var HiddenUpdateLines = []string{
	"Tip: You can now select a provider and model",
	"hermes model              # Select provider and model",
}

// DesktopDefaultPortRe finds DEFAULT_PORT in apps/desktop/electron/renderer-server.ts (D9).
var DesktopDefaultPortRe = regexp.MustCompile(`(?m)\bDEFAULT_PORT\s*=\s*(\d{2,5})\b`)

// RendererServerSource is where DEFAULT_PORT lives, relative to the checkout.
const RendererServerSource = "apps/desktop/electron/renderer-server.ts"

// DesktopMainSource is the Electron main process source, relative to the checkout.
const DesktopMainSource = "apps/desktop/electron/main.ts"

// RendererFileLoadRe matches a renderer loaded from disk in main.ts, e.g.
// pathToFileURL(resolveRendererIndex()) (builds after renderer-server.ts was removed).
var RendererFileLoadRe = regexp.MustCompile(`pathToFileURL\(\s*(?:[\w.]+\s*\|\|\s*)?resolveRendererIndex\b`)

// RendererServerMentionRe matches an import of the (removed) renderer-server module.
var RendererServerMentionRe = regexp.MustCompile(`(?:from|import|require\()\s*['"][^'"]*renderer-server`)

// VersionInstallDirRe reads the "Install directory:" line of `hermes --version`
// (launcher selection, platforms.md §2).
var VersionInstallDirRe = regexp.MustCompile(`(?m)^Install directory:[ \t]*(.+?)[ \t\r]*$`)

// VersionHeadRe reads the first `hermes --version` line, e.g.
// "Hermes Agent v0.21.5+5279.g4e74031 (2026.9.24) · upstream 234badf4".
var VersionHeadRe = regexp.MustCompile(`(?m)^Hermes Agent (\S+)(?: \(([^)]*)\))?`)

// VersionMethodRe reads "Install method: git".
var VersionMethodRe = regexp.MustCompile(`(?m)^Install method:[ \t]*(.+?)[ \t\r]*$`)

// ElectronPathFile names the file in node_modules/electron holding the
// platform leaf of the Electron executable.
const ElectronPathFile = "path.txt"
