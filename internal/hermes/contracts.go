package hermes

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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

// ElectronPackageDirs lists where npm may have put the `electron` package
// of a Hermes checkout, in the order Node resolves `require("electron")`
// from apps/desktop: the desktop app's own node_modules first, then the
// checkout root's. Older Hermes installs it in apps/desktop only; since
// "pin Electron 44.9.3" (21693bdf29) npm hoists it to the root and the
// desktop folder has none.
func ElectronPackageDirs(checkout string) []string {
	return []string{
		filepath.Join(checkout, "apps", "desktop", "node_modules", "electron"),
		filepath.Join(checkout, "node_modules", "electron"),
	}
}

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
	// "→ Holding at this build: stable v0.22.0 (abc...) predates the gateway
	// runtime your sessions use." (update_cmd_check.py, Hermes 0.22+): Hermes
	// itself holds the checkout, because the release is older than the gateway
	// runtime a state.db already uses. For Safe Update this is the same
	// outcome as "newer than the selected release": the release is not
	// something to move to, so the run waits (the hold) and never runs the update.
	checkHoldingRe    = regexp.MustCompile(`Holding at this build:[ 	]*([a-z0-9][a-z0-9-]*) v(\S+) \(([0-9a-f]{7,40})\)`)
	checkHoldingTail  = "predates the gateway runtime"
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
	case checkHoldingRe.MatchString(out):
		c.Verdict = VerdictRollback
		c.label(checkHoldingRe.FindStringSubmatch(out))
	case strings.Contains(out, checkHoldingTail):
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

// ---- Apply-time hold by Hermes (Hermes v0.22.7 and v0.23.0; Safe Update v0.8.1) ----
//
// `hermes update` itself can hold instead of updating (update_cmd.py
// hold_channel_release_behind_runtime, update_cmd_check.py): the channel's
// release predates the gateway runtime the sessions use, so moving there
// would be a downgrade. It prints two lines, finishes the "already up to
// date" tail (dependency sync, gateway) on the SAME code and exits 0:
//
//	→ This build is newer than the <ch> release <sha12>: that release predates the gateway runtime your sessions use, so there is nothing to update to yet.
//	  Hermes stays on <head12> until a newer <ch> release ships. To follow every commit instead: `hermes update --set-channel main`.
//	✓ Holding at this build until a newer <ch> release ships.
//
// The channel is any release channel name. Like the other patterns the lines
// are matched on their ASCII text; the leading glyph may be mangled (0-4
// non-space characters, then a blank) and CRLF is tolerated. The final "✓
// Holding ..." line is the one that proves the hold happened (the "→" line
// alone is also printed before a refusal). The "✓ Holding" line is normally
// there; when this run's local changes were stashed and not restored, Hermes
// (update_cmd.py _complete_source_update) replaces that completion line with
//
//	⚠ hermes update stashed <n> local modification(s) and did NOT restore them.
//	  Stash ref: <ref>
//
// and only the "→ This build is newer ... nothing to update to yet" line is
// left, so that line alone counts as the hold too (the caller still wants
// exit 0, no "Update complete!" and an unmoved HEAD). The check-path line
// "→ Holding at this build: ..." never matches.
var (
	applyHoldRe     = regexp.MustCompile(`(?m)^[ \t]*(?:\S{1,4}[ \t]+)?Holding at this build until a newer ([a-z0-9][a-z0-9-]*) release ships\.?[ \t\r]*$`)
	applyStashRe    = regexp.MustCompile(`(?m)^[ \t]*(?:\S{1,4}[ \t]+)?hermes update stashed ([0-9]{1,6}) local modification\(s\) and did NOT restore them\.?[ \t\r]*$`)
	applyStashRefRe = regexp.MustCompile(`(?m)^[ \t]*Stash ref: ([A-Za-z0-9@{}:/._-]{1,80})[ \t\r]*$`)
	applyHoldWhyRe  = regexp.MustCompile(`(?m)^[ \t]*(?:\S{1,4}[ \t]+)?This build is newer than the ([a-z0-9][a-z0-9-]*) release ([0-9a-f]{7,40}): that release predates the gateway runtime your sessions use, so there is nothing to update to yet\.?[ \t\r]*$`)
)

// ApplyHold is a hold printed by `hermes update`.
type ApplyHold struct {
	Channel string // the release channel named in the "Holding" line
	SHA12   string // the release Hermes did not move to (empty when its "→" line is absent)
	// StashFiles/StashRef: Hermes stashed local changes this run and did not
	// restore them (0/empty: no such notice).
	StashFiles int
	StashRef   string
}

// Why is the reason for the card, in Safe Update's words.
func (h ApplyHold) Why() string {
	rel := h.Channel + " release"
	if h.SHA12 != "" {
		rel += " " + h.SHA12
	}
	return "the " + rel + " predates the gateway runtime your sessions use"
}

