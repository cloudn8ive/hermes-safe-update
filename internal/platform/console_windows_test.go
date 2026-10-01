//go:build windows

package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestKeyFromEvent(t *testing.T) {
	cases := []struct {
		name  string
		ch    uint16
		vk    uint16
		ctrl  uint32
		down  bool
		want  Key
		count bool
	}{
		{"letter lower-cased", 'Y', 0x59, 0, true, Key{Rune: 'y'}, true},
		{"enter", '\r', 0x0D, 0, true, Key{Rune: '\r'}, true},
		{"esc", 0x1b, 0x1B, 0, true, Key{Rune: 0x1b}, true},
		{"ctrl+c read as a key", 0x03, 0x43, 0x0008, true, Key{CtrlC: true}, true},
		{"arrow up is Other", 0, 0x26, 0x0100, true, Key{Other: true}, true},
		{"F5 is Other", 0, 0x74, 0, true, Key{Other: true}, true},
		{"key up ignored", 'y', 0x59, 0, false, Key{}, false},
		{"shift alone ignored", 0, 0x10, 0x0010, true, Key{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, ok := keyFromEvent(c.down, c.vk, c.ch)
			if ok != c.count || (ok && k != c.want) {
				t.Errorf("got %+v ok=%v, want %+v ok=%v", k, ok, c.want, c.count)
			}
		})
	}
}

func TestConsoleOnPipeIsNotTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	c := &winConsole{out: windows.Handle(w.Fd()), in: windows.Handle(r.Fd())}
	if c.IsTerminal() {
		t.Error("a pipe is not a console")
	}
	if _, err := c.EnableVT(); err == nil {
		t.Error("EnableVT on a pipe must fail (plain output)")
	}
	if _, _, ok := c.Size(); ok {
		t.Error("Size on a pipe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.ReadKey(ctx); err == nil {
		t.Error("ReadKey without a console must return the context error")
	}
}

func TestIdentityStatusSkips(t *testing.T) {
	c := &winConsole{getenv: func(k string) string {
		if k == "WT_SESSION" {
			return "x"
		}
		return ""
	}}
	if s := c.SetIdentity("HermesAgent.SafeUpdate", nil); s != "skipped: windows terminal" {
		t.Errorf("status %q", s)
	}
	c = &winConsole{getenv: func(string) string { return "" }, hwnd: func() windows.HWND { return 0 }}
	if s := c.SetIdentity("HermesAgent.SafeUpdate", nil); s != "skipped: no console window" {
		t.Errorf("status %q", s)
	}
}

func TestOSCProgressSequences(t *testing.T) {
	var b strings.Builder
	p := &wtProgress{w: &b}
	p.Set(ProgressNormal, 0.426)
	p.Set(ProgressPaused, 0.5)
	p.Set(ProgressError, 1)
	p.Set(ProgressIndeterminate, 0)
	p.Close()
	want := "\x1b]9;4;1;42\a\x1b]9;4;4;50\a\x1b]9;4;2;100\a\x1b]9;4;3;0\a\x1b]9;4;0;0\a"
	if b.String() != want {
		t.Errorf("got %q\nwant %q", b.String(), want)
	}
}

func TestDiskFreeAndVolumes(t *testing.T) {
	d := winDisk{getenv: os.Getenv}
	free, err := d.Free(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("free=%d err=%v", free, err)
	}
	vols, err := d.Volumes()
	if err != nil || len(vols) == 0 {
		t.Fatalf("vols=%v err=%v", vols, err)
	}
	sys := 0
	for _, v := range vols {
		if v.System {
			sys++
			if !v.Fixed {
				t.Errorf("system volume not fixed: %+v", v)
			}
		}
	}
	if sys != 1 {
		t.Errorf("want exactly one system volume, got %d in %+v", sys, vols)
	}
	if _, err := d.Free(filepath.Join(t.TempDir(), "missing", "dir")); err != nil {
		t.Errorf("Free of a missing dir should use the nearest existing parent: %v", err)
	}
}

func TestTaskXMLExecutable(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-16"?><Task><Actions><Exec><Command>C:\Users\you\AppData\Local\hermes\tools\cua-driver-0.21.0-win32-x64\cua-driver.exe</Command><Arguments>serve</Arguments></Exec></Actions></Task>`
	if got := taskExe(xml); got != `C:\Users\you\AppData\Local\hermes\tools\cua-driver-0.21.0-win32-x64\cua-driver.exe` {
		t.Errorf("got %q", got)
	}
	if got := taskExe(`<Task><Command>"C:\x\cua-driver.exe"</Command></Task>`); got != `C:\x\cua-driver.exe` {
		t.Errorf("quoted command: %q", got)
	}
	if got := taskExe(`<Task><Command>"C:\x\other.exe"</Command></Task>`); got != "" {
		t.Errorf("task naming no cua-driver: %q", got)
	}
	if got := taskExe(`<Task></Task>`); got != "" {
		t.Errorf("no command: %q", got)
	}
	// schtasks /XML emits UTF-16LE
	u := windows.StringToUTF16(xml)
	raw := make([]byte, 0, len(u)*2)
	for _, c := range u[:len(u)-1] {
		raw = append(raw, byte(c), byte(c>>8))
	}
	if got := decodeSchtasks(append([]byte{0xFF, 0xFE}, raw...)); !strings.Contains(got, "cua-driver.exe") {
		t.Errorf("utf-16 decode: %q", got)
	}
}

func TestElevatedCommandLineQuoting(t *testing.T) {
	got := elevatedParams([]string{"-NoProfile", "-EncodedCommand", "AbC=="})
	if got != "-NoProfile -EncodedCommand AbC==" {
		t.Errorf("got %q", got)
	}
	got = elevatedParams([]string{`C:\Program Files\x y\a.exe`, `it's "q"`})
	if got != `"C:\Program Files\x y\a.exe" "it's \"q\""` {
		t.Errorf("got %q", got)
	}
}

func TestTaskbarProgressWithoutWindowIsSilentNoOp(t *testing.T) {
	p := newTaskbarProgress(0)
	p.Set(ProgressNormal, 0.5)
	p.Set(ProgressPaused, 0.5)
	p.Close()
	p.Close() // idempotent
}

func TestTaskbarFlagsMapping(t *testing.T) {
	want := map[ProgressState]uintptr{ProgressNone: 0, ProgressIndeterminate: 1, ProgressNormal: 2, ProgressError: 4, ProgressPaused: 8}
	for s, f := range want {
		if got := tbFlags(s); got != f {
			t.Errorf("state %d -> %d, want %d", s, got, f)
		}
	}
}

func TestNewWiresWindowsBackends(t *testing.T) {
	p := New()
	if p.OS != "windows" || !p.Tested {
		t.Errorf("%+v", p)
	}
	if _, ok := p.Procs.(winProcs); !ok {
		t.Errorf("procs %T", p.Procs)
	}
	if _, ok := p.App.(*winApp); !ok {
		t.Errorf("app %T", p.App)
	}
	if _, ok := p.Paths.(winPaths); !ok {
		t.Errorf("paths %T", p.Paths)
	}
	if _, ok := p.Disk.(winDisk); !ok {
		t.Errorf("disk %T", p.Disk)
	}
	if _, ok := p.Autostart.(winAutostart); !ok {
		t.Errorf("autostart %T", p.Autostart)
	}
	if h, err := p.Paths.HermesHome(Env{Getenv: os.Getenv}); err != nil || !strings.HasSuffix(strings.ToLower(h), `\hermes`) {
		t.Errorf("home %q %v", h, err)
	}
}
