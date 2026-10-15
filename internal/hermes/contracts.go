package hermes

import (
	"regexp"
	"sort"
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

// UpdateArgv is the update argv of the main channel: `--branch main` beats
// Hermes' own channel on every version, so this is also what runs on a
// Hermes without release channels. It is the legacy argv from before channels
// and stays byte for byte as it was. Runs use BuildUpdateArgv (channel.go),
// which returns exactly this for main (rule 6: no --gateway).
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
	// Older Hermes printed this line; v0.22 only publishes it to a status file
	// (never stdout), so it is a harmless no-op there. The two lines above
	// carry the step on v0.22.
	{"Updating Python dependencies", "pydeps"},
	{"Preparing Node dependencies…", "frontend"},
	{"Building the TUI…", "frontend"},
	{"Building the web UI…", "frontend"}, // source_build.py: label of the web UI build
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

// The update marker (update_lock.py, format v2; electron/update-marker.ts):
//
//	line 1  <pid>
//	line 2  <started_at, unix seconds>
//	line 3  ct:<owner creation time, unix seconds, 3 decimals>      (absent = v1)
//	line 4+ delegate:<pid> ct:<ct>   and   run:<id>                 (optional)
//
// An owner is live while its pid is alive AND its creation time still matches
// (MarkerCreateTimeToleranceS); a marker without the ct line is v1 and only
// ages out after MarkerStaleAfterS. The line shapes are matched case-sensitively
// per line, after BOM/CRLF/space-and-tab trimming.
var (
	MarkerCreateTimeRe = regexp.MustCompile(`^ct:([0-9]+(?:\.[0-9]+)?)$`)
	MarkerDelegateRe   = regexp.MustCompile(`^delegate:([0-9]+) ct:([0-9]+(?:\.[0-9]+)?)$`)
)

// MarkerCreateTimeToleranceS is the clock difference Hermes allows between a
// recorded and a probed process creation time (CREATE_TIME_TOLERANCE_SECONDS).
const MarkerCreateTimeToleranceS = 2.0

// UpdateExitConcurrent is `hermes update`'s exit code when another update (or
// instance) owns the install (UPDATE_EXIT_CONCURRENT in update_lock.py): the
// update did not start. Hermes prints one of two lines first.
const UpdateExitConcurrent = 2

var (
	// ConcurrentUpdateRe: "✗ Another Hermes update is already running (started 3m 4s ago, process 123)."
	ConcurrentUpdateRe = regexp.MustCompile(`Another Hermes update is already running[^\r\n]*`)
	// CannotLockRe: "✗ Cannot lock this install for the update: <reason>."
	CannotLockRe = regexp.MustCompile(`Cannot lock this install for the update:[ \t]*([^\r\n]*)`)
)

// ConcurrentUpdateNote explains an exit code 2 of `hermes update` in one
// clause for the RESULT line ("" when rc is not 2).
func ConcurrentUpdateNote(rc int, out string) string {
	if rc != UpdateExitConcurrent {
		return ""
	}
	if m := ConcurrentUpdateRe.FindString(out); m != "" {
		return "another Hermes update is running (" + strings.TrimRight(strings.TrimSpace(m), ".") + "); it holds the update lock, so this run's update did not start. Wait for it to finish, then run Safe Update again"
	}
	if m := CannotLockRe.FindStringSubmatch(out); m != nil {
		return "Hermes could not lock the install for the update: " + strings.TrimRight(strings.TrimSpace(m[1]), ".")
	}
	return "Hermes refused to start the update: another update or Hermes instance holds the install. Wait for it to finish, then run Safe Update again"
}

// ---- pre-flight probes (Safe Update v0.4.0) ----
//
// What Safe Update reads from Hermes BEFORE it closes anything. Nothing here
// is scraped from `hermes update` output; these are file names, byte ranges
// and tool locations Hermes uses, each pinned by a synthetic fixture under
// testdata/contracts/.

const (
	// CheckoutLockName is Hermes' checkout lock inside the git common dir
	// (update_lock.py GIT_CHECKOUT_LOCK_NAME): a kernel lock `hermes update`
	// holds for its whole process tree.
	CheckoutLockName = "hermes-update.lock"
	// CheckoutLockNameNoGit is the lock of an install with no .git (a ZIP
	// install), kept in the install root (CHECKOUT_LOCK_NAME).
	CheckoutLockNameNoGit = ".hermes-update.lock"
	// CheckoutLockOffset and CheckoutLockLength are the Windows byte range
	// of the lock (_WINDOWS_LOCK_OFFSET = 1 << 20; the owner's byte plus
	// _LEASE_SLOTS = 16 lease bytes). scripts/desktop-update/marker.ps1
	// probes exactly this with Lock(1048576, 17). A range far past the holder
	// record keeps the record readable while the lock is held.
	CheckoutLockOffset = 1 << 20
	CheckoutLockLength = 1 + 16
)

// ARM64 build tools (scripts/windows-build-deps.ps1: Get-HermesClang,
// Get-HermesArm64VisualStudio, Initialize-HermesArm64BuildTools). The update
// on a win32-arm64 host runs that script whenever a Python dependency set is
// rebuilt; it installs Visual Studio Build Tools (several GB, a UAC prompt)
// when either the ARM64 C++ tools or clang.exe is missing.
const (
	// ARM64Target is the target name in the managed Python folder
	// (tools/python-<version>-win32-arm64, pm/store.py current_target).
	ARM64Target = "win32-arm64"
	// ARM64ClangExe and ARM64ClangFormatExe: the half-installed Clang
	// component leaves clang-format.exe without clang.exe.
	ARM64ClangExe       = "clang.exe"
	ARM64ClangFormatExe = "clang-format.exe"
	// ARM64VswhereEnv is the environment variable whose folder holds
	// `Microsoft Visual Studio\Installer\vswhere.exe` (${env:ProgramFiles(x86)}).
	ARM64VswhereEnv = "ProgramFiles(x86)"
	// ARM64WingetLLVM is the winget package the issue's reporter used to fix
	// a missing clang.exe (unverified by Safe Update, which installs nothing).
	ARM64WingetLLVM = "LLVM.LLVM"
)

var (
	// ARM64VswhereRel is vswhere's place under ProgramFiles(x86).
	ARM64VswhereRel = []string{"Microsoft Visual Studio", "Installer", "vswhere.exe"}
	// ARM64VswhereArgs asks for the newest Visual Studio with the ARM64 C++
	// tools (-latest -products * -requires ...VC.Tools.ARM64).
	ARM64VswhereArgs = []string{"-latest", "-products", "*", "-requires", "Microsoft.VisualStudio.Component.VC.Tools.ARM64", "-property", "installationPath"}
	// ARM64ClangLLVMRel is clang.exe under ProgramFiles (standalone LLVM).
	ARM64ClangLLVMRel = []string{"LLVM", "bin"}
	// ARM64ClangVSRels are the folders under the Visual Studio install, in
	// the script's order, that may hold clang.exe.
	ARM64ClangVSRels = [][]string{
		{"VC", "Tools", "Llvm", "ARM64", "bin"},
		{"VC", "Tools", "Llvm", "bin"},
	}
)

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

// Hermes' other "owed work" wordings (update_completion.py, update_receipt.py,
// source_build.py, main_desktop.py). Each is one whole line; `hermes update`
// still exits 0 after them, so they are warnings, never a failed update.
var (
	// owedLineRes: line shape -> what is owed. Group 1 is the reason text.
	owedLineRes = []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`^[ 	]*(?:⚠[ 	]*)?Desktop app build owed:[ 	]*(.*?)[ 	\r]*$`), "Desktop app build owed"},
		{regexp.MustCompile(`^[ 	]*⚠[ 	]*Gateway restart still owed:[ 	]*(.*?)[ 	\r]*$`), "Gateway restart still owed"},
		{regexp.MustCompile(`^[ 	]*⚠[ 	]*Interrupted after the code was updated:[ 	]*(.*?)[ 	\r]*$`), "Interrupted after the code was updated"},
		{regexp.MustCompile(`^[ 	]*⚠[ 	]*Skipped rebuilding the desktop app:[ 	]*(.*?)[ 	\r]*$`), "Skipped rebuilding the desktop app"},
	}
	// stepFailedRe is source_build.py's "  ⚠ <name> failed: <reason>" (one line
	// per product that did not build; names have no colon).
	stepFailedRe = regexp.MustCompile(`^[ 	]*⚠[ 	]*([^:⚠]+?) failed:[ 	]*(.*?)[ 	\r]*$`)
)