// ParseApplyHold reads the hold out of `hermes update` output (ok=false:
// no hold). It says nothing about whether the checkout moved: the caller
// compares HEAD.
func ParseApplyHold(out string) (ApplyHold, bool) {
	w := applyHoldWhyRe.FindStringSubmatch(out)
	var h ApplyHold
	if m := applyHoldRe.FindStringSubmatch(out); m != nil {
		h.Channel = m[1]
		if w != nil && w[1] == h.Channel {
			h.SHA12 = w[2][:min(12, len(w[2]))]
		}
	} else if w != nil {
		// the unrestored-stash notice replaced the "✓ Holding" line
		h.Channel, h.SHA12 = w[1], w[2][:min(12, len(w[2]))]
	} else {
		return ApplyHold{}, false
	}
	if s := applyStashRe.FindStringSubmatch(out); s != nil {
		h.StashFiles, _ = strconv.Atoi(s[1])
		if ref := applyStashRefRe.FindStringSubmatch(out); ref != nil {
			h.StashRef = ref[1]
		}
	}
	return h, true
}

// ---- New-catalog plugin notice (Hermes v0.23.0; Safe Update v0.8.1) ----
//
// At the end of `hermes update`, per profile with catalog plugins left on an
// old pin (plugins_auto_update.py report_catalog_updates_after_hermes_update):
//
//	→ 2 plugin update(s) from the new catalog in this profile: a, b
//	  apply: hermes plugins update   (or turn on: hermes plugins auto-update on)
//
// The label is "this profile" for the active home, else a sibling profile's
// label. Plugins installed with --ref are not counted. Only the first line is
// read; the "apply:" line is Hermes' advice, not something to match.
var catalogNoticeRe = regexp.MustCompile(`(?m)^[ \t]*(?:\S{1,4}[ \t]+)?(\d+) plugin update\(s\) from the new catalog in (.+?): (\S[^\r\n]*?)[ \t\r]*$`)

// ThisProfileLabel is the label Hermes gives the active profile.
const ThisProfileLabel = "this profile"

// CatalogNotice is one profile's line of that notice.
type CatalogNotice struct {
	Label string   // "this profile" or a sibling profile's label
	Count int      // the number Hermes printed
	Names []string // plugin names that are safe to show and to compare
	// Dropped counts names that were not a plausible plugin name; they are
	// neither shown nor used.
	Dropped int
}

// ParseCatalogNotices reads every profile's notice line of `hermes update`
// output, in order. A line whose count or names cannot be read is skipped.
func ParseCatalogNotices(out string) []CatalogNotice {
	var res []CatalogNotice
	for _, m := range catalogNoticeRe.FindAllStringSubmatch(out, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			continue
		}
		c := CatalogNotice{Label: strings.TrimSpace(m[2]), Count: n}
		for _, name := range strings.Split(m[3], ",") {
			name = strings.TrimSpace(name)
			switch {
			case name == "":
			case SafePluginName(name):
				c.Names = append(c.Names, name)
			default:
				c.Dropped++
			}
		}
		if len(c.Names) == 0 {
			continue
		}
		res = append(res, c)
	}
	return res
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

// DesktopForceBuildCmd is Hermes' own full rebuild of the desktop app
// (`desktop --force-build`, main_desktop.py "Force a full rebuild even if the
// content stamp matches"). Safe Update only names it, never runs it.
const DesktopForceBuildCmd = "hermes desktop --force-build"

// Hermes' machine boundary for "how do I run hermes without its launcher":
// `<managed python> -I <checkout>/hermes_cli/_launchers.py
// --print-runtime-command -- <args>` prints the argv as one JSON array
// (_launchers.py print_runtime_command; the dashboard runs its own update that
// way). RuntimeArgv (launcherroute.go) validates what comes back.
const (
	LaunchersScript    = "hermes_cli/_launchers.py"
	RuntimeCommandFlag = "--print-runtime-command"
)

// ---- Update receipts (v0.5.0) ----
//
// Hermes v0.21.6 and later write a receipt for every `hermes update`
// (hermes_cli/update_receipt.py; v0.21.6 has the same module as the current
// one: the running record at the start, pid_create_time, writer_pid, stages,
// reconcile_interrupted_runs): the run's own file `update_<date>_<time>_
// <pid>_<id>.json` and a copy of the newest one as `latest.json`, in
// `logs/update_receipts/` of the root home. The record is written as
// "running" the moment the run begins and is rewritten when it ends. A run
// that was killed (a crash, a power loss) leaves it "running" with a dead
// owner; the next `hermes update` marks such a record "interrupted"
// (reconcile_interrupted_runs; it keeps finished_at empty, so the time of
// the stop stays the last stage). A Hermes without receipts has no such
// directory: that is "nothing to see", never an error.
//
// latest.json is NOT the newest update: the plugin manager's sync and
// plugin-check receipts (`pm_<stamp>-<kind>-<pid>-<id>.json`, pm/receipt.py)
// replace it too, with `"kind": "sync"|"plugin-check"` and an outcome such as
// "ok" or "updates-available", every time Hermes starts. So the newest
// `update_*.json` is read (the last ReceiptKeep by file time, as
// reconcile_interrupted_runs does) and latest.json only when there is none.

