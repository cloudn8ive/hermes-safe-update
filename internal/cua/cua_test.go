package cua

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

const (
	oldExe = `C:\hermes\tools\cua-driver-0.20.0-win32-x64\cua-driver.exe`
	newExe = `C:\hermes\tools\cua-driver-0.21.0-win32-x64\cua-driver.exe`
)

type rig struct {
	t      *testing.T
	runner *execx.Fake
	auto   *platform.FakeAutostart
	logs   []string
	waits  []string
	exists map[string]bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, runner: &execx.Fake{}, auto: &platform.FakeAutostart{Tasks: map[string]string{}}, exists: map[string]bool{}}
	// Defaults: the install succeeds and status names the new binary.
	r.runner.On([]string{"hermes-under-test", "computer-use", "install", "--upgrade"}, execx.Result{Output: "installed\n"})
	r.runner.On([]string{"hermes-under-test", "computer-use", "status"}, execx.Result{Output: "cua-driver installed at " + newExe + "\n"})
	return r
}

func (r *rig) svc() *Service {
	return New(Deps{
		Runner:    r.runner,
		Launcher:  "hermes-under-test",
		Autostart: r.auto,
		Log:       func(s string) { r.logs = append(r.logs, s) },
		OnWait:    func(s string) { r.waits = append(r.waits, s) },
		Exists:    func(p string) bool { return r.exists[p] },
	})
}

