package cua

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

const notApplicable = "not applicable on this OS"

var versionRe = regexp.MustCompile(`cua-driver-([\d.]+)`)

// Deps of the refresher.
type Deps struct {
	Runner    execx.Runner
	Launcher  string // hermes CLI
	Autostart platform.Autostart
	// Log receives file-log lines (including "WARN ..." lines). May be nil.
	Log func(string)
	// OnWait is called with a label while a blocking UAC prompt is open
	// (the UI shows it as the wait label). May be nil.
	OnWait func(string)
	// Exists reports whether a file exists (default os.Stat).
	Exists func(string) bool
	// Home is HERMES_HOME; an old task binary under it is trusted like one
	// under the cua-driver install root. May be empty.
	Home string
}

// Service implements Refresher.
type Service struct{ d Deps }

var _ Refresher = (*Service)(nil)

// New returns the refresher.
func New(d Deps) *Service {
	if d.Exists == nil {
		d.Exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	return &Service{d: d}
}

func (s *Service) logf(format string, a ...any) {
	if s.d.Log != nil {
		s.d.Log(fmt.Sprintf(format, a...))
	}
}

func (s *Service) hermes(ctx context.Context, timeout time.Duration, args ...string) (execx.Result, error) {
	return s.d.Runner.Run(ctx, execx.Cmd{Argv: append([]string{s.d.Launcher}, args...), Timeout: timeout})
}

func (s *Service) supported() bool { return s.d.Autostart != nil && s.d.Autostart.Supported() }

// binary is the cua-driver path the current Hermes pin selects, from
// `hermes computer-use status` ("" = unknown).
func (s *Service) binary(ctx context.Context) string {
	res, err := s.hermes(ctx, 180*time.Second, "computer-use", "status")
	if err != nil || res.Code != 0 && res.Output == "" {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(res.Output, "\r\n", "\n"), "\n") {
		if m := hermes.CuaInstalledRe.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// task is the exe the logon task runs. known=false = could not tell.
func (s *Service) task(ctx context.Context) (exe string, found, known bool) {
	exe, found, err := s.d.Autostart.TaskExecutable(ctx, hermes.CuaTaskName)
	if err != nil {
		return "", false, false
	}
	return exe, found, true
}

// samePath compares two Windows paths: separators and case do not matter.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	norm := func(p string) string {
		return strings.ToLower(filepath.Clean(strings.ReplaceAll(p, "/", `\`)))
	}
	return norm(a) == norm(b)
}

// normWin lower-cases a Windows path, turns "/" into "\\" and resolves "." and
// ".." lexically (platform-neutral, so the check behaves the same in tests on
// every OS).
func normWin(p string) string {
	return strings.ToLower(strings.ReplaceAll(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/", `\`))
}

// underRoot reports whether p lies strictly below root at a path boundary
// (case-insensitive). A root that is empty or a bare volume root never matches.
func underRoot(p, root string) bool {
	if p == "" || root == "" {
		return false
	}
	r := strings.TrimRight(normWin(root), `\`)
	if r == "" || strings.HasSuffix(r, ":") {
		return false
	}
	return strings.HasPrefix(normWin(p), r+`\`)
}

// trustedStop reports whether the old task binary may be run elevated: it must
// lie under the cua-driver install root of newExe (the directory holding the
// versioned cua-driver-* folders) or under the Hermes home (SR-5).
func trustedStop(oldTask, newExe, home string) bool {
	root := path.Dir(path.Dir(strings.ReplaceAll(newExe, `\`, "/")))
	return underRoot(oldTask, root) || underRoot(oldTask, home)
}

func versionOf(p string) string {
	if m := versionRe.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return "refreshed"
}

// Status is the read-only view for `check`.
func (s *Service) Status(ctx context.Context) Status {
	if !s.supported() {
		return Status{Task: TaskNone, Text: notApplicable}
	}
	exe, found, known := s.task(ctx)
	cur := s.binary(ctx)
	st := Status{TaskExe: exe, Current: cur}
	switch {
	case !known:
		st.Task, st.Text = TaskUnknown, "unknown"
	case !found:
		st.Task, st.Text = TaskNone, "none (on-demand)"
	case cur == "":
		st.Task, st.Text = TaskUnknown, "unknown (could not read the current cua-driver)"
	case samePath(exe, cur):
		st.Task, st.Text = TaskCurrent, "current"
	default:
		shown := exe
		if shown == "" {
			shown = "?"
		}
		st.Task, st.Text = TaskStale, fmt.Sprintf("stale (%s vs pin %s)", shown, cur)
	}
	return st
}

// Refresh runs only when `hermes update` deferred the cua-driver refresh:
// `computer-use install --upgrade`, then, if a logon task points at another
// binary, one elevated re-registration (never with Unattended/NoElevate).
// Failures are WARN log lines and Ok=false; they never touch the verified
// result of the update.
func (s *Service) Refresh(ctx context.Context, o Options) Result {
	if !strings.Contains(o.UpdateOutput, hermes.CuaDeferred) {
		return Result{OK: true}
	}
	if !s.supported() {
		return Result{OK: true, Line: notApplicable}
	}
	s.logf("== cua-driver (Computer Use) refresh")
	res, err := s.hermes(ctx, 900*time.Second, "computer-use", "install", "--upgrade")
	if err != nil {
		return Result{Ran: true, Line: "interrupted"}
	}
	s.logf("computer-use install --upgrade rc=%d: %s", res.Code, execx.Tail(res.Output, 300))
	if res.Code != 0 {
		s.logf("WARN cua-driver refresh failed; run `hermes computer-use doctor` (the update itself is fine)")
		return Result{Ran: true}
	}
	cur := s.binary(ctx)
	task, found, _ := s.task(ctx)
	line := versionOf(cur)
	if !found {
		s.logf("cua-driver: no logon task (on-demand mode); nothing else to do")
		return Result{Ran: true, OK: true, Line: line + ", on-demand (no logon task)"}
	}
	if samePath(task, cur) {
		s.logf("cua-driver: logon task already targets the current binary (%s)", cur)
		return Result{Ran: true, OK: true, Line: line + ", logon task current"}
	}
	if cur == "" {
		s.logf("WARN could not resolve the current cua-driver binary; logon task left unchanged")
		return Result{Ran: true, Line: "logon task left unchanged (see warning)"}
	}
	manual := fmt.Sprintf("from an elevated PowerShell: & %s autostart enable", psQuote(cur))
	shown := task
	if shown == "" {
		shown = "?"
	}
	s.logf("cua-driver: logon task targets %s, current pin is %s", shown, cur)
	if o.Unattended || o.NoElevate {
		why := "--no-elevate"
		if o.Unattended {
			why = "unattended"
		}
		s.logf("cua-driver: re-registration needs admin rights; skipped (%s). Run %s", why, manual)
		return Result{Ran: true, Line: line + ", logon task needs an admin command (see above)"}
	}
	old := taskIfExists(task, s.d.Exists)
	if old != "" && !trustedStop(old, cur, s.d.Home) {
		s.logf("cua-driver: old task binary outside the install root; skipped stop (%s)", old)
		old = ""
	}
	argv, err := elevatedArgv(old, cur, true)
	if err != nil {
		s.logf("WARN cua-driver re-registration not attempted (%v). Run %s", err, manual)
		return Result{Ran: true, Line: line + ", logon task NOT updated (see warning)"}
	}
	s.logf("cua-driver: Windows will ask for administrator approval (UAC) to update the logon task")
	if s.d.OnWait != nil {
		s.d.OnWait("Waiting for the Windows UAC prompt")
	}
	code, out, rerr := s.d.Autostart.RunElevated(ctx, argv)
	if rerr != nil {
		code, out = -1, rerr.Error()
	}
	after, _, _ := s.task(ctx)
	if code == 0 && samePath(after, cur) {
		s.logf("cua-driver: logon task re-registered for %s; daemon restarted", cur)
		return Result{Ran: true, OK: true, Line: line + ", logon task re-registered"}
	}
	s.logf("WARN cua-driver re-registration not completed (rc=%d: %s). Run %s", code, tailRunes(strings.TrimSpace(out), 200), manual)
	return Result{Ran: true, Line: line + ", logon task NOT updated (see warning)"}
}

func taskIfExists(task string, exists func(string) bool) string {
	if task != "" && exists(task) {
		return task
	}
	return ""
}

func tailRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// psQuote makes s one PowerShell single-quoted string literal. PowerShell
// also treats the typographic single quotes as quote characters, so all of
// them are doubled (D16).
func psQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

var errBadPath = errors.New("a path contains a control character")

// elevatedArgv builds the argv that RunElevated receives (the platform adds
// the single UAC prompt and quotes argv). The PowerShell text travels as
// -EncodedCommand (base64 of UTF-16LE), so no path is ever re-parsed by a
// shell command line. oldTask is the task's current exe if it still exists
// (it is asked to stop first), else "".
func elevatedArgv(oldTask, newExe string, _ bool) ([]string, error) {
	for _, p := range []string{oldTask, newExe} {
		if strings.ContainsAny(p, "\r\n\x00") {
			return nil, errBadPath
		}
	}
	stop := "$null = 0"
	if oldTask != "" {
		stop = "try { & " + psQuote(oldTask) + " stop *> $null } catch {}"
	}
	script := strings.Join([]string{
		stop,
		"& " + psQuote(newExe) + " autostart enable",
		"if ($LASTEXITCODE) { exit $LASTEXITCODE }",
		"schtasks.exe /Run /TN " + hermes.CuaTaskName + " | Out-Null",
		"exit 0",
	}, "; ")
	u := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(u)*2)
	for _, c := range u {
		raw = append(raw, byte(c), byte(c>>8))
	}
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw)}, nil
}