// ReceiptDirParts is the receipt folder below the ROOT Hermes home (see
// RootHome). Hermes writes the run files there (_receipt_dir) whatever profile
// it runs in; a profile's own folder gets latest.json and the pm files only.
var ReceiptDirParts = []string{"logs", "update_receipts"}

// ReceiptLatestFile is the copy of the newest receipt (of any kind, see above).
const ReceiptLatestFile = "latest.json"

// ReceiptRunGlob matches the run file of an update. The pm_* files and
// latest.json are not updates.
const ReceiptRunGlob = "update_*.json"

// ReceiptKeep is how many run files Hermes keeps (_RECEIPT_KEEP) and how many
// are looked at.
const ReceiptKeep = 20

// The two outcomes that mean an update did not finish. Every other outcome
// ("success", "failed", "partial", ...) is a run that reached its own end.
const (
	ReceiptRunning     = "running"
	ReceiptInterrupted = "interrupted"
)

// The outcomes of an update that ran to its end (update_receipt.py: running |
// success | partial | failed). A run Hermes refused ("refused") did nothing and
// is not one of them. Only success and partial count as finished work
// (ReceiptReader.NewestFinished): a failed update still owes its steps.
const (
	ReceiptSuccess = "success"
	ReceiptPartial = "partial"
	ReceiptFailed  = "failed"
)

// ReceiptProfilesDir is the folder of a root home that holds the profile
// homes (<root>/profiles/<name>).
const ReceiptProfilesDir = "profiles"

// UpdateReceipt is the part of a receipt Safe Update reads. Time stamps are
// RFC 3339 text; *_create_time are Unix seconds (a float) of the process that
// owns the run, written by Hermes' process_identity so a reused pid cannot
// pass for the owner. A field the receipt lacks stays zero / nil.
type UpdateReceipt struct {
	Outcome          string         `json:"outcome"`
	UpdateID         string         `json:"update_id"`
	StartedAt        string         `json:"started_at"`
	FinishedAt       string         `json:"finished_at"`
	PID              int            `json:"pid"`
	PIDCreateTime    *float64       `json:"pid_create_time"`
	WriterPID        int            `json:"writer_pid"`
	WriterCreateTime *float64       `json:"writer_create_time"`
	Stages           []ReceiptStage `json:"stages"`
}

// ReceiptStage is one entry of a receipt's "stages". A stage is recorded when
// it ENDS ("plan", "snapshot", "apply", "deps", "build", "restart"), so the
// last entry of a killed run names the last step that was completed.
type ReceiptStage struct {
	Name string `json:"name"`
	At   string `json:"at"`
}

// receiptStageNext is the update step a run was in when it stopped, by the
// last stage its receipt recorded: the step after it, in Hermes' order (plan,
// snapshot, apply, deps, build, restart). The values are step keys of the
// update steps (timings.UpdateSteps), so the words are the same as when the
// step is read from Safe Update's own log. "build" is the group of the TUI,
// web and desktop builds (a receipt does not say which) and "tail" what
// follows the restart. "deps" -> the builds is the real case (a crash while
// packaging the desktop app).
var receiptStageNext = map[string]string{
	"":         "start",
	"plan":     "pull",
	"snapshot": "pull",
	"apply":    "pydeps",
	"deps":     "build",
	"build":    "restart",
	"restart":  "tail",
}

// ReceiptStopStep names the update step the run was in when it stopped, from
// the name of the last stage its receipt recorded ("" = none recorded yet).
// An unknown name gives "" (nothing is guessed).
func ReceiptStopStep(lastRecorded string) string { return receiptStageNext[lastRecorded] }

// ---- extras (v0.7.0): catalog plugins, hub skills and the bundled tools ----
//
// A reworded line must show up as "could not read", never as "all fine".
// Fixtures: testdata/contracts/extras-*.
//
//	hermes plugins check-updates --json   JSON list (structured, no scraping)
//	hermes skills check                   box table + a count footer
//	hermes pm doctor                      "v name ver" / "x name: reason" lines
//	hermes pm update --check              "name old -> new" lines (listing only)

