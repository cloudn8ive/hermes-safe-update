package hermes

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// created returns a Created func serving the given creation times by pid
// (a pid not in the map is unreadable).
func created(m map[int]time.Time) func(int) (time.Time, bool) {
	return func(pid int) (time.Time, bool) {
		t, ok := m[pid]
		return t, ok
	}
}

func epochTime(sec float64) time.Time {
	return time.Unix(0, int64(sec*1e9))
}

func readMarker(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMarkerClaimWritesV2WhenCreationTimeIsKnown(t *testing.T) {
	m, path, _ := marker(t)
	m.Created = created(map[int]time.Time{4242: time.Unix(1_789_999_000, 123_456_789)})
	if err := m.Claim(); err != nil {
		t.Fatal(err)
	}
	if got, want := readMarker(t, path), "4242\n1790000000\nct:1789999000.123\n"; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
	// rounding to three decimals carries into the seconds, like Python's f"{ct:.3f}"
	m.Created = created(map[int]time.Time{4242: time.Unix(1_789_999_000, 999_700_000)})
	if err := m.Claim(); err != nil {
		t.Fatal(err)
	}
	if got, want := readMarker(t, path), "4242\n1790000000\nct:1789999001.000\n"; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
}

// Never write a ct that could mismatch: an unreadable creation time means v1.
func TestMarkerClaimStaysV1WithoutAReadableCreationTime(t *testing.T) {
	for name, created := range map[string]func(int) (time.Time, bool){
		"no reader":        nil,
		"reader fails":     func(int) (time.Time, bool) { return time.Time{}, false },
		"zero time":        func(int) (time.Time, bool) { return time.Time{}, true },
		"pid not in table": created(map[int]time.Time{1: time.Unix(1, 0)}),
	} {
		t.Run(name, func(t *testing.T) {
			m, path, _ := marker(t)
			m.Created = created
			if err := m.Claim(); err != nil {
				t.Fatal(err)
			}
			if got := readMarker(t, path); got != "4242\n1790000000\n" {
				t.Errorf("marker = %q, want the v1 form", got)
			}
		})
	}
}

func TestMarkerRefreshKeepsEveryLineButTheStartTime(t *testing.T) {
	cases := []struct {
		name, before, after string
	}{
		{"v2", "4242\n1790000000\nct:1789999000.123\n", "4242\n1790000120\nct:1789999000.123\n"},
		{"v2 with the delegate Hermes appends", "4242\n1790000000\nct:1789999000.123\ndelegate:5151 ct:1790000001.500\nrun:abc-1\n",
			"4242\n1790000120\nct:1789999000.123\ndelegate:5151 ct:1790000001.500\nrun:abc-1\n"},
		{"v2 written with CRLF", "4242\r\n1790000000\r\nct:1789999000.123\r\n", "4242\r\n1790000120\r\nct:1789999000.123\r\n"},
		{"v1", "4242\n1790000000\n", "4242\n1790000120\n"},
		{"epoch line without a newline", "4242\n1790000000", "4242\n1790000120\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, path, _ := marker(t)
			// a different creation time now must NOT change the recorded one
			m.Created = created(map[int]time.Time{4242: time.Unix(1_700_000_000, 0)})
			if err := os.WriteFile(path, []byte(c.before), 0o644); err != nil {
				t.Fatal(err)
			}
			m.Now = func() time.Time { return time.Unix(1_790_000_120, 0) }
			if err := m.Refresh(); err != nil {
				t.Fatal(err)
			}
			if got := readMarker(t, path); got != c.after {
				t.Errorf("after refresh = %q, want %q", got, c.after)
			}
		})
	}
}

func TestMarkerRefreshAndReleaseLeaveForeignV2Alone(t *testing.T) {
	m, path, _ := marker(t)
	const foreign = "999\n1790000000\nct:1789999000.000\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if released, err := m.Release(); err != nil || released {
		t.Fatalf("release = %v, %v", released, err)
	}
	if got := readMarker(t, path); got != foreign {
		t.Errorf("someone else's marker modified: %q", got)
	}
}