func (r *rig) logged(sub string) bool {
	for _, l := range r.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

var deferred = "→ cua-driver refresh deferred: run hermes computer-use install --upgrade\n"

func TestRefreshDecisionTable(t *testing.T) {
	ctx := context.Background()
	type want struct {
		ran, ok   bool
		line      string
		elevated  bool
		logSubstr string
	}
	cases := []struct {
		name  string
		out   string
		task  *string // nil = no task
		opts  Options
		setup func(r *rig)
		want  want
	}{
		{"no deferral: nothing runs, nothing logged", "Update complete!\n", nil, Options{}, nil,
			want{ran: false, ok: true}},
		{"no logon task: on-demand", deferred, nil, Options{}, nil,
			want{ran: true, ok: true, line: "0.21.0, on-demand (no logon task)", logSubstr: "no logon task (on-demand mode)"}},
		{"task already current (case and slash differences ignored)", deferred, ptr(strings.ToLower(strings.ReplaceAll(newExe, `\`, `/`))), Options{}, nil,
			want{ran: true, ok: true, line: "0.21.0, logon task current", logSubstr: "already targets the current binary"}},
		{"stale + unattended: never elevates", deferred, ptr(oldExe), Options{Unattended: true}, nil,
			want{ran: true, ok: false, line: "0.21.0, logon task needs an admin command (see above)", logSubstr: "skipped (unattended)"}},
		{"stale + --no-elevate: never elevates", deferred, ptr(oldExe), Options{NoElevate: true}, nil,
			want{ran: true, ok: false, line: "0.21.0, logon task needs an admin command (see above)", logSubstr: "skipped (--no-elevate)"}},
		{"stale + elevation succeeds and task now current", deferred, ptr(oldExe), Options{},
			func(r *rig) { r.auto.OnElevate = func([]string) { r.auto.Tasks[hermes.CuaTaskName] = newExe } },
			want{ran: true, ok: true, line: "0.21.0, logon task re-registered", elevated: true, logSubstr: "re-registered for"}},
		{"stale + elevation fails (user declined UAC)", deferred, ptr(oldExe), Options{},
			func(r *rig) { r.auto.ElevateCode = 1223; r.auto.ElevateOut = "The operation was canceled by the user" },
			want{ran: true, ok: false, line: "0.21.0, logon task NOT updated (see warning)", elevated: true, logSubstr: "re-registration not completed (rc=1223"}},
		{"stale + exit 0 but task still old", deferred, ptr(oldExe), Options{}, nil,
			want{ran: true, ok: false, line: "0.21.0, logon task NOT updated (see warning)", elevated: true, logSubstr: "not completed"}},
		{"install --upgrade fails", deferred, ptr(oldExe), Options{},
			func(r *rig) {
				r.runner = &execx.Fake{}
				r.runner.On([]string{"hermes-under-test", "computer-use", "install", "--upgrade"}, execx.Result{Code: 1, Output: "boom\n"})
			},
			want{ran: true, ok: false, logSubstr: "WARN cua-driver refresh failed; run `hermes computer-use doctor`"}},
		{"status gives no binary: task left unchanged", deferred, ptr(oldExe), Options{},
			func(r *rig) {
				r.runner = &execx.Fake{}
				r.runner.On([]string{"hermes-under-test", "computer-use", "install", "--upgrade"}, execx.Result{})
				r.runner.On([]string{"hermes-under-test", "computer-use", "status"}, execx.Result{Output: "not installed\n"})
			},
			want{ran: true, ok: false, line: "logon task left unchanged (see warning)", logSubstr: "could not resolve the current cua-driver binary"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.task != nil {
				r.auto.Tasks[hermes.CuaTaskName] = *c.task
			}
			if c.setup != nil {
				c.setup(r)
			}
			c.opts.UpdateOutput = c.out
			got := r.svc().Refresh(ctx, c.opts)
			if got.Ran != c.want.ran || got.OK != c.want.ok || got.Line != c.want.line {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if (len(r.auto.Elevated) > 0) != c.want.elevated {
				t.Fatalf("elevated = %v, want %v", r.auto.Elevated, c.want.elevated)
			}
			if c.want.logSubstr != "" && !r.logged(c.want.logSubstr) {
				t.Fatalf("log lacks %q: %q", c.want.logSubstr, r.logs)
			}
			if !c.want.ran && (len(r.runner.Calls()) != 0 || len(r.logs) != 0) {
				t.Fatalf("not deferred but ran %v / logged %v", r.runner.Calls(), r.logs)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestRefreshUpgradeUsesLongTimeout(t *testing.T) {
	r := newRig(t)
	r.svc().Refresh(context.Background(), Options{UpdateOutput: deferred})
	for _, c := range r.runner.Calls() {
		if strings.Join(c.Argv, " ") == "hermes-under-test computer-use install --upgrade" && c.Timeout.Seconds() != 900 {
			t.Fatalf("install timeout = %v", c.Timeout)
		}
		if strings.HasSuffix(strings.Join(c.Argv, " "), "computer-use status") && c.Timeout.Seconds() != 180 {
			t.Fatalf("status timeout = %v", c.Timeout)
		}
		if c.ShowWindow {
			t.Fatal("helper must not show a window")
		}
	}
}

func TestRefreshNotApplicableElsewhere(t *testing.T) {
	r := newRig(t)
	r.auto.Tasks = nil // FakeAutostart.Supported() == false
	got := r.svc().Refresh(context.Background(), Options{UpdateOutput: deferred})
	if got.Ran || !got.OK || got.Line != "not applicable on this OS" {
		t.Fatalf("got %+v", got)
	}
	if len(r.runner.Calls()) != 0 {
		t.Fatalf("ran %v", r.runner.Calls())
	}
	st := r.svc().Status(context.Background())
	if st.Text != "not applicable on this OS" || st.Task != TaskNone {
		t.Fatalf("status = %+v", st)
	}
}

func TestElevatedCommandQuotingAndShape(t *testing.T) {
	ctx := context.Background()
	// Paths with spaces and single quotes must stay inside one PowerShell
	// string each (D16); the command travels as -EncodedCommand.
	task := `D:\O'Brien tools\it's old\cua-driver.exe`
	newer := `D:\O'Brien tools\cua driver\cua-driver.exe`
	r := newRig(t)
	r.runner = &execx.Fake{}
	r.runner.On([]string{"hermes-under-test", "computer-use", "install", "--upgrade"}, execx.Result{})
	r.runner.On([]string{"hermes-under-test", "computer-use", "status"}, execx.Result{Output: "cua-driver installed at " + newer + "\n"})
	r.auto.Tasks[hermes.CuaTaskName] = task
	r.exists[task] = true
	r.svc().Refresh(ctx, Options{UpdateOutput: deferred})
	if len(r.auto.Elevated) != 1 {
		t.Fatalf("elevated %d times", len(r.auto.Elevated))
	}
	argv := r.auto.Elevated[0]
	if len(argv) != 5 || argv[0] != "powershell.exe" || argv[1] != "-NoProfile" || argv[2] != "-NonInteractive" || argv[3] != "-EncodedCommand" {
		t.Fatalf("argv = %q", argv)
	}
	inner := decodePS(t, argv[4])
	want := `try { & 'D:\O''Brien tools\it''s old\cua-driver.exe' stop *> $null } catch {}; ` +
		`& 'D:\O''Brien tools\cua driver\cua-driver.exe' autostart enable; ` +
		`if ($LASTEXITCODE) { exit $LASTEXITCODE }; ` +
		`schtasks.exe /Run /TN cua-driver-serve | Out-Null; exit 0`
	if inner != want {
		t.Fatalf("inner command:\n got %s\nwant %s", inner, want)
	}
	if len(r.waits) != 1 || r.waits[0] != "Waiting for the Windows UAC prompt" {
		t.Fatalf("waits = %v", r.waits)
	}
	if !r.logged("Windows will ask for administrator approval (UAC)") {
		t.Fatalf("logs = %q", r.logs)
	}
}

func TestElevatedCommandSkipsStopWhenOldBinaryIsGone(t *testing.T) {
	r := newRig(t)
	r.auto.Tasks[hermes.CuaTaskName] = oldExe // r.exists says it is not on disk
	r.svc().Refresh(context.Background(), Options{UpdateOutput: deferred})
	inner := decodePS(t, r.auto.Elevated[0][4])
	if !strings.HasPrefix(inner, "$null = 0; & '"+newExe+"' autostart enable") {
		t.Fatalf("inner = %s", inner)
	}
}

func TestElevatedStopOnlyForOldBinaryUnderInstallRoot(t *testing.T) {
	home := `C:\Users\you\AppData\Local\hermes`
	cases := []struct {
		name     string
		task     string
		wantStop bool
	}{
		{"sibling version dir under the install root", oldExe, true},
		{"install root compared case-insensitively with slashes", strings.ToUpper(strings.ReplaceAll(oldExe, `\`, `/`)), true},
		{"under the Hermes home", home + `\tools\cua-driver.exe`, true},
		{"outside every root", `C:\Users\you\Downloads\cua-driver.exe`, false},
		{"prefix-sharing sibling is not under the root", `C:\hermes\tools-evil\cua-driver-0.1-win32-x64\cua-driver.exe`, false},
		{"dot-dot escape out of the root", `C:\hermes\tools\..\evil\cua-driver.exe`, false},
		{"Hermes-home prefix sibling", home + `-evil\cua-driver.exe`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.auto.Tasks[hermes.CuaTaskName] = c.task
			r.exists[c.task] = true
			s := New(Deps{Runner: r.runner, Launcher: "hermes-under-test", Autostart: r.auto, Home: home,
				Log: func(l string) { r.logs = append(r.logs, l) }, Exists: func(p string) bool { return r.exists[p] }})
			s.Refresh(context.Background(), Options{UpdateOutput: deferred})
			if len(r.auto.Elevated) != 1 {
				t.Fatalf("elevated %d times", len(r.auto.Elevated))
			}
			inner := decodePS(t, r.auto.Elevated[0][4])
			if has := strings.Contains(inner, " stop "); has != c.wantStop {
				t.Fatalf("stop line present = %v, want %v: %s", has, c.wantStop, inner)
			}
			if got := r.logged("old task binary outside the install root; skipped stop"); got == c.wantStop {
				t.Fatalf("skip log = %v, want %v: %q", got, !c.wantStop, r.logs)
			}
			if !c.wantStop && !strings.HasPrefix(inner, "$null = 0; & '"+newExe+"' autostart enable") {
				t.Fatalf("inner = %s", inner)
			}
		})
	}
}

func TestElevatedCommandRefusesHostilePathCharacters(t *testing.T) {
	// A newline or NUL in a path cannot be made safe inside a one-line
	// PowerShell string: refuse instead of guessing.
	for _, bad := range []string{"C:\\a\nb\\cua-driver.exe", "C:\\a\x00b\\cua-driver.exe", "C:\\a\rb\\cua-driver.exe"} {
		if _, err := elevatedArgv("", bad, false); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, curly := range []string{"C:\\a\u2018b\\cua-driver.exe", "C:\\a\u2019b\\cua-driver.exe"} {
		// PowerShell treats typographic quotes as quote characters too.
		argv, err := elevatedArgv("", curly, false)
		if err != nil {
			t.Fatal(err)
		}
		inner := decodePS(t, argv[4])
		if strings.Count(inner, "\u2018") != 2 && strings.Count(inner, "\u2019") != 2 {
			t.Errorf("typographic quote not doubled: %s", inner)
		}
	}
}

func decodePS(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("not UTF-16LE base64: %v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		task *string
		want TaskStatus
		text string
	}{
		{"none", nil, TaskNone, "none (on-demand)"},
		{"current", ptr(newExe), TaskCurrent, "current"},
		{"stale", ptr(oldExe), TaskStale, "stale (" + oldExe + " vs pin " + newExe + ")"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.task != nil {
				r.auto.Tasks[hermes.CuaTaskName] = *c.task
			}
			st := r.svc().Status(ctx)
			if st.Task != c.want || st.Text != c.text || st.Current != newExe {
				t.Fatalf("status = %+v", st)
			}
		})
	}
	t.Run("task exists but names no recognisable exe: stale, never current", func(t *testing.T) {
		r := newRig(t)
		r.auto.Tasks[hermes.CuaTaskName] = ""
		st := r.svc().Status(ctx)
		if st.Task != TaskStale || st.Text != "stale (? vs pin "+newExe+")" {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("pin unknown: unknown", func(t *testing.T) {
		r := newRig(t)
		r.runner = &execx.Fake{}
		r.runner.On([]string{"hermes-under-test", "computer-use", "status"}, execx.Result{Code: 1})
		r.auto.Tasks[hermes.CuaTaskName] = oldExe
		st := r.svc().Status(ctx)
		if st.Task != TaskUnknown {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("task lookup error: unknown", func(t *testing.T) {
		r := newRig(t)
		a := &errAuto{FakeAutostart: r.auto}
		s := New(Deps{Runner: r.runner, Launcher: "hermes-under-test", Autostart: a})
		if st := s.Status(ctx); st.Task != TaskUnknown {
			t.Fatalf("status = %+v", st)
		}
	})
}

type errAuto struct{ *platform.FakeAutostart }

func (errAuto) TaskExecutable(context.Context, string) (string, bool, error) {
	return "", false, os.ErrPermission
}

func TestVersionFromPath(t *testing.T) {
	cases := map[string]string{
		newExe:                    "0.21.0",
		`C:\x\cua-driver\bin.exe`: "refreshed",
		"":                        "refreshed",
	}
	for p, want := range cases {
		if got := versionOf(p); got != want {
			t.Errorf("versionOf(%q) = %q, want %q", p, got, want)
		}
	}
	_ = filepath.Join
}

// The real `computer-use status` shape: a "cua-driver: " prefix, CRLF line
// endings, a trailing "(version)" after the path, and a second line.
func TestStatusRealOutputShape(t *testing.T) {
	r := newRig(t)
	r.runner = &execx.Fake{}
	r.runner.On([]string{"hermes-under-test", "computer-use", "status"}, execx.Result{
		Output: "cua-driver: installed at " + newExe + " (0.21.0)\r\n  ok Runtime contract ready (Hermes PM pin).\r\n"})
	r.auto.Tasks[hermes.CuaTaskName] = newExe
	st := r.svc().Status(context.Background())
	if st.Task != TaskCurrent || st.Text != "current" || st.Current != newExe {
		t.Fatalf("status = %+v", st)
	}
}