// PluginArgvCheck, PluginArgvUpdate and the other argv tails are what the
// stage runs after the launcher. None of them passes --force or --yes: the
// security scan of Hermes asks (and, with nobody to ask, refuses).
var (
	PluginsCheckArgv   = []string{"plugins", "check-updates", "--json"}
	SkillsCheckArgv    = []string{"skills", "check"}
	SkillsUpdateArgv   = []string{"skills", "update"}
	PMDoctorArgv       = []string{"pm", "doctor"}
	PMUpdateCheckArgv  = []string{"pm", "update", "--check"}
	pluginUpdateHead   = []string{"plugins", "update"}
	pluginEnableHead   = []string{"plugins", "enable"}
	pluginsUpdatable   = map[string]bool{"catalog": true, "git": true}
	pluginNameSafeRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	evictionNoticeRe   = regexp.MustCompile(`Disabled plugin '([^']+)' in `)
	skillCountFooterRe = regexp.MustCompile(`(\d+) update\(s\) available across (\d+) checked skill\(s\)`)
	doctorLineRe       = regexp.MustCompile(`^(✓|✗|\?|-) (\S+?)(?::| )\s*(.*)$`)
	pmUpdateLineRe     = regexp.MustCompile(`^(\S+)\s+(\S+) → (\S+)\s*$`)
)

// NoHubSkillsText: `hermes skills check` with nothing to check.
const NoHubSkillsText = "No hub-installed skills to check"

// Words in `hermes plugins update <name>` output that say nothing changed
// because the plugin was current already, and the ones of `hermes skills
// update` for "not updated" and "kept your edits".
var (
	PluginCurrentTexts = []string{"already up to date", "is already at catalog pin"}
	SkillsNotUpdated   = "Not updated:"
	SkillsKeptEdits    = "kept your local edits"
)

// Words in `hermes plugins update <name>` output that mean it did not apply.
var pluginUpdateRefusals = []string{"Blocked:", "Security scan flagged", "not applied", "Error:"}

// PluginUpdateArgv is the argv tail that updates one plugin.
func PluginUpdateArgv(name string) []string {
	return append(append([]string{}, pluginUpdateHead...), name)
}

// PluginEnableArgv is the argv tail that re-admits one plugin.
func PluginEnableArgv(name string) []string {
	return append(append([]string{}, pluginEnableHead...), name)
}

// SafePluginName: a plugin name that may be put on a command line and in a
// path. Anything else is never passed on.
func SafePluginName(name string) bool { return pluginNameSafeRe.MatchString(name) }

// PluginRow is one row of `hermes plugins check-updates --json`.
type PluginRow struct {
	Name        string  `json:"name"`
	Class       string  `json:"class"`
	Current     *string `json:"current"`
	Latest      *string `json:"latest"`
	Update      *bool   `json:"update_available"`
	NeedsFixing *string `json:"needs_fixing"`
	Reason      string  `json:"reason"`
}

// Updatable: Hermes has an update path for this row (catalog re-pin or git
// pull) and an update is waiting.
func (p PluginRow) Updatable() bool {
	return p.Update != nil && *p.Update && p.NeedsFixing == nil && pluginsUpdatable[p.Class] && SafePluginName(p.Name)
}

// Short is the commit/version text of a pin, 8 characters.
func Short(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return (*s)[:min(8, len(*s))]
}

// ParsePluginRows reads the JSON of `plugins check-updates --json`. The
// launcher may print notices before the JSON; the list starts at the first
// line that is "[".
func ParsePluginRows(out string) ([]PluginRow, error) {
	i := strings.Index(out, "\n[")
	switch {
	case strings.HasPrefix(out, "["):
		i = 0
	case i >= 0:
		i++
	default:
		return nil, fmt.Errorf("no JSON list in the output")
	}
	var rows []PluginRow
	dec := json.NewDecoder(strings.NewReader(out[i:]))
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("unreadable JSON: %w", err)
	}
	for _, r := range rows {
		if r.Name == "" {
			return nil, fmt.Errorf("a row without a name")
		}
	}
	return rows, nil
}

// SkillRow is one row of the `hermes skills check` table.
type SkillRow struct{ Name, Source, Status string }

// SkillCheck is the parsed `hermes skills check`.
type SkillCheck struct {
	Rows    []SkillRow
	Updates int  // update_available rows
	Empty   bool // "No hub-installed skills to check."
}

// ParseSkillCheck reads the table. ok=false when neither the footer nor the
// "no skills" line is there (reworded output): the caller says "could not
// read", it does not call that "up to date".
func ParseSkillCheck(out string) (SkillCheck, bool) {
	var sc SkillCheck
	if strings.Contains(out, NoHubSkillsText) {
		sc.Empty = true
		return sc, true
	}
	footer := skillCountFooterRe.FindStringSubmatch(out)
	if footer == nil {
		return sc, false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "│") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "│"), "│")
		if len(cells) != 3 {
			continue
		}
		r := SkillRow{strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1]), strings.TrimSpace(cells[2])}
		if r.Name == "Name" && r.Status == "Status" || r.Name == "" {
			continue
		}
		sc.Rows = append(sc.Rows, r)
		if r.Status == "update_available" {
			sc.Updates++
		}
	}
	var n int
	_, _ = fmt.Sscan(footer[1], &n)
	if n > sc.Updates { // a wrapped name or status hid rows from the table scan
		sc.Updates = n
	}
	return sc, true
}

