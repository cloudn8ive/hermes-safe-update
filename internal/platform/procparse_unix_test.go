package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		comm, state string
		ppid        int
		start       int64
		wantErr     bool
	}{
		{"plain", "4242 (hermes) S 1 4242 4242 0 -1 4194560 100 0 0 0 5 1 0 0 20 0 18 0 5000 1 2 3\n", "hermes", "S", 1, 5000, false},
		{"comm with parens and spaces", "4300 (Web Content) (x) R 4242 4242 4242 0 -1 4194560 100 0 0 0 5 1 0 0 20 0 1 0 6000 1 2 3", "Web Content) (x", "R", 4242, 6000, false},
		{"zombie", "4301 (kworker/0:1) Z 2 0 0 0 -1 69238880 0 0 0 0 0 0 0 0 20 0 1 0 7000 0 0 0", "kworker/0:1", "Z", 2, 7000, false},
		{"no parens", "junk", "", "", 0, 0, true},
		{"too short", "1 (a) S 1 2", "", "", 0, 0, true},
		{"empty", "", "", "", 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := parseProcStat(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if s.Comm != c.comm || s.State != c.state || s.PPID != c.ppid || s.StartTicks != c.start {
				t.Errorf("got %+v", s)
			}
		})
	}
}

func TestParseProcCmdline(t *testing.T) {
	got := parseProcCmdline([]byte("/opt/h/hermes\x00--type=renderer\x00--lang=en US\x00"))
	if got != "/opt/h/hermes --type=renderer --lang=en US" {
		t.Errorf("got %q", got)
	}
	if parseProcCmdline(nil) != "" || parseProcCmdline([]byte("\x00")) != "" {
		t.Error("empty cmdline must give an empty string")
	}
}

func TestReadProcTableFromFixtures(t *testing.T) {
	root := filepath.Join("testdata", "linux", "proc")
	procs, err := readProcTable(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 3 {
		t.Fatalf("want 3 processes (non-numeric dirs skipped), got %d: %+v", len(procs), procs)
	}
	byPID := map[int]Process{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	main := byPID[4242]
	if main.Name != "hermes" || main.PPID != 1 {
		t.Errorf("main = %+v", main)
	}
	if main.Cmdline != "/opt/hermes/apps/desktop/release/linux-unpacked/hermes --no-sandbox" {
		t.Errorf("cmdline = %q", main.Cmdline)
	}
	if main.Exe != "/opt/hermes/apps/desktop/release/linux-unpacked/hermes" {
		t.Errorf("exe falls back to an absolute argv[0]: %q", main.Exe)
	}
	// btime 1790000000 + 5000 ticks / 100 Hz
	if want := time.Unix(1790000050, 0); !main.Created.Equal(want) {
		t.Errorf("created = %v, want %v", main.Created, want)
	}
	if h := byPID[4300]; h.PPID != 4242 || h.Name != "Web Content) (x" {
		t.Errorf("helper = %+v", h)
	}
	if k := byPID[4301]; k.Cmdline != "" || k.Exe != "" {
		t.Errorf("kernel thread = %+v", k)
	}
}

func TestReadProcTableMissingRoot(t *testing.T) {
	if _, err := readProcTable(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing /proc must be an error")
	}
}

func TestReadProcTableSkipsVanishedProcess(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "55"), 0o755); err != nil { // no stat file
		t.Fatal(err)
	}
	procs, err := readProcTable(context.Background(), root)
	if err != nil || len(procs) != 0 {
		t.Errorf("got %v, %v", procs, err)
	}
}

func TestProcIsZombie(t *testing.T) {
	root := filepath.Join("testdata", "linux", "proc")
	if !procIsZombie(root, 4301) {
		t.Error("4301 is Z")
	}
	if procIsZombie(root, 4242) || procIsZombie(root, 99999) {
		t.Error("running and missing processes are not zombies")
	}
}

func TestParsePSTableFromFixtures(t *testing.T) {
	comm, err := os.ReadFile(filepath.Join("testdata", "darwin", "ps_comm.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := os.ReadFile(filepath.Join("testdata", "darwin", "ps_command.txt"))
	if err != nil {
		t.Fatal(err)
	}
	procs := parsePSTable(string(comm), string(cmd))
	if len(procs) != 4 {
		t.Fatalf("want 4 (garbage line skipped), got %d: %+v", len(procs), procs)
	}
	by := map[int]Process{}
	for _, p := range procs {
		by[p.PID] = p
	}
	m := by[501]
	if m.PPID != 1 || m.Name != "Hermes" || m.Exe != "/Applications/Hermes.app/Contents/MacOS/Hermes" || m.Cmdline != m.Exe {
		t.Errorf("main = %+v", m)
	}
	h := by[502]
	if h.PPID != 501 || h.Name != "Hermes Helper (Renderer)" || h.Cmdline == h.Exe {
		t.Errorf("helper = %+v", h)
	}
	if got := h.Cmdline; got != h.Exe+" --type=renderer --lang=en-US" {
		t.Errorf("helper cmdline = %q", got)
	}
	// A path with spaces: comm= takes the rest of the line.
	if sp := by[777]; sp.Exe != "/Users/u/Library/Application Support/Hermes/x y" || sp.Name != "x y" {
		t.Errorf("spaces = %+v", sp)
	}
}

func TestParsePSTableCommandMissing(t *testing.T) {
	procs := parsePSTable("  9     1 /bin/zsh\n", "")
	if len(procs) != 1 || procs[0].Cmdline != "" || procs[0].Name != "zsh" {
		t.Errorf("got %+v", procs)
	}
}