// ParseOwed reads one line of the owed-work vocabulary other than "did not
// finish": "Desktop app build owed: …", "⚠ <name> failed: …", "⚠ Gateway
// restart still owed: …", "⚠ Interrupted after the code was updated: …",
// "⚠ Skipped rebuilding the desktop app: …". ok=false otherwise.
func ParseOwed(line string) (Unfinished, bool) {
	if UnfinishedRe.MatchString(line) {
		return Unfinished{}, false // ParseUnfinished's wording
	}
	for _, o := range owedLineRes {
		if m := o.re.FindStringSubmatch(line); m != nil {
			reason := strings.TrimRight(strings.TrimSpace(m[1]), ".,")
			return Unfinished{What: o.what, Reason: shortReason(reason)}, true
		}
	}
	if m := stepFailedRe.FindStringSubmatch(line); m != nil {
		reason := shortReason(m[2])
		if CommandDumpStartRe.MatchString(line) {
			reason = "command details in the log" // as the screen filter shows it
		}
		return Unfinished{What: strings.TrimSpace(m[1]) + " failed", Reason: reason}, true
	}
	return Unfinished{}, false
}

// UnfinishedSteps lists every part of the update that Hermes reported as not
// finished or still owed, deduplicated: "did not finish" follow-ups first,
// then the other owed-work lines (ParseOwed). A line that only repeats a
// follow-up already listed is dropped, so one failure is one row.
func UnfinishedSteps(out string) []Unfinished {
	var list []Unfinished
	seen := map[string]bool{}
	add := func(u Unfinished) {
		if !seen[u.String()] {
			seen[u.String()] = true
			list = append(list, u)
		}
	}
	lines := strings.Split(out, "\n")
	var followups strings.Builder // raw "did not finish" lines
	for _, l := range lines {
		if u, ok := ParseUnfinished(l); ok {
			add(u)
			followups.WriteString(l + "\n")
		}
	}
	followupText := followups.String()
	hasGatewayFollowup := strings.Contains(followupText, "'gateway_restart'")
	var owed []Unfinished
	for _, l := range lines {
		if u, ok := ParseOwed(l); ok {
			owed = append(owed, u)
		}
	}
	for _, u := range owed {
		name, failed := strings.CutSuffix(u.What, " failed")
		switch {
		case failed && strings.Contains(followupText, name+": "):
			continue // the follow-up names this failure
		case u.What == "Gateway restart still owed" && hasGatewayFollowup:
			continue // the gateway_restart follow-up says the same
		case u.What == "Desktop app build owed" && owedFailed(owed, u.Reason):
			continue // the "⚠ <name> failed" line says why
		}
		add(u)
	}
	return list
}