// SkillUpdateNames are the skills with an update waiting.
func (s SkillCheck) UpdateNames() []string {
	var out []string
	for _, r := range s.Rows {
		if r.Status == "update_available" {
			out = append(out, r.Name)
		}
	}
	return out
}

// DoctorFault is one failing package of `hermes pm doctor`.
type DoctorFault struct{ Name, Reason string }

// DoctorReport is the parsed `hermes pm doctor`.
type DoctorReport struct {
	OK   int
	Soft int // "? name: not installed" optional packages
	NA   int // "- name: n/a on ..."
	// Installed are the names with a check-mark line: a tool is installed here.
	Installed []string
	Faults    []DoctorFault
	Unknown   int // lines that matched none of the four shapes
}

// ParseDoctor reads the per-package lines. ok=false when no line was
// understood at all.
func ParseDoctor(out string) (DoctorReport, bool) {
	var d DoctorReport
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "" {
			continue
		}
		m := doctorLineRe.FindStringSubmatch(line)
		if m == nil {
			d.Unknown++
			continue
		}
		seen++
		switch m[1] {
		case "✓":
			d.OK++
			d.Installed = append(d.Installed, m[2])
		case "?":
			d.Soft++
		case "-":
			d.NA++
		case "✗":
			d.Faults = append(d.Faults, DoctorFault{Name: m[2], Reason: m[3]})
		}
	}
	return d, seen > 0
}

// DigestFault is the doctor line for a tool whose files differ from the
// digest Hermes recorded (stray files written into the tool folder are the
// usual cause on this PC: git-bash's "/" is the git tool folder).
func (f DoctorFault) DigestFault() bool {
	return strings.Contains(f.Reason, "do not match recorded digest")
}

// Advice is what to do about a fault, only where Hermes has a command that
// fixes that class of fault (pm/cli.py): "not installed", "outdated" and
// "legacy fact" are fixed by `hermes pm install`. A digest mismatch is not a
// missing file Hermes can restore without a reinstall of the tool, and the
// cause is usually a stray file: no command is promised.
func (f DoctorFault) Advice() string {
	r := f.Reason
	switch {
	case f.DigestFault():
		return "files in the tool's folder differ from what Hermes installed (often stray files another tool wrote into it); look at the folder, remove what is not Hermes', or reinstall the tool with `hermes pm install " + f.Name + "`"
	case r == "not installed" || r == "outdated" || strings.HasPrefix(r, "legacy fact"):
		return "`hermes pm install " + f.Name + "` installs it"
	case strings.HasPrefix(r, "installed but failed verification"):
		return "`hermes pm install " + f.Name + "` reinstalls it"
	}
	return "see `hermes pm doctor`"
}

// ToolUpdate is one "name old -> new" line of `hermes pm update --check`.
type ToolUpdate struct{ Name, From, To string }

// Present: the tool is installed here (a check-mark line) or installed and
// failing (a cross line other than "not installed"): a newer version of it
// matters.
func (d DoctorReport) Present(name string) bool {
	for _, n := range d.Installed {
		if n == name {
			return true
		}
	}
	for _, f := range d.Faults {
		if f.Name == name && f.Reason != "not installed" {
			return true
		}
	}
	return false
}

// ParsePMUpdateCheck lists the tools that have a newer version. Lines for
// tools without a source, up to date, failed to resolve or only newer for
// other platforms are not updates for this PC. Never an action: the command
// that applies them rewrites Hermes' core files.
func ParsePMUpdateCheck(out string) []ToolUpdate {
	var ups []ToolUpdate
	for _, line := range strings.Split(out, "\n") {
		if m := pmUpdateLineRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			ups = append(ups, ToolUpdate{m[1], m[2], m[3]})
		}
	}
	return ups
}

// EvictionLines are Hermes' own "Disabled plugin '<n>' in <home>: <why>" lines
// of a command's output, trimmed, for the log and the card.
func EvictionLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if evictionNoticeRe.MatchString(l) {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	return lines
}

// ConfigSetProviderArgv is the argv tail that sets memory.provider through
// Hermes (it writes its own config; Safe Update never edits config.yaml).
func ConfigSetProviderArgv(name string) []string {
	return []string{"config", "set", "memory.provider", name}
}

// PluginUpdateRefused: the output of `plugins update <name>` says the update
// did not apply (scan block, consent needed, error).
func PluginUpdateRefused(out string) bool {
	for _, w := range pluginUpdateRefusals {
		if strings.Contains(out, w) {
			return true
		}
	}
	return false
}

