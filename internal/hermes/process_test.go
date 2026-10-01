package hermes

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

func TestClassify(t *testing.T) {
	const home = `C:\Users\you\AppData\Local\hermes`
	markers := []string{"win-unpacked"}
	const checkout = home + `\hermes-agent`
	cases := []struct {
		name string
		p    platform.Process
		want string
	}{
		{"desktop main", platform.Process{Name: "Hermes.exe", Cmdline: `"` + home + `\hermes-agent\apps\desktop\release\win-unpacked\Hermes.exe"`}, "desktop"},
		{"desktop renderer child has --type", platform.Process{Name: "Hermes.exe", Cmdline: `"...\win-unpacked\Hermes.exe" --type=renderer`}, ""},
		{"desktop name match is case-insensitive", platform.Process{Name: "HERMES.EXE", Cmdline: strings.ToUpper(checkout) + `\APPS\DESKTOP\RELEASE\WIN-UNPACKED\hermes.exe`}, "desktop"},
		{"other hermes.exe without marker", platform.Process{Name: "hermes.exe", Cmdline: home + `\bin\hermes.exe update`}, ""},
		{"kernel runner of this home", platform.Process{Name: "python.exe", Cmdline: home + `\python\python.exe ` + home + `\x\hermes_kernel_runner.py`}, "kernel"},
		// SR-1 lookalikes: must never be claimed (they would be killed).
		{"editor opening the kernel script", platform.Process{Name: "notepad.exe", Cmdline: `notepad.exe ` + home + `\x\hermes_kernel_runner.py`}, ""},
		{"rg searching for the kernel script", platform.Process{Name: "rg.exe", Cmdline: `rg hermes_kernel_runner.py`}, ""},
		{"vscode with the kernel script", platform.Process{Name: "Code.exe", Cmdline: `Code.exe ` + checkout + `\hermes_kernel_runner.py`}, ""},
		{"kernel python outside this install", platform.Process{Name: "python.exe", Cmdline: `python.exe C:\elsewhere\hermes_kernel_runner.py`}, ""},
		{"home prefix of another dir", platform.Process{Name: "python.exe", Cmdline: `python.exe ` + home + `-other\x.py gateway run`}, ""},
		{"editor on a file under the backend dir", platform.Process{Name: "Code.exe", Cmdline: `Code.exe ` + checkout + `\hermes_cli serve notes.md`}, ""},
		{"another checkout's desktop", platform.Process{Name: "Hermes.exe", Exe: `D:\other\apps\desktop\release\win-unpacked\Hermes.exe`, Cmdline: `"D:\other\apps\desktop\release\win-unpacked\Hermes.exe"`}, ""},
		{"sibling checkout name prefix", platform.Process{Name: "Hermes.exe", Exe: checkout + `-2\apps\desktop\release\win-unpacked\Hermes.exe`, Cmdline: `x`}, ""},
		{"desktop exe under this checkout", platform.Process{Name: "Hermes.exe", Exe: checkout + `\apps\desktop\release\win-unpacked\Hermes.exe`, Cmdline: `"x"`}, "desktop"},
		{"known exe outside release beats cmdline marker", platform.Process{Name: "Hermes.exe", Exe: `D:\o\Hermes.exe`, Cmdline: `"` + checkout + `\apps\desktop\release\win-unpacked\Hermes.exe"`}, ""},
		{"desktop cmdline marker without checkout", platform.Process{Name: "Hermes.exe", Cmdline: `D:\o\win-unpacked\Hermes.exe`}, ""},
		{"gateway", platform.Process{Name: "hermes.exe", Cmdline: home + `\bin\hermes.exe gateway run`}, "gateway"},
		{"gateway of another home is ignored", platform.Process{Name: "hermes.exe", Cmdline: `D:\other\bin\hermes.exe gateway run`}, ""},
		{"home match is case-insensitive", platform.Process{Name: "hermes.exe", Cmdline: strings.ToUpper(home) + `\bin\hermes.exe gateway run`}, "gateway"},
		{"backend via hermes.exe", platform.Process{Name: "hermes.exe", Cmdline: home + `\bin\hermes.exe serve --port 1`}, "backend"},
		{"backend via hermes_cli", platform.Process{Name: "python.exe", Cmdline: home + `\python\python.exe -m hermes_cli.main serve ` + home}, "backend"},
		{"serve of something else", platform.Process{Name: "node.exe", Cmdline: home + `\x\node.exe serve`}, ""},
		// SR-1F: a known Exe must itself lie under home/checkout.
		{"system python naming the home: kernel", platform.Process{Name: "python.exe", Exe: `C:\Python313\python.exe`, Cmdline: `python.exe ` + home + `\x\hermes_kernel_runner.py`}, ""},
		{"system python naming the home: gateway", platform.Process{Name: "python.exe", Exe: `C:\Python313\python.exe`, Cmdline: `python.exe ` + home + `\x gateway run`}, ""},
		{"system python naming the home: backend", platform.Process{Name: "python.exe", Exe: `C:\Python313\python.exe`, Cmdline: `python.exe -m hermes_cli.main serve ` + home}, ""},
		{"managed python under home: gateway", platform.Process{Name: "python.exe", Exe: home + `\python\python.exe`, Cmdline: `python.exe x gateway run`}, "gateway"},
		{"venv python under checkout: kernel", platform.Process{Name: "python.exe", Exe: checkout + `\venv\Scripts\python.exe`, Cmdline: `python.exe x\hermes_kernel_runner.py`}, "kernel"},
		{"exe in a sibling of home", platform.Process{Name: "python.exe", Exe: home + `-other\python.exe`, Cmdline: `python.exe ` + home + `\x gateway run`}, ""},
		// SR-1F: an unknown Exe only counts when the cmdline's first token is an absolute path under the install.
		{"empty exe, bare python naming home", platform.Process{Name: "python.exe", Cmdline: `python.exe ` + home + `\x gateway run`}, ""},
		{"empty exe, bare hermes.exe naming checkout and marker", platform.Process{Name: "hermes.exe", Cmdline: `hermes.exe --x ` + checkout + ` win-unpacked`}, ""},
		{"empty exe, no name, relative first token", platform.Process{Cmdline: `python ` + home + `\x gateway run`}, ""},
		{"empty exe, absolute python outside install", platform.Process{Name: "python.exe", Cmdline: `C:\Python313\python.exe ` + home + `\x gateway run`}, ""},
		{"empty exe, absolute python under home", platform.Process{Name: "python.exe", Cmdline: home + `\python\python.exe x gateway run`}, "gateway"},
		{"empty exe, quoted absolute python under home", platform.Process{Name: "python.exe", Cmdline: `"` + home + `\python\python.exe" x gateway run`}, "gateway"},
		{"empty cmdline", platform.Process{Name: "hermes.exe"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.p, home, checkout, markers); got != c.want {
				t.Errorf("Classify = %q want %q", got, c.want)
			}
		})
	}
}