// owedFailed reports whether owed holds a "⚠ <name> failed" row matching the
// reason of a "Desktop app build owed: <name> failed" line.
func owedFailed(owed []Unfinished, reason string) bool {
	for _, o := range owed {
		if o.What != "" && o.What == reason {
			return true
		}
	}
	return false
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

// ---- Release channels (Hermes v0.21.6+; Safe Update v0.3.0) ----
//
// What `hermes update --check` and `hermes update` print on a release
// channel. Hermes at 1f6c02c07c (update_cmd_check.py, source_releases.py).
// All patterns match the ASCII tail of a line, not the leading glyph
// (→ ✓ ⚕ ✗ may be mangled by a console code page), and tolerate CRLF.

// UpdateChannelRe reads the channel from "→ Update channel: stable". Both
// `--check` and the apply print it (absent with an explicit --branch).
var UpdateChannelRe = regexp.MustCompile(`(?m)Update channel:[ \t]*([a-z0-9][a-z0-9-]*)[ \t\r]*$`)

// The release verdict lines of `update --check`. Labels read
// "<channel> v<version> (<sha12>)", e.g. "stable v0.22.1 (abcdef012345)".
var (
	checkAvailableRe  = regexp.MustCompile(`Selected release available:[ \t]*([a-z0-9][a-z0-9-]*) v(\S+) \(([0-9a-f]{7,40})\)`)
	checkUpToDateRe   = regexp.MustCompile(`Up to date with the latest release \(([a-z0-9][a-z0-9-]*) v(\S+) \(([0-9a-f]{7,40})\)\)`)
	checkWaitsRe      = regexp.MustCompile(`No newer release:[ \t]*([a-z0-9][a-z0-9-]*) v([^\s;()]+)`)
	checkRollbackRe   = regexp.MustCompile(`This checkout is newer than the selected release:[ \t]*([a-z0-9][a-z0-9-]*) v(\S+) \(([0-9a-f]{7,40})\)`)
	checkRollbackTail = "moves it back to that release"
	checkUnresolvedRe = regexp.MustCompile(`(?m)Could not resolve the (\S+) source channel:[ \t]*(.*?)[ \t\r]*$`)
)

// CheckVerdict is what `hermes update --check` concluded.
type CheckVerdict int

const (
	// VerdictUnknown: none of the known wordings matched. Never read this as
	// "up to date": callers warn and stop.
	VerdictUnknown CheckVerdict = iota
	// VerdictAvailable: a newer release (or, on main, newer commits) exists.
	VerdictAvailable
	// VerdictUpToDate: the checkout is at the selected release (or main's tip).
	VerdictUpToDate
	// VerdictWaitsForNext: "No newer release: …; this checkout is newer and
	// waits for the next release" (forward-only default channel).
	VerdictWaitsForNext
	// VerdictRollback: "This checkout is newer than the selected release":
	// `hermes update` would move the checkout BACK to that release.
	VerdictRollback
	// VerdictUnresolved: "Could not resolve the <channel> source channel:
	// <reason>" (GitHub unreachable, rate limited, ...); exit code 1.
	VerdictUnresolved
)

func (v CheckVerdict) String() string {
	switch v {
	case VerdictAvailable:
		return "available"
	case VerdictUpToDate:
		return "up-to-date"
	case VerdictWaitsForNext:
		return "waits-for-next-release"
	case VerdictRollback:
		return "rollback"
	case VerdictUnresolved:
		return "unresolved"
	}
	return "unknown"
}

// ChannelCheck is `hermes update --check` output, parsed.
type ChannelCheck struct {
	Channel string // "→ Update channel: X" ("" when the line is absent, e.g. with --branch)
	Verdict CheckVerdict
	Release string // channel named in the release label ("stable")
	Version string // "0.22.1" (no leading v)
	SHA12   string // the commit as printed, at most 12 hex digits
	Commits *int   // main-style output only: "N commits behind origin/main"
	Reason  string // Unresolved: Hermes' reason text
}

// Label is "stable v0.22.1" ("" when the output carried no release label).
func (c ChannelCheck) Label() string {
	if c.Release == "" || c.Version == "" {
		return ""
	}
	return c.Release + " v" + c.Version
}

func (c *ChannelCheck) label(m []string) {
	c.Release, c.Version = m[1], m[2]
	if len(m) > 3 {
		c.SHA12 = m[3][:min(12, len(m[3]))]
	}
}

// ParseChannelCheck reads the output of `hermes update --check` (exit codes
// are the caller's: Unresolved comes with rc 1). Order matters: the rollback
// and "waits" lines are release verdicts too and must not read as "available".
// Main-channel output ("Update available: N commits behind origin/main.",
// "Already up to date.") is understood as Available / UpToDate; text that
// matches nothing is VerdictUnknown.
func ParseChannelCheck(out string) ChannelCheck {
	var c ChannelCheck
	if m := UpdateChannelRe.FindStringSubmatch(out); m != nil {
		c.Channel = m[1]
	}
	switch {
	case checkUnresolvedRe.MatchString(out):
		m := checkUnresolvedRe.FindStringSubmatch(out)
		c.Verdict, c.Release, c.Reason = VerdictUnresolved, m[1], m[2]
	case checkRollbackRe.MatchString(out):
		c.Verdict = VerdictRollback
		c.label(checkRollbackRe.FindStringSubmatch(out))
	case strings.Contains(out, checkRollbackTail):
		c.Verdict = VerdictRollback
	case checkWaitsRe.MatchString(out):
		c.Verdict = VerdictWaitsForNext
		c.label(checkWaitsRe.FindStringSubmatch(out))
	case checkUpToDateRe.MatchString(out):
		c.Verdict = VerdictUpToDate
		c.label(checkUpToDateRe.FindStringSubmatch(out))
	case checkAvailableRe.MatchString(out):
		c.Verdict = VerdictAvailable
		c.label(checkAvailableRe.FindStringSubmatch(out))
	default:
		// main channel (or an old Hermes)
		lower := strings.ToLower(out)
		switch {
		case CheckSaysBehind(out):
			c.Verdict = VerdictAvailable
			if m := CheckCommitsRe.FindStringSubmatch(out); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					c.Commits = &n
				}
			}
		case strings.Contains(lower, "up to date"):
			c.Verdict = VerdictUpToDate
		}
	}
	return c
}