// ---- Session database size and compaction (Safe Update v0.9.0) ----
//
// Hermes' pre-update snapshot (hermes_cli/update_cmd_maint.py,
// _PRE_UPDATE_SNAPSHOT_MAX_FILE_SIZE) leaves out any state.db over 1 GiB and
// prints, per profile:
//
//	! Snapshot: skipping state.db (1.2 GB exceeds 1.0 GB limit)
//	! Skipping snapshot prune: DB file(s) skipped for size: state.db
//
// `hermes sessions optimize-storage` (hermes_cli/sessions_cmd.py) rebuilds the
// full-text index in the compact layout and runs VACUUM. It refuses while any
// process has the database open: "Refusing `hermes sessions optimize-storage`:
// another process is using <path>." followed by the holders.

// SnapshotMaxFileSize is Hermes' limit for one file in its pre-update snapshot.
const SnapshotMaxFileSize int64 = 1 << 30

// CompactMinVersion is the oldest Hermes Safe Update lets compact: 0.23.0.
// The subcommand exists earlier (v0.21.6 on), but the search-index code was
// largely rewritten for 0.23, and an older install is not offered it.
const (
	CompactMinMajor = 0
	CompactMinMinor = 23
)

var versionCoreRe = regexp.MustCompile(`^[vV]?(\d+)\.(\d+)(?:\.(\d+))?`)

// CompactCapable reports whether a Hermes of this version (`hermes
// --version`, e.g. "v0.23.1", "v0.23.0+5.gabc1234") may be offered the
// compaction. An empty or unreadable version is not capable. A version
// derived from an older release ("v0.22.7+300.g...") counts as that release.
func CompactCapable(version string) bool {
	m := versionCoreRe.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > CompactMinMajor || (major == CompactMinMajor && minor >= CompactMinMinor)
}

// snapshotSkipRe reads "Snapshot: skipping <name> (<size> exceeds <limit> limit)".
var snapshotSkipRe = regexp.MustCompile(`(?mi)Snapshot: skipping (\S*state\.db) \(([^()\r\n]*?) exceeds ([^()\r\n]*?) limit\)`)

// pruneSkipRe reads the companion line.
var pruneSkipRe = regexp.MustCompile(`(?mi)Skipping snapshot prune: DB file\(s\) skipped for size:[ \t]*([^\r\n]*)`)

// SnapshotSkip is one file Hermes said it left out of its snapshot.
type SnapshotSkip struct {
	File  string // "state.db"
	Size  string // as printed, "1.2 GB"
	Limit string // as printed, "1.0 GB"
}

// ParseSnapshotSkips reads the skip lines of `hermes update` output, in
// order (one per profile that has an oversized database, the profile is not
// named). The second line (prune skipped) is returned as pruneSkipped.
func ParseSnapshotSkips(out string) (skips []SnapshotSkip, pruneSkipped bool) {
	for _, m := range snapshotSkipRe.FindAllStringSubmatch(out, -1) {
		skips = append(skips, SnapshotSkip{File: m[1], Size: strings.TrimSpace(m[2]), Limit: strings.TrimSpace(m[3])})
	}
	return skips, pruneSkipRe.MatchString(out)
}

// OptimizeStorageArgv is the argv (after the launcher) of the compaction for
// a profile; "" or "default" is the Hermes home's own database. --yes only
// answers its own "Proceed?"; --force is never passed (it would go past a
// holder).
func OptimizeStorageArgv(profile string) []string {
	args := []string{"sessions", "optimize-storage", "--yes"}
	if profile != "" && profile != "default" {
		args = append([]string{"-p", profile}, args...)
	}
	return args
}

// OptimizeStorageHelpArgv asks whether this Hermes has the subcommand.
func OptimizeStorageHelpArgv() []string {
	return []string{"sessions", "optimize-storage", "--help"}
}

// HasOptimizeStorage reads the `--help` answer: the subcommand exists when
// the command exited 0 and the text names it.
func HasOptimizeStorage(code int, out string) bool {
	return code == 0 && strings.Contains(out, "optimize-storage")
}

var optimizeRefusedRe = regexp.MustCompile("(?m)^[ \\t]*(?:\\S{1,4}[ \\t]+)?Refusing `hermes sessions optimize-storage`")

// OptimizeRefusal returns Hermes' refusal text (the "Refusing ..." line and
// what follows it: the holder list) from the output, or "".
func OptimizeRefusal(out string) string {
	loc := optimizeRefusedRe.FindStringIndex(out)
	if loc == nil {
		return ""
	}
	return strings.TrimSpace(out[loc[0]:])
}

// OptimizeVacuumSkipped: the index was rebuilt but Hermes did not VACUUM, so
// the freed space was not returned. Hermes prints one of two lines and still
// exits 0 (hermes_cli/sessions_cmd.py, end of _cmd_optimize_storage):
//
//	(VACUUM skipped: another process is using the database. Re-run ...)
//	(VACUUM was skipped or failed — run `hermes sessions optimize` later ...)
//
// OptimizeVacuumSkipped returns that line ("" = the VACUUM ran, or the text
// is not Hermes').
func OptimizeVacuumSkipped(out string) string {
	return strings.TrimSpace(optimizeVacuumSkippedRe.FindString(out))
}