func TestClassifyDesktopMarkersPerOS(t *testing.T) {
	mac := platform.Process{Name: "Hermes", Cmdline: "/h/hermes-agent/apps/desktop/release/mac/Hermes.app/Contents/MacOS/Hermes"}
	if got := Classify(mac, "/h", "/h/hermes-agent", []string{"/contents/macos/hermes"}); got != "desktop" {
		t.Errorf("mac main = %q", got)
	}
	helper := platform.Process{Name: "Hermes Helper", Cmdline: "/h/hermes-agent/apps/desktop/release/mac/Hermes.app/Contents/MacOS/Hermes --type=gpu-process"}
	if got := Classify(helper, "/h", "/h/hermes-agent", []string{"/contents/macos/hermes"}); got != "" {
		t.Errorf("mac helper = %q", got)
	}
}

func TestCountByClass(t *testing.T) {
	got := CountByClass([]string{"gateway", "desktop", "gateway", "", "kernel"})
	want := "desktop 1, gateway 2, kernel 1"
	if FormatCounts(got) != want {
		t.Errorf("got %q want %q", FormatCounts(got), want)
	}
	if FormatCounts(nil) != "none" {
		t.Error("empty counts should read none")
	}
}

func marker(t *testing.T) (*Marker, string, *platform.FakeProcs) {
	t.Helper()
	home := t.TempDir()
	procs := platform.NewFakeProcs()
	now := time.Unix(1_790_000_000, 0)
	m := &Marker{Home: home, Pid: 4242, Procs: procs, Now: func() time.Time { return now }}
	return m, filepath.Join(home, MarkerFile), procs
}

func TestMarkerHolder(t *testing.T) {
	m, path, procs := marker(t)
	cases := []struct {
		name    string
		content *string
		alive   []int
		want    int
		stale   bool
	}{
		{"no file", nil, nil, 0, false},
		{"live owner", sp("777\n1790000000\n"), []int{777}, 777, false},
		{"dead owner is stale", sp("777\n1790000000\n"), nil, 0, true},
		{"unparseable is stale", sp("garbage"), nil, 0, true},
		{"empty is stale", sp(""), nil, 0, true},
		{"zero pid is stale", sp("0\n1\n"), []int{0}, 0, true},
		{"space separated first token", sp("888 1790000000"), []int{888}, 888, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_ = os.Remove(path)
			if c.content != nil {
				if err := os.WriteFile(path, []byte(*c.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			existing, _ := procs.List(context.Background())
			for _, p := range existing {
				procs.Remove(p.PID)
			}
			for _, pid := range c.alive {
				procs.Add(platform.Process{PID: pid})
			}
			pid, stale := m.Holder()
			if pid != c.want || stale != c.stale {
				t.Errorf("Holder = %d,%v want %d,%v", pid, stale, c.want, c.stale)
			}
		})
	}
}

func sp(s string) *string { return &s }

func TestMarkerClaimRefreshRelease(t *testing.T) {
	m, path, _ := marker(t)
	if err := m.Claim(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "4242\n1790000000\n" {
		t.Fatalf("marker = %q", b)
	}
	// refresh rewrites the epoch while ours
	later := time.Unix(1_790_000_120, 0)
	m.Now = func() time.Time { return later }
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "4242\n1790000120\n" {
		t.Fatalf("after refresh = %q", b)
	}
	released, err := m.Release()
	if err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("marker still there")
	}
}

func TestMarkerNotOursIsLeftAlone(t *testing.T) {
	m, path, _ := marker(t)
	if err := os.WriteFile(path, []byte("999\n1790000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	released, err := m.Release()
	if err != nil || released {
		t.Fatalf("release = %v, %v", released, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "999\n1790000000\n" {
		t.Errorf("someone else's marker modified: %q", b)
	}
}

func TestMarkerReleaseWhenMissingIsNotAnError(t *testing.T) {
	m, _, _ := marker(t)
	released, err := m.Release()
	if err != nil || released {
		t.Fatalf("got %v, %v", released, err)
	}
}

func TestMarkerRunRefresherStopsOnCancel(t *testing.T) {
	m, path, _ := marker(t)
	_ = m.Claim()
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	m.Now = func() time.Time { return time.Unix(1_790_000_300, 0) }
	go func() { m.RunRefresher(ctx, ticks); close(done) }()
	ticks <- time.Time{}
	// wait for the rewrite
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(path); strings.HasPrefix(string(b), "4242\n"+strconv.Itoa(1_790_000_300)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresher did not stop")
	}
}