// UpdateRelease is what the output of a release-channel `hermes update`
// tells about the target.
type UpdateRelease struct {
	Channel   string // "→ Update channel: X" ("" when absent)
	Release   string // channel in the release label
	Version   string // "0.22.1"
	SHA12     string // from "→ Latest release: stable v0.22.1 (<sha12>)"
	Waits     bool   // "→ Release: …; this checkout is newer and waits for the next release"
	SwitchSHA string // "→ Switching to source commit <sha10>"
}

// Label is "stable v0.22.1" ("" when no release line was printed).
func (u UpdateRelease) Label() string {
	if u.Release == "" || u.Version == "" {
		return ""
	}
	return u.Release + " v" + u.Version
}

var (
	releaseLineRe = regexp.MustCompile(`(?m)(?:Latest release|Release):[ \t]*([a-z0-9][a-z0-9-]*) v([^\s;()]+)(?:[ \t]*\(([0-9a-f]{7,40})\))?(;[ \t]*this checkout is newer and waits)?`)
	switchingRe   = regexp.MustCompile(`Switching to source commit ([0-9a-f]{7,40})`)
)

// ParseUpdateRelease reads the channel and release lines of `hermes update`
// output. On a release channel "Found N new commit(s)" is never printed, so
// ParseFoundCommits is optional there.
func ParseUpdateRelease(out string) UpdateRelease {
	var u UpdateRelease
	if m := UpdateChannelRe.FindStringSubmatch(out); m != nil {
		u.Channel = m[1]
	}
	if m := releaseLineRe.FindStringSubmatch(out); m != nil {
		u.Release, u.Version, u.Waits = m[1], m[2], m[4] != ""
		if m[3] != "" {
			u.SHA12 = m[3][:min(12, len(m[3]))]
		}
	}
	if m := switchingRe.FindStringSubmatch(out); m != nil {
		u.SwitchSHA = m[1]
	}
	return u
}