var optimizeVacuumSkippedRe = regexp.MustCompile(`(?m)^[ \t]*\(VACUUM (?:skipped: [^\r\n]*|was skipped or failed[^\r\n]*)`)

// profileIDRe is Hermes' PROFILE_ID_RE (hermes_constants.py; used by
// hermes_cli/profiles.py to accept a profile folder): a profile folder with
// any other name is not a profile.
var profileIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidProfileID reports whether a folder name under profiles/ is a Hermes
// profile id.
func ValidProfileID(name string) bool { return profileIDRe.MatchString(name) }

// OptimizeAlreadyCompact: Hermes had nothing to do.
func OptimizeAlreadyCompact(out string) bool {
	return strings.Contains(out, "already on the compact layout")
}

// OptimizeNoDisk: Hermes' own disk preflight said no.
func OptimizeNoDisk(out string) bool {
	return strings.Contains(out, "Not enough free disk")
}

// ---- Does compaction have work to do? (Safe Update v0.9.1) ----
//
// Hermes offers `hermes sessions optimize-storage` only when
// SessionDB.fts_optimize_available() is true (v0.23.1,
// hermes_state_search.py:592-604; in v0.23.3 the same terms live in
// persistence/sqlite/admin/_maintenance.py:388-402, see probeVerifiedRe).
// Safe Update mirrors that check, read-only, so it asks the compaction
// question only for a database where Hermes would do something. The pieces,
// with their v0.23.1 locations (unchanged in v0.23.2):
//
//   - FTS enabled: messages_fts exists (hermes_state.py:743, a SELECT probe)
//     and no fts_stale marker (hermes_state_schema.py:963);
//   - _db_needs_fts_storage_upgrade = fts_layout_drift (hermes_state_fts.py:93-103,
//     267-270): a stored FTS definition from an older release;
//   - state_meta fts_rebuild_high_water present (search.py:601, _meta_row :176):
//     an optimize that was interrupted;
//   - _has_fts_trash (hermes_state_fts.py:426-434): a table named
//     fts_v22_trash_% awaits its teardown;
//   - _fts_external_index_empty_with_messages (search.py:521-529): messages has
//     rows and messages_fts_docsize has none.
//
// v0.23.0 differs by one more term: a CJK backfill (fts_cjk_* markers) on a
// host that loads the CJK tokenizer. That cannot be known from outside, so
// those markers make the answer "unknown".
const (
	FTSBaseTable        = "messages_fts"               // hermes_state_fts.py:15
	FTSTrigramTable     = "messages_fts_trigram"       // hermes_state_fts.py:15
	FTSDocsizeTable     = "messages_fts_docsize"       // hermes_state_search.py:528
	FTSTrashPrefix      = "fts_v22_trash_"             // hermes_state_fts.py:426
	StateMetaTable      = "state_meta"                 // hermes_state_search.py:178
	MessagesTable       = "messages"                   // hermes_state_search.py:526
	FTSRebuildHighWater = "fts_rebuild_high_water"     // hermes_state_search.py:601
	FTSStaleKey         = "fts_stale"                  // hermes_state_common.py:1147
	FTSCJKHighWater     = "fts_cjk_rebuild_high_water" // v0.23.0 hermes_state_search.py:672
	FTSCJKStaleKey      = "fts_cjk_stale"              // v0.23.0 hermes_state_common.py:1134
)

// probeVerifiedRe matches exactly the releases the mirrored check was read
// from: core 0.23.0, 0.23.1, 0.23.2 and 0.23.3, optionally with a +canary.*
// build suffix. 0.23.2 was added after a source diff against v0.23.1:
// hermes_state_fts.py and hermes_state_search.py (fts_optimize_available at
// :592-604) are byte-identical, and hermes_state_common.py changed only outside
// the FTS keys (a resumable_subagents table, a prompt_cache_clock column, a
// preview trim, path_is_under).
//
// 0.23.3 moved the session store into a persistence/ package and deleted
// hermes_state_fts.py / hermes_state_search.py, so it was re-read term by term
// (.work/v0.9.1/probe-0.23.3-contract.md). The live check is
// persistence/sqlite/admin/_maintenance.py:388-402
// (_FtsStorageOptimizer.fts_optimize_available; the definition at :698-700
// only delegates to it; hermes_cli/sessions_cmd.py:1140 asks it on a writable
// admin). Its terms are the same: _db_needs_fts_storage_upgrade ->
// fts_layout_drift (persistence/sqlite/_schema_ddl.py:975, registry :926,
// FTS_STORAGE_VERSION :90 is not part of the decision), fts_rebuild_high_water
// (_schema.py:55 _meta_row), _has_fts_trash (_schema.py:1703, prefix :1700),
// _fts_external_index_empty_with_messages (_schema.py:1794), FTS_STALE_KEY
// (_schema_ddl.py:822). The CJK backfill is retired there (_schema.py:1712-1760
// deletes the fts_cjk_* markers on open), so those markers stay "unknown" here
// only out of caution. A derived git build (`0.23.1+12.gabc1234`) is
// unreleased main code and does not match.
var probeVerifiedRe = regexp.MustCompile(`^[vV]?0\.23\.[0123](?:\+canary\.[0-9A-Za-z.]+)?$`)

