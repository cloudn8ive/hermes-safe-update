package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAgeText(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		last time.Time
		want string
	}{
		{"never", time.Time{}, "never refreshed"},
		{"seconds ago", now.Add(-30 * time.Second), "refreshed just now"},
		{"minutes", now.Add(-5 * time.Minute), "refreshed 5 min ago"},
		{"just under two hours", now.Add(-119 * time.Minute), "refreshed 119 min ago"},
		{"hours", now.Add(-125 * time.Minute), "refreshed 2 h ago"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AgeText(c.last, now); got != c.want {
				t.Fatalf("AgeText = %q, want %q", got, c.want)
			}
		})
	}
	if got := SourceLine(now.Add(-3*time.Minute), now); got != "local mirror, refreshed 3 min ago" {
		t.Fatalf("SourceLine = %q", got)
	}
}

// makeBareShape creates the minimum a "valid bare repo" check looks for.
func makeBareShape(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJobs(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "cron"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cron", "jobs.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatus(t *testing.T) {
	t.Run("nothing set up", func(t *testing.T) {
		e := newEnv(t)
		st := New(e.deps()).Status()
		if st.OK || st.JobExists || st.Declined || st.ConfiguredPath != "" || st.Complete() {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("missing drive reads as no mirror but keeps the configured path", func(t *testing.T) {
		e := newEnv(t)
		gone := filepath.Join(t.TempDir(), "gone", "m.git")
		saveMachine(t, e, gone)
		st := New(e.deps()).Status()
		if st.OK || st.ConfiguredPath != gone {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("valid mirror, last ok and error from the state file", func(t *testing.T) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m.git")
		makeBareShape(t, m)
		saveMachine(t, e, m)
		writeState(t, e, `{"last_ok": 1790000000, "last_error": "boom"}`)
		st := New(e.deps()).Status()
		if !st.OK || st.LastError != "boom" || st.LastOK.Unix() != 1790000000 {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("declined is read from machine state", func(t *testing.T) {
		e := newEnv(t)
		if err := os.WriteFile(e.state, []byte(`{"mirror_offer": "declined"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !New(e.deps()).Status().Declined {
			t.Fatal("want Declined")
		}
	})
	jobTable := []struct {
		name           string
		body           string
		exists, active bool
	}{
		{"no jobs file", "", false, false},
		{"other job only", `{"jobs":[{"script":"x.py"}]}`, false, false},
		{"python job active", `{"jobs":[{"id":"a","script":"hermes_update_mirror.py"}]}`, true, true},
		{"go shim job active, windows path", `{"jobs":[{"id":"a","script":"C:\\h\\scripts\\` + shimName + `","enabled":true}]}`, true, true},
		{"bare list form", `[{"id":"a","script":"` + shimName + `"}]`, true, true},
		{"disabled", `{"jobs":[{"id":"a","script":"` + shimName + `","enabled":false}]}`, true, false},
		{"paused_at", `{"jobs":[{"id":"a","script":"` + shimName + `","paused_at":"2026-01-01"}]}`, true, false},
		{"state paused", `{"jobs":[{"id":"a","script":"` + shimName + `","state":"Paused"}]}`, true, false},
		{"not json", `{{{`, false, false},
	}
	for _, c := range jobTable {
		t.Run("job: "+c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.body != "" {
				writeJobs(t, e.home, c.body)
			}
			st := New(e.deps()).Status()
			if st.JobExists != c.exists || st.JobActive != c.active {
				t.Fatalf("exists/active = %v/%v, want %v/%v", st.JobExists, st.JobActive, c.exists, c.active)
			}
		})
	}
	t.Run("complete needs mirror and active job", func(t *testing.T) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m.git")
		makeBareShape(t, m)
		saveMachine(t, e, m)
		writeJobs(t, e.home, `{"jobs":[{"id":"a","script":"`+shimName+`"}]}`)
		if !New(e.deps()).Status().Complete() {
			t.Fatal("want complete")
		}
	})
}

func saveMachine(t *testing.T, e *env, mirrorPath string) {
	t.Helper()
	body := `{"mirror": {"path": ` + quote(mirrorPath) + `, "set_up": "2026-10-01T12:00:00+00:00"}, "keep": 1}`
	if err := os.WriteFile(e.state, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func writeState(t *testing.T, e *env, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(e.home, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.home, "cache", stateFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLock(t *testing.T) {
	ctx := context.Background()
	lockPath := func(e *env) string { return filepath.Join(e.home, "cache", lockFile) }
	t.Run("acquire and release", func(t *testing.T) {
		e := newEnv(t)
		s := New(e.deps())
		rel, err := s.acquireLock(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !isFile(lockPath(e)) {
			t.Fatal("lock file missing")
		}
		rel()
		if isFile(lockPath(e)) {
			t.Fatal("lock file not removed")
		}
	})
	t.Run("held by a live pid: contention", func(t *testing.T) {
		e := newEnv(t)
		e.alive[4242] = true
		s := New(e.deps())
		_ = os.MkdirAll(filepath.Dir(lockPath(e)), 0o755)
		stamp := e.now.Now().Unix()
		if err := os.WriteFile(lockPath(e), []byte(fmtLock(4242, stamp)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.acquireLock(ctx, 0); err != ErrBusy {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
	})
	t.Run("waits by polling once a second, then gives up", func(t *testing.T) {
		e := newEnv(t)
		e.alive[4242] = true
		s := New(e.deps())
		_ = os.MkdirAll(filepath.Dir(lockPath(e)), 0o755)
		_ = os.WriteFile(lockPath(e), []byte(fmtLock(4242, e.now.Now().Unix())), 0o644)
		if _, err := s.acquireLock(ctx, 3*time.Second); err != ErrBusy {
			t.Fatalf("err = %v", err)
		}
		if len(e.sleeps) != 3 || e.sleeps[0] != time.Second {
			t.Fatalf("sleeps = %v", e.sleeps)
		}
	})
	t.Run("dead pid: stale lock is taken over", func(t *testing.T) {
		e := newEnv(t)
		s := New(e.deps())
		_ = os.MkdirAll(filepath.Dir(lockPath(e)), 0o755)
		_ = os.WriteFile(lockPath(e), []byte(fmtLock(4242, e.now.Now().Unix())), 0o644)
		if _, err := s.acquireLock(ctx, 0); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("older than 1800 s: stale even if pid alive", func(t *testing.T) {
		e := newEnv(t)
		e.alive[4242] = true
		s := New(e.deps())
		_ = os.MkdirAll(filepath.Dir(lockPath(e)), 0o755)
		_ = os.WriteFile(lockPath(e), []byte(fmtLock(4242, e.now.Now().Add(-31*time.Minute).Unix())), 0o644)
		if _, err := s.acquireLock(ctx, 0); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("garbage lock content is stale", func(t *testing.T) {
		e := newEnv(t)
		s := New(e.deps())
		_ = os.MkdirAll(filepath.Dir(lockPath(e)), 0o755)
		_ = os.WriteFile(lockPath(e), []byte("not a lock"), 0o644)
		if _, err := s.acquireLock(ctx, 0); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func fmtLock(pid int, ts int64) string {
	return strings.TrimSpace(strings.Join([]string{itoa(pid), itoa(int(ts))}, " "))
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestNewNeverDefaultsToTheRealAPI(t *testing.T) {
	s := New(newEnv(t).deps())
	if s.d.APIBase != "" {
		t.Fatalf("library defaulted APIBase to %q; only main() may set the real endpoint", s.d.APIBase)
	}
	if _, ok := s.apiGet(context.Background(), "/x", "application/json", time.Second, 10); ok {
		t.Fatal("apiGet answered with no APIBase")
	}
}