// ---- release tags (the target of a stable update) ----

// StableTagRe is Hermes' STABLE_TAG_RE (update_channel.py): a stable release
// tag is strict `vX.Y.Z`, the major part at most three digits, no suffix.
// CalVer tags (v2026.9.24), canary tags (v0.22.0+canary.<ts>) and rc tags are
// not releases and never a target. internal/mirror keeps an identical copy
// (it cannot import this package); a parity test pins the two together.
var StableTagRe = regexp.MustCompile(`^v(?:0|[1-9]\d{0,2})\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$`)

// ReleaseTag is the git tag of a release version printed by Hermes
// ("0.22.1" -> "v0.22.1"), or "" when it is not a strict stable tag. A
// version is never turned into a tag any other way, and tags are never
// sorted to find a release: Hermes names the target, we only look it up.
func ReleaseTag(version string) string {
	tag := "v" + strings.TrimPrefix(version, "v")
	if StableTagRe.MatchString(tag) {
		return tag
	}
	return ""
}

// SetChannelDoneRe reads the confirmation `hermes update --set-channel X`
// prints on success: "Update channel for <install id>: <name>" (followed, for
// stable and canary, by a generic back-up warning that is not matched).
var SetChannelDoneRe = regexp.MustCompile(`(?m)^[ \t]*Update channel for ([0-9a-f]{16}): ([a-z0-9][a-z0-9-]*)[ \t\r]*$`)