// ProbeVerified reports whether a Hermes of this version (`hermes --version`)
// is one the mirrored check was read from. A "nothing to do" answer is trusted
// only for those; any other version (0.23.3+, newer lines, derived builds) may
// have changed the rules, so it is "unknown".
func ProbeVerified(version string) bool {
	return probeVerifiedRe.MatchString(strings.TrimSpace(version))
}

// FTSVtableLayouts is every stored definition a released Hermes has created for
// the two index tables, oldest first; the last entry is the compact layout
// v0.23.1 creates (hermes_state_fts.py:44-61, FTS_VTABLE_LAYOUTS). The inline
// pre-v23 form (`fts5(content)`) is the second entry of each list; the
// trigram form that still carries tool_calls is the third.
var FTSVtableLayouts = map[string][]string{
	FTSBaseTable: {
		"CREATE VIRTUAL TABLE messages_fts USING fts5(content,content=messages,content_rowid=id)",
		"CREATE VIRTUAL TABLE messages_fts USING fts5(content)",
		"CREATE VIRTUAL TABLE messages_fts USING fts5(content,tool_name,tool_calls,content='messages'," +
			"content_rowid='id')",
		"CREATE VIRTUAL TABLE messages_fts USING fts5(content,tool_name,tool_calls," +
			"content='messages_fts_src',content_rowid='id')",
	},
	FTSTrigramTable: {
		"CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,content=messages,content_rowid=id," +
			"tokenize='trigram')",
		"CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tokenize='trigram')",
		"CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tool_name,tool_calls," +
			"content='messages_fts_trigram_src',content_rowid='id',tokenize='trigram')",
		"CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content,tool_name," +
			"content='messages_fts_trigram_src',content_rowid='id',tokenize='trigram')",
	},
}

var (
	storedFormPunctRe = regexp.MustCompile(`\s*([(),])\s*`)
	storedFormNameRe  = regexp.MustCompile(`^CREATE (VIRTUAL TABLE|VIEW) "([^"]+)"`)
)

// FTSStoredForm is sqlite_master.sql as Hermes compares it (_stored_form,
// hermes_state_fts.py:19-24): whitespace collapsed, none around ( ) and commas,
// the object name unquoted.
func FTSStoredForm(sql string) string {
	collapsed := storedFormPunctRe.ReplaceAllString(strings.Join(strings.Fields(sql), " "), "${1}")
	return storedFormNameRe.ReplaceAllString(collapsed, "CREATE ${1} ${2}")
}

// ftsOlderLayout mirrors _is_older_layout (hermes_state_fts.py:71-79).
func ftsOlderLayout(table, stored string) bool {
	layouts := FTSVtableLayouts[table]
	for _, l := range layouts[:len(layouts)-1] {
		if stored == l {
			return true
		}
	}
	if table == FTSBaseTable {
		return !strings.Contains(stored, "tool_name")
	}
	return strings.Contains(strings.ToLower(stored), "tool_calls")
}

// FTSLayoutDrift mirrors fts_layout_drift (hermes_state_fts.py:93-103): the
// index tables whose stored definition (sqlite_master.sql, raw) is from an
// older release and that only optimize-storage replaces. A base index that
// reads the wrong source is realigned by Hermes on its next open and is not
// work. unrecognised lists tables whose definition is neither in the registry
// nor shaped like an old one (a newer release's, or something else): Hermes
// leaves those alone, Safe Update cannot vouch for them.
func FTSLayoutDrift(stored map[string]string) (drift, unrecognised []string) {
	for _, table := range []string{FTSBaseTable, FTSTrigramTable} {
		raw, ok := stored[table]
		if !ok {
			continue
		}
		form := FTSStoredForm(raw)
		if ftsOlderLayout(table, form) {
			realigned := table == FTSBaseTable && strings.Contains(form, "tool_name") && !strings.Contains(form, "messages_fts_src")
			if !realigned {
				drift = append(drift, table)
			}
			continue
		}
		known := false
		for _, l := range FTSVtableLayouts[table] {
			if form == l {
				known = true
			}
		}
		if !known {
			unrecognised = append(unrecognised, table)
		}
	}
	return drift, unrecognised
}
