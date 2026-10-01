package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// routeRunner sends hermes launcher calls to a fake and everything else
// (local git only) to the real runner.
type routeRunner struct {
	hermes *execx.Fake
	real   execx.Runner
}

func (r *routeRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if c.Argv[0] == "hermes-under-test" {
		return r.hermes.Run(ctx, c)
	}
	return r.real.Run(ctx, c)
}

func (r *routeRunner) Stream(ctx context.Context, c execx.Cmd, f func(string)) (execx.Result, error) {
	if c.Argv[0] == "hermes-under-test" {
		return r.hermes.Stream(ctx, c, f)
	}
	return r.real.Stream(ctx, c, f)
}

func setupEnv(t *testing.T) (*env, *routeRunner, string) {
	t.Helper()
	e := newEnv(t)
	upstream := newBare(t, "up")
	work := newWork(t, upstream)
	commit(t, work, "a.txt", "1")
	g(t, work, "push", "origin", "main")
	rr := &routeRunner{hermes: &execx.Fake{}, real: execx.New()}
	return e, rr, upstream
}

func setupDeps(e *env, rr *routeRunner, upstream string) Deps {
	d := e.realDeps()
	d.Runner = rr
	d.CloneURL = upstream
	d.Launcher = "hermes-under-test"
	return d
}