// ---- Truthful verify (Safe Update v0.4.0) ----
//
// What Hermes prints when its own gateway relaunch or restart goes wrong, and
// the commands behind them. Hermes v0.21.6 and later (update_cmd_windows.py,
// gateway_windows.py, update_cmd_fleet.py). All patterns match the ASCII
// tail of a line, not the leading glyph, and tolerate CRLF.

// RelaunchUnverifiedMarkers are the wordings of one failure family: the
// updater's own check of the gateway it relaunched failed (#123971). They
// are substrings, any of them marks a line as "this is that family":
//
//   - "Windows gateway relaunch after update was not verified alive" is the
//     RuntimeError of _verify_relaunched_gateways_alive;
//   - "Windows gateway restart could not be verified" is the warning line
//     printed just before it;
//   - "Windows gateway recovery failed:" is the follow-up Hermes records when
//     the resume itself raised.
//
// In #123971 (an updater older than v0.21.6) this error ended `hermes update`
// with exit 1 after "Update complete!". Since v0.21.6 a failure after the
// commit point is an owed follow-up with exit 0 (Safe Update shows it as a
// "Not finished" row), so this family only reaches an exit code through an
// updater that is older than the code it installs, or before the commit point
// (then there is no "Update complete!" and it is never tolerated).
var RelaunchUnverifiedMarkers = []string{
	"Windows gateway relaunch after update was not verified alive",
	"Windows gateway restart could not be verified",
	"Windows gateway recovery failed:",
}

// UpdateDoneMarker is the line `hermes update` prints when the code update
// finished (also the "restart" step trigger).
const UpdateDoneMarker = "Update complete!"

// failureLineRe matches a line that reports a failure: a "✗" line, the last
// line of a Python traceback ("RuntimeError: …", "OSError: …"), or a bare
// "Error: …" / "fatal: …" at the start of a line (git, node and shell tools
// print those).
var failureLineRe = regexp.MustCompile(`✗|^[ \t]*(?:[A-Za-z_][A-Za-z0-9_]*\.)*[A-Za-z_][A-Za-z0-9_]*(?:Error|Exception|Exit):|^(?:Error|ERROR|error|fatal|FATAL):`)

func hasRelaunchMarker(line string) bool {
	for _, m := range RelaunchUnverifiedMarkers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// ToleratedRelaunchExit reports whether the output of a `hermes update` that
// exited 1 is explained by the relaunch-unverified family alone: the update
// finished ("Update complete!"), at least one line carries a marker, and every
// failure line (a "✗" line or a traceback's exception line) carries one too. A
// single unexplained failure line returns false. line is the first marker
// line, trimmed, for the summary.
func ToleratedRelaunchExit(out string) (line string, ok bool) {
	if !strings.Contains(out, UpdateDoneMarker) {
		return "", false
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case hasRelaunchMarker(l):
			if line == "" {
				line = strings.TrimLeft(l, "⚠\uFE0F✗✓ ") // without the glyph
			}
		case failureLineRe.MatchString(l):
			return "", false
		}
	}
	return line, line != ""
}