func TestMarkerReleaseDeletesOursWithDelegateLines(t *testing.T) {
	m, path, _ := marker(t)
	if err := os.WriteFile(path, []byte("4242\n1790000000\nct:1789999000.123\ndelegate:5151 ct:1790000001.500\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if released, err := m.Release(); err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("marker still there")
	}
}

func TestMarkerHolderV2(t *testing.T) {
	const owner, deleg = 777, 888
	base := 1_789_999_000.0
	type tc struct {
		name    string
		content string
		alive   []int
		cts     map[int]time.Time
		nilCr   bool // Created reader not wired
		want    int
		stale   bool
	}
	cases := []tc{
		{"v1 owner alive", "777\n1790000000\n", []int{owner}, nil, false, owner, false},
		{"v2 same incarnation", "777\n1790000000\nct:1789999000.123\n", []int{owner}, map[int]time.Time{owner: epochTime(base + 0.1)}, false, owner, false},
		{"v2 inside the 2 s tolerance", "777\n1790000000\nct:1789999000.000\n", []int{owner}, map[int]time.Time{owner: epochTime(base + 1.9)}, false, owner, false},
		{"v2 pid reused by another process", "777\n1790000000\nct:1789999000.000\n", []int{owner}, map[int]time.Time{owner: epochTime(base + 3600)}, false, 0, true},
		{"v2 just over the tolerance", "777\n1790000000\nct:1789999000.000\n", []int{owner}, map[int]time.Time{owner: epochTime(base + 2.5)}, false, 0, true},
		{"v2 owner dead", "777\n1790000000\nct:1789999000.000\n", nil, nil, false, 0, true},
		{"v2 creation time unreadable: the live pid still holds it", "777\n1790000000\nct:1789999000.000\n", []int{owner}, map[int]time.Time{}, false, owner, false},
		{"v2 without a reader: the live pid still holds it", "777\n1790000000\nct:1789999000.000\n", []int{owner}, nil, true, owner, false},
		{"v2 CRLF, BOM and padding", "\ufeff 777 \r\n 1790000000\t\r\nct:1789999000.000\r\n", []int{owner}, map[int]time.Time{owner: epochTime(base)}, false, owner, false},
		{"owner dead, delegate alive", "777\n1790000000\nct:1789999000.000\ndelegate:888 ct:1789999100.000\n", []int{deleg}, map[int]time.Time{deleg: epochTime(base + 100)}, false, deleg, false},
		{"owner dead, delegate pid reused", "777\n1790000000\nct:1789999000.000\ndelegate:888 ct:1789999100.000\n", []int{deleg}, map[int]time.Time{deleg: epochTime(base + 9999)}, false, 0, true},
		{"owner alive wins over the delegate", "777\n1790000000\nct:1789999000.000\ndelegate:888 ct:1789999100.000\n", []int{owner, deleg}, map[int]time.Time{owner: epochTime(base), deleg: epochTime(base + 100)}, false, owner, false},
		{"first well-formed delegate counts", "777\n1790000000\nct:1789999000.000\ndelegate:abc ct:1\ndelegate:888 ct:1789999100.000\ndelegate:999 ct:1\n", []int{deleg}, map[int]time.Time{deleg: epochTime(base + 100)}, false, deleg, false},
		{"a delegate line counts from line 4 on only (as in Hermes)", "777\n1790000000\ndelegate:888 ct:1789999100.000\n", []int{deleg}, map[int]time.Time{deleg: epochTime(base + 100)}, false, 0, true},
		{"garbage ct line makes it v1", "777\n1790000000\nct:soon\n", []int{owner}, map[int]time.Time{owner: epochTime(base + 3600)}, false, owner, false},
		{"unparseable", "garbage", nil, nil, false, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, path, procs := marker(t)
			if !c.nilCr {
				m.Created = created(c.cts)
			}
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
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

func TestParseMarker(t *testing.T) {
	got := parseMarker([]byte("\ufeff12\r\n1790000000\r\nct:1789999000.5\r\n  delegate:34 ct:1789999100.250 \r\nrun:x\r\n"))
	if got.pid != 12 || got.ct == nil || *got.ct != 1789999000.5 || got.delegatePid != 34 || got.delegateCT != 1789999100.25 {
		t.Errorf("parsed %+v", got)
	}
	if got := parseMarker([]byte("12 1790000000")); got.pid != 12 || got.ct != nil {
		t.Errorf("space separated: %+v", got)
	}
	for _, bad := range []string{"", "x\n1\n", "0\n1\n", "-3\n1\n"} {
		if got := parseMarker([]byte(bad)); got.pid != 0 {
			t.Errorf("parseMarker(%q).pid = %d, want 0", bad, got.pid)
		}
	}
}

func TestConcurrentUpdateNote(t *testing.T) {
	out := readFixture(t, "update-concurrent.txt")
	note := ConcurrentUpdateNote(UpdateExitConcurrent, out)
	for _, want := range []string{"another Hermes update is running", "started 3m 4s ago, process 4711", "Wait for it to finish"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	if got := ConcurrentUpdateNote(UpdateExitConcurrent, strings.ReplaceAll(out, "\n", "\r\n")); got != note {
		t.Errorf("CRLF output reads differently: %q", got)
	}
	if got := ConcurrentUpdateNote(1, out); got != "" {
		t.Errorf("rc 1 must not read as a concurrent update: %q", got)
	}
	if got := ConcurrentUpdateNote(0, ""); got != "" {
		t.Errorf("rc 0: %q", got)
	}
	// the other rc-2 wording, and rc 2 with no recognisable text
	if got := ConcurrentUpdateNote(2, "✗ Cannot lock this install for the update: C:\\x is read-only.\n\n  more"); !strings.Contains(got, "could not lock the install") || !strings.Contains(got, "C:\\x is read-only") {
		t.Errorf("cannot-lock note = %q", got)
	}
	if got := ConcurrentUpdateNote(2, "something new"); !strings.Contains(got, "another update or Hermes instance holds the install") {
		t.Errorf("unknown rc 2 note = %q", got)
	}
}

func TestEvaluateUpdateRC2NamesTheOtherUpdate(t *testing.T) {
	const head = "4e7403130ee278bd99c450fcd9f73c6b32135c95"
	out := readFixture(t, "update-concurrent.txt")
	r := EvaluateUpdate(VerifyInput{RC: 2, Output: out, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true})
	if r.OK || r.Line != "RESULT: PROBLEM (update rc=2, gateway on new code=true, desktop verify rc=0)" ||
		!strings.Contains(r.Note, "another Hermes update is running") {
		t.Errorf("got %+v", r)
	}
	// every other rc keeps the plain line
	r = EvaluateUpdate(VerifyInput{RC: 1, Output: out, Head: head, Gateway: GatewayState{CodeSHA: head, PID: 5}, Alive: true})
	if r.Note != "" {
		t.Errorf("rc 1 note: %q", r.Note)
	}
}

// The files this tool writes and the code that reads them agree with the
// shapes Hermes documents (update_lock.py module docstring).
func TestMarkerContractShapes(t *testing.T) {
	for line, want := range map[string]bool{"ct:1789999000.123": true, "ct:1789999000": true, "ct:": false, "ct:1.2.3": false, "CT:1": false, " ct:1": false} {
		if got := MarkerCreateTimeRe.MatchString(line); got != want {
			t.Errorf("MarkerCreateTimeRe(%q) = %v", line, got)
		}
	}
	for line, want := range map[string]bool{"delegate:12 ct:1789999000.5": true, "delegate:12  ct:1": false, "delegate:x ct:1": false, "delegate:12 ct:": false} {
		if got := MarkerDelegateRe.MatchString(line); got != want {
			t.Errorf("MarkerDelegateRe(%q) = %v", line, got)
		}
	}
}
