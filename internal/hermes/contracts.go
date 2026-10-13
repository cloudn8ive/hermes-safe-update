package hermes

import (
	"regexp"
	"strconv"
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
	{"Resolving Python dependencies…", "pydeps"},
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

// FoundCommitsRe reads the commit count `hermes update` prints after its
// fetch, e.g. "→ Found 92 new commit(s)". It counts what the update really
// pulled, which can be more than the pre-flight check saw (Hermes fetches
// GitHub itself, minutes later).
var FoundCommitsRe = regexp.MustCompile(`Found (\d+) new commit\(s\)`)

// ParseFoundCommits returns that count from `hermes update` output
// (ok=false when the line is absent or not a positive number).
func ParseFoundCommits(output string) (n int, ok bool) {
	m := FoundCommitsRe.FindStringSubmatch(output)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

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
)

// GCSkippedMarker starts the line `hermes update` prints when its own cleanup
// of old dependency sets stopped early.
const GCSkippedMarker = "dependency generation cleanup skipped"

// GCLockedRe matches that line when the reason is a file held open by a
// running process (WinError 5 access denied or 32 sharing violation, e.g. a
// hardlinked .pyd of an old set). Other reasons do not match: they stay
// visible as Hermes printed them.
var GCLockedRe = regexp.MustCompile(GCSkippedMarker + `: \[WinError (?:5|32)\]`)

// GCGenerationRe reads the generation folder from such a line: the path up
// to and including "environments\<id>" (group 1) and the folder name (group
// 2). The path is a Python repr, so its backslashes arrive doubled.
var GCGenerationRe = regexp.MustCompile(`'?([^'"]*environments[\\/]+([0-9a-f]{8,64}))`)

// GCLocked is a skipped-cleanup line: one old dependency set that
// `hermes update` could not remove because a file in it was in use.
type GCLocked struct {
	Gen string // folder name of the set (normally 32 hex digits); "" if the path is unreadable
	Dir string // the set's folder with single separators; "" if the path is unreadable
}

// ParseGCLocked reports whether line is the "cleanup skipped, a file was in
// use" line, and which set it names.
func ParseGCLocked(line string) (GCLocked, bool) {
	if !GCLockedRe.MatchString(line) {
		return GCLocked{}, false
	}
	m := GCGenerationRe.FindStringSubmatch(line)
	if m == nil {
		return GCLocked{}, true
	}
	dir := strings.ReplaceAll(m[1], `\\`, `\`)
	return GCLocked{Gen: m[2], Dir: dir}, true
}

// CheckSaysBehind interprets `hermes update --check` output (rc must be 0).
func CheckSaysBehind(out string) bool {
	l := strings.ToLower(out)
	return (strings.Contains(l, "behind") || strings.Contains(l, "available")) && !strings.Contains(l, "up to date")
}

// Changed-file classification for the API/mirror paths (§2.3.1).
var (
	NPMFilesRe = regexp.MustCompile(`(^|/)package(-lock)?\.json$`)
	PyFilesRe  = regexp.MustCompile(PyFilesPattern)
)

// PyFilesPattern lists the files whose change makes `hermes update` lock and
// install Python dependencies again. Hermes' package manager (pm) compares a
// stamp of uv.lock, its own pm/lock.json (Python and tool pins) and the
// manifests of every enabled plugin that declares Python packages
// (plugin.yaml|yml|json, pyproject.toml): a plugin appearing, moving or
// changing re-resolves the environment even though uv.lock did not change.
// Measured: a bundled plugin that moved out of core (its plugin.yaml deleted)
// was installed into the user's plugins folder during the update and forced a
// full resolve and install although neither uv.lock nor pyproject.toml changed.
// A changed manifest of a plugin the user never enabled is a harmless false
// "changed" (only the estimate is longer); the other way round is not.
const PyFilesPattern = `^(?:uv\.lock|pyproject\.toml|pm/lock\.json|plugins/(?:.+/)?(?:plugin\.ya?ml|plugin\.json|pyproject\.toml))$`

// PyFilesPathspecs is PyFilesPattern as git pathspecs for `git diff --quiet`
// (the two lists are tested against each other).
var PyFilesPathspecs = []string{
	"uv.lock", "pyproject.toml", "pm/lock.json",
	":(glob)plugins/**/plugin.yaml", ":(glob)plugins/**/plugin.yml",
	":(glob)plugins/**/plugin.json", ":(glob)plugins/**/pyproject.toml",
}

// UnfinishedRe matches Hermes' "⚠ <what> did not finish: <reason>" lines
// (update_receipt.py follow-ups such as the web UI build, update_completion.py,
// the gateway migration). `hermes update` still exits 0 after them.
var UnfinishedRe = regexp.MustCompile(`(?m)^[ \t]*⚠[ \t]*(.+?) did not finish: (.*?)[ \t\r]*$`)

// unfinishedHintRe is the trailing advice in such a line, e.g.
// "(the next launch or `hermes update` retries it)" or
// "(run `hermes gateway migrate` later)".
var unfinishedHintRe = regexp.MustCompile(`\s*\(([^()]*(?:retries it|later)[^()]*)\)$`)

// CommandDumpStartRe matches a failure line that echoes an inline-code
// command ("⚠ web UI build failed: …python.exe -I -S -c import ctypes, …"):
// the script follows one output line per source line, and the dump ends with
// a line matching CommandDumpEndRe ("… exited 1").
var (
	CommandDumpStartRe = regexp.MustCompile(`^(\s*\S.*?\bfailed): \S.*\s-c\s`)
	CommandDumpEndRe   = regexp.MustCompile(`\bexited -?\d+\s*$`)
)

// Unfinished is one part of `hermes update` that did not finish.
type Unfinished struct {
	What   string // "Update follow-up 'build'"
	Reason string // shortened: "web UI build"
	Hint   string // "the next launch or `hermes update` retries it" (may be "")
}

// String is the one-line summary text.
func (u Unfinished) String() string {
	s := u.What
	if u.Reason != "" {
		s += ": " + u.Reason
	}
	if u.Hint != "" {
		s += "; " + u.Hint
	}
	return s
}

// ParseUnfinished reads one "did not finish" line (ok=false otherwise).
func ParseUnfinished(line string) (Unfinished, bool) {
	m := UnfinishedRe.FindStringSubmatch(line)
	if m == nil {
		return Unfinished{}, false
	}
	reason, hint := m[2], ""
	if h := unfinishedHintRe.FindStringSubmatchIndex(reason); h != nil {
		hint, reason = reason[h[2]:h[3]], reason[:h[0]]
	}
	return Unfinished{What: strings.TrimSpace(m[1]), Reason: shortReason(reason), Hint: hint}, true
}

// UnfinishedSteps lists every "did not finish" line in update output, deduplicated.
func UnfinishedSteps(out string) []Unfinished {
	var list []Unfinished
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if u, ok := ParseUnfinished(l); ok && !seen[u.String()] {
			seen[u.String()] = true
			list = append(list, u)
		}
	}
	return list
}

// shortReason keeps "web UI build" from "web UI build: <long command line>"
// and caps anything else at 120 characters.
func shortReason(s string) string {
	s = strings.TrimSpace(s)
	if head, rest, ok := strings.Cut(s, ": "); ok && len([]rune(rest)) > 60 &&
		len([]rune(head)) <= 60 && !strings.ContainsAny(head, `\/`) {
		return head
	}
	if r := []rune(s); len(r) > 120 {
		return string(r[:119]) + "…"
	}
	return s
}

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