// GatewayHeldRe reads "⚠ Paused gateway(s) left stopped: <why>." Hermes prints
// it when it deliberately did not restart the gateways it paused (the checkout
// tree is not whole); its next `hermes` command (or `hermes update`) restarts
// them.
var GatewayHeldRe = regexp.MustCompile(`(?m)Paused gateway\(s\) left stopped:[ \t]*(.*?)\.?[ \t\r]*$`)

// ParseGatewayHeld returns Hermes' reason when the output says the paused
// gateways were left stopped on purpose.
func ParseGatewayHeld(out string) (why string, ok bool) {
	m := GatewayHeldRe.FindStringSubmatch(out)
	if m == nil {
		return "", false
	}
	return shortReason(m[1]), true
}

// GatewayStartFailedRe reads "✗ Gateway start via <how> FAILED — no stable
// gateway process detected": `hermes gateway start` could not bring a gateway
// up (for example a parent Job Object killed it).
var GatewayStartFailedRe = regexp.MustCompile(`Gateway start via (.+?) FAILED`)

// gatewayTaskRunRe reads the recovery hint `hermes gateway start` prints next
// to that failure when a Scheduled Task is registered: "Recovery: schtasks
// /Run /TN Hermes_Gateway …". The name is the profile-scoped one Hermes itself
// uses, so it is read here and never assumed.
var gatewayTaskRunRe = regexp.MustCompile(`schtasks /Run /TN ([^\s"'<>|]+)`)

// ParseGatewayTask returns the Scheduled Task name from Hermes' recovery hint
// ("" when no task is registered or the hint is absent). A token that starts
// with "/" or "-" is a switch of schtasks, never a task name, and is refused.
func ParseGatewayTask(out string) string {
	if m := gatewayTaskRunRe.FindStringSubmatch(out); m != nil && !strings.HasPrefix(m[1], "/") && !strings.HasPrefix(m[1], "-") {
		return m[1]
	}
	return ""
}

// GatewayStartFailed reports whether a `gateway start` run failed: a non-zero
// exit code, or Hermes' own "Gateway start via … FAILED" line.
func GatewayStartFailed(rc int, out string) bool {
	return rc != 0 || GatewayStartFailedRe.MatchString(out)
}

// The two "Restarting … gateway profile(s)" lines of `hermes update`. Hermes
// prints one for the fleet watcher (manual gateways) and one for the Windows
// resume; both listing the same profile means it was relaunched twice
// (#126821).
var (
	restartingManualRe  = regexp.MustCompile(`(?m)Restarting manual gateway profile\(s\):[ \t]*(.+?)[ \t\r]*$`)
	restartingWindowsRe = regexp.MustCompile(`(?m)Restarting Windows gateway profile\(s\):[ \t]*(.+?)[ \t\r]*$`)
)

// DoubleRelaunchProfiles returns the profiles (sorted) that both lines name.
// Different profiles in the two lines are legitimate and give nil.
func DoubleRelaunchProfiles(out string) []string {
	names := func(re *regexp.Regexp) map[string]bool {
		set := map[string]bool{}
		for _, m := range re.FindAllStringSubmatch(out, -1) {
			for _, p := range strings.Split(m[1], ",") {
				if p = strings.TrimSpace(p); p != "" {
					set[p] = true
				}
			}
		}
		return set
	}
	manual, win := names(restartingManualRe), names(restartingWindowsRe)
	var both []string
	for p := range manual {
		if win[p] {
			both = append(both, p)
		}
	}
	sort.Strings(both)
	return both
}

// Hermes' machine boundary for "how do I run hermes without its launcher":
// `<managed python> -I <checkout>/hermes_cli/_launchers.py
// --print-runtime-command -- <args>` prints the argv as one JSON array
// (_launchers.py print_runtime_command; the dashboard runs its own update that
// way). RuntimeArgv (launcherroute.go) validates what comes back.
const (
	LaunchersScript    = "hermes_cli/_launchers.py"
	RuntimeCommandFlag = "--print-runtime-command"
)