func TestSetupClonesRecordsStateAndCreatesJob(t *testing.T) {
	ctx := context.Background()
	e, rr, upstream := setupEnv(t)
	dest := filepath.Join(t.TempDir(), "mirrors", "hermes-agent.git")
	// Existing state: unknown field kept, declined offer cleared.
	if err := os.WriteFile(e.state, []byte(`{"keep": {"a": 1}, "mirror_offer": "declined"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var created []string
	rr.hermes.OnFunc([]string{"hermes-under-test", "cron", "create"}, func(c execx.Cmd) execx.Result {
		created = c.Argv
		// Simulate Hermes writing the job.
		writeJobs(t, e.home, `{"jobs":[{"id":"j1","script":"`+shimName+`","enabled":true}]}`)
		return execx.Result{}
	})
	api := newAPI(t)
	api.Size = 1000
	d := setupDeps(e, rr, upstream)
	d.APIBase = api.URL

	if err := New(d).Setup(ctx, dest); err != nil {
		t.Fatalf("Setup: %v\nout=%v\nlogs=%v", err, e.out, e.logs)
	}
	if !validBare(dest) {
		t.Fatal("mirror not created")
	}
	if isDir(dest + ".partial") {
		t.Fatal(".partial left behind")
	}
	if got := g(t, dest, "config", "--get", "uploadpack.allowFilter"); got != "true" {
		t.Fatalf("allowFilter = %q", got)
	}
	if got := g(t, dest, "config", "--get", "gc.auto"); got != "0" {
		t.Fatalf("gc.auto = %q", got)
	}
	data, _ := os.ReadFile(e.state)
	s := string(data)
	if !strings.Contains(s, `"keep"`) || strings.Contains(s, "mirror_offer") || !strings.Contains(s, `"set_up"`) {
		t.Fatalf("machine state:\n%s", s)
	}
	st := New(d).Status()
	if !st.OK || !st.JobActive || !st.Complete() || st.LastOK.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	want := []string{"hermes-under-test", "cron", "create", "every 4h", "--name", "Hermes update mirror refresh",
		"--script", shimName, "--no-agent", "--deliver", "local", "--failure-deliver", "local"}
	if strings.Join(created, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cron create argv = %q", created)
	}
	shim, err := os.ReadFile(filepath.Join(e.home, "scripts", shimName))
	if err != nil {
		t.Fatalf("shim not written: %v", err)
	}
	if !strings.Contains(string(shim), `"mirror", "refresh"`) || !strings.Contains(string(shim), `hermes-safe-update.exe`) {
		t.Fatalf("shim:\n%s", shim)
	}
}

func TestSetupJobStates(t *testing.T) {
	ctx := context.Background()
	t.Run("active job: nothing created", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		writeJobs(t, e.home, `{"jobs":[{"id":"j1","script":"`+legacyShim+`"}]}`)
		d := setupDeps(e, rr, up)
		if err := New(d).Setup(ctx, filepath.Join(t.TempDir(), "m.git")); err != nil {
			t.Fatal(err)
		}
		if len(rr.hermes.Calls()) != 0 {
			t.Fatalf("hermes called: %v", rr.hermes.Calls())
		}
	})
	t.Run("paused job is resumed", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		writeJobs(t, e.home, `{"jobs":[{"id":"j7","script":"`+shimName+`","paused_at":"2026-01-01"}]}`)
		rr.hermes.OnFunc([]string{"hermes-under-test", "cron", "resume", "j7"}, func(execx.Cmd) execx.Result {
			writeJobs(t, e.home, `{"jobs":[{"id":"j7","script":"`+shimName+`"}]}`)
			return execx.Result{}
		})
		if err := New(setupDeps(e, rr, up)).Setup(ctx, filepath.Join(t.TempDir(), "m.git")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cron create fails: incomplete, mirror still recorded", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		rr.hermes.OnPrefix([]string{"hermes-under-test", "cron", "create"}, execx.Result{Code: 1, Output: "boom"})
		err := New(setupDeps(e, rr, up)).Setup(ctx, filepath.Join(t.TempDir(), "m.git"))
		if !errors.Is(err, ErrIncomplete) || SetupExitCode(err) != 1 {
			t.Fatalf("err = %v code=%d", err, SetupExitCode(err))
		}
		if !New(e.deps()).Status().OK {
			t.Fatal("mirror not recorded")
		}
	})
}

func TestSetupExistingMirrorIsKeptAndRefreshed(t *testing.T) {
	ctx := context.Background()
	e, rr, up := setupEnv(t)
	m := mirrorOf(t, e, up) // writes machine state
	d := setupDeps(e, rr, up)
	d.CloneURL = filepath.Join(t.TempDir(), "must-not-be-cloned")
	rr.hermes.OnFunc([]string{"hermes-under-test", "cron", "create"}, func(execx.Cmd) execx.Result {
		writeJobs(t, e.home, `{"jobs":[{"id":"j","script":"`+shimName+`"}]}`)
		return execx.Result{}
	})
	if err := New(d).Setup(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := New(d).mirrorPath(); got != m {
		t.Fatalf("path = %q", got)
	}
}

func TestSetupRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("not enough space", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		api := newAPI(t)
		api.Size = 2 * 1000 * 1000 // 2 GB in KB -> needs 2*1.5+5 = 8 GB
		d := setupDeps(e, rr, up)
		d.APIBase = api.URL
		d.Disk = &platform.FakeDisk{FreeBytes: 7e9}
		dest := filepath.Join(t.TempDir(), "m.git")
		err := New(d).Setup(ctx, dest)
		if !errors.Is(err, ErrNoSpace) || SetupExitCode(err) != 2 {
			t.Fatalf("err = %v", err)
		}
		if isDir(dest) || isDir(dest+".partial") {
			t.Fatal("something was created")
		}
		if New(e.deps()).Status().ConfiguredPath != "" {
			t.Fatal("machine state written")
		}
	})
	t.Run("clone failure leaves nothing configured", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		d := setupDeps(e, rr, up)
		d.CloneURL = filepath.Join(t.TempDir(), "no-such-repo.git")
		dest := filepath.Join(t.TempDir(), "m.git")
		err := New(d).Setup(ctx, dest)
		if !errors.Is(err, ErrCloneFailed) || SetupExitCode(err) != 1 {
			t.Fatalf("err = %v", err)
		}
		if isDir(dest) || New(e.deps()).Status().ConfiguredPath != "" {
			t.Fatal("state left behind")
		}
	})
	t.Run("unfinished earlier attempt is replaced", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		rr.hermes.OnPrefix([]string{"hermes-under-test"}, execx.Result{})
		d := setupDeps(e, rr, up)
		dest := filepath.Join(t.TempDir(), "m.git")
		_ = os.MkdirAll(dest+".partial", 0o755)
		_ = os.WriteFile(filepath.Join(dest+".partial", "junk"), []byte("x"), 0o644)
		_ = New(d).Setup(ctx, dest)
		if !validBare(dest) || isDir(dest+".partial") {
			t.Fatal("did not recover from the partial dir")
		}
	})
	t.Run("no location without a path and no volume fits", func(t *testing.T) {
		e, rr, up := setupEnv(t)
		d := setupDeps(e, rr, up)
		d.Disk = &platform.FakeDisk{FreeBytes: 1e9, Vols: []platform.Volume{{Root: `C:\`, Free: 1e9, Fixed: true, System: true}}}
		err := New(d).Setup(ctx, "")
		if !errors.Is(err, ErrNoLocation) || SetupExitCode(err) != 2 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLocation(t *testing.T) {
	ctx := context.Background()
	gb := func(n float64) uint64 { return uint64(n * 1e9) }
	vols := func(v ...platform.Volume) Deps {
		e := newEnv(t)
		api := newAPI(t)
		api.Size = 1000 * 1000 // 1 GB -> need 6.5 GB
		d := e.deps()
		d.APIBase = api.URL
		d.FallbackDir = filepath.Join(t.TempDir(), "local")
		d.Disk = &platform.FakeDisk{FreeBytes: gb(50), Vols: v}
		return d
	}
	t.Run("non-system fixed volume with most room wins", func(t *testing.T) {
		d := vols(
			platform.Volume{Root: "C:/", Free: gb(200), Fixed: true, System: true},
			platform.Volume{Root: "D:/", Free: gb(30), Fixed: true},
			platform.Volume{Root: "E:/", Free: gb(90), Fixed: true},
			platform.Volume{Root: "F:/", Free: gb(500), Fixed: false}, // removable/network: skipped
		)
		p, size, why := New(d).Location(ctx)
		if p != filepath.Join("E:/", "hermes-mirror", "hermes-agent.git") || size != 1 || !strings.Contains(why, "GitHub") {
			t.Fatalf("p=%q size=%v why=%q", p, size, why)
		}
	})
	t.Run("too-small other volume falls back to the system location", func(t *testing.T) {
		d := vols(
			platform.Volume{Root: "C:/", Free: gb(200), Fixed: true, System: true},
			platform.Volume{Root: "D:/", Free: gb(3), Fixed: true},
		)
		p, _, _ := New(d).Location(ctx)
		if p != filepath.Join(d.FallbackDir, "hermes-mirror", "hermes-agent.git") {
			t.Fatalf("p = %q", p)
		}
	})
	t.Run("nowhere has room", func(t *testing.T) {
		d := vols(platform.Volume{Root: "C:/", Free: gb(4), Fixed: true, System: true})
		p, _, why := New(d).Location(ctx)
		if p != "" || !strings.Contains(why, "no drive has the 6.5 GB it needs (most free: 4.0 GB)") {
			t.Fatalf("p=%q why=%q", p, why)
		}
	})
	t.Run("API down: estimate", func(t *testing.T) {
		e := newEnv(t)
		api := newAPI(t)
		api.Close()
		d := e.deps()
		d.APIBase = api.URL
		d.Disk = &platform.FakeDisk{Vols: []platform.Volume{{Root: "D:/", Free: gb(100), Fixed: true}}}
		_, size, why := New(d).Location(ctx)
		if size != 1.5 || !strings.Contains(why, "from estimate") {
			t.Fatalf("size=%v why=%q", size, why)
		}
	})
}

func TestDeclineAndRemove(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	writeJobs(t, e.home, `{"jobs":[{"id":"j9","script":"`+shimName+`"}]}`)
	e.runner.OnPrefix([]string{"hermes-under-test"}, execx.Result{})
	s := New(e.deps())
	if err := s.Decline(); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Declined {
		t.Fatal("not declined")
	}
	if err := s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.ConfiguredPath != "" {
		t.Fatalf("mirror still configured: %+v", st)
	}
	if !isDir(m) {
		t.Fatal("folder must be kept")
	}
	data, _ := os.ReadFile(e.state)
	if !strings.Contains(string(data), `"keep"`) || !strings.Contains(string(data), "declined") {
		t.Fatalf("other state lost:\n%s", data)
	}
	last := e.runner.Calls()[len(e.runner.Calls())-1].Argv
	if strings.Join(last, " ") != "hermes-under-test cron remove j9" {
		t.Fatalf("argv = %v", last)
	}
}

// The mirror folder is chosen by the user and can sit under a personal
// path; the log file is meant to be pasted into bug reports, so it records
// that a setup/seed happened, not where.
func TestMirrorPathStaysOutOfTheLog(t *testing.T) {
	ctx := context.Background()
	e, rr, upstream := setupEnv(t)
	dest := filepath.Join(t.TempDir(), "mirrors", "hermes-agent.git")
	rr.hermes.OnFunc([]string{"hermes-under-test", "cron", "create"}, func(execx.Cmd) execx.Result {
		writeJobs(t, e.home, `{"jobs":[{"id":"j1","script":"`+shimName+`","enabled":true}]}`)
		return execx.Result{}
	})
	api := newAPI(t)
	api.Size = 1000
	d := setupDeps(e, rr, upstream)
	d.APIBase = api.URL
	if err := New(d).Setup(ctx, dest); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.RemoveAll(dest); err != nil { // now the recorded mirror is gone
		t.Fatal(err)
	}
	if res := New(d).Seed(ctx, t.TempDir()); res.Used {
		t.Fatalf("seed used a deleted mirror: %+v", res)
	}
	if len(e.logs) == 0 {
		t.Fatal("nothing was logged")
	}
	for _, l := range e.logs {
		if strings.Contains(l, dest) || strings.Contains(l, filepath.ToSlash(dest)) {
			t.Errorf("log line carries the mirror path: %q", l)
		}
	}
}
