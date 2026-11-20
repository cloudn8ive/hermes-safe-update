package extras

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

const launcher = "/fake/hermes"

var (
	pluginsJSON = `[
  {"name": "memory-demo", "class": "catalog", "current": "0123456789abcdef", "latest": "89abcdef01234567", "update_available": true, "needs_fixing": null, "reason": ""},
  {"name": "notes-demo", "class": "git", "current": "1111111111111111", "latest": "1111111111111111", "update_available": false, "needs_fixing": null, "reason": ""},
  {"name": "by-hand", "class": "manual", "current": null, "latest": null, "update_available": null, "needs_fixing": null, "reason": "no provenance; not auto-updatable"}
]`
	pluginsCurrentJSON = `[{"name": "notes-demo", "class": "git", "current": "11", "latest": "11", "update_available": false, "needs_fixing": null, "reason": ""}]`
	skillsUpdate       = "┌──┐\n│ Name │ Source │ Status │\n├──┤\n│ demo-skill │ official │ update_available │\n└──┘\n1 update(s) available across 1 checked skill(s)\n"
	skillsCurrent      = "┌──┐\n│ Name │ Source │ Status │\n├──┤\n│ demo-skill │ official │ up_to_date │\n└──┘\n0 update(s) available across 1 checked skill(s)\n"
	doctorOK           = "✓ node 1.2.3\n? bws: not installed\n✓ git 2.0\n"
	doctorFault        = "✓ node 1.2.3\n✗ git: realized bytes do not match recorded digest\n"
)

func argv(tail ...string) []string { return append([]string{launcher}, tail...) }

func newSvc(f *execx.Fake) *Svc {
	return New(Deps{Runner: f, Launcher: launcher})
}

func TestProbeNothingToDo(t *testing.T) {
	f := &execx.Fake{}
	f.On(argv("plugins", "check-updates", "--json"), execx.Result{Output: pluginsCurrentJSON})
	f.On(argv("skills", "check"), execx.Result{Output: skillsCurrent})
	f.On(argv("pm", "doctor"), execx.Result{Output: doctorOK})
	rep := newSvc(f).Probe(context.Background(), Options{Plugins: true, Skills: true, Tools: true})
	if rep.Plugins.Err != "" || len(rep.Plugins.Updatable()) != 0 || len(rep.Plugins.Other()) != 0 {
		t.Errorf("plugins: %+v", rep.Plugins)
	}
	if rep.Skills.Err != "" || rep.Skills.Check.Updates != 0 {
		t.Errorf("skills: %+v", rep.Skills)
	}
	if !rep.Tools.DoctorRanOK || len(rep.Tools.Doctor.Faults) != 0 || rep.Tools.UpdatesRan {
		t.Errorf("tools: %+v", rep.Tools)
	}
}

func TestProbeUpdatesAndFaults(t *testing.T) {
	home := t.TempDir()
	writeRecord(t, home, installRecordCatalog("memory-demo"))
	f := &execx.Fake{}
	f.On(argv("plugins", "check-updates", "--json"), execx.Result{Output: pluginsJSON})
	f.On(argv("skills", "check"), execx.Result{Output: skillsUpdate})
	f.On(argv("pm", "doctor"), execx.Result{Code: 1, Output: doctorFault})
	f.On(argv("pm", "update", "--check"), execx.Result{Code: 1, Output: "node 1.0.0 → 1.1.0\npython resolve failed: HTTP Error 504\n"})
	rep := New(Deps{Runner: f, Launcher: launcher, Home: home}).Probe(context.Background(), Options{Plugins: true, Skills: true, Tools: true, PMUpdate: true})
	up := rep.Plugins.Updatable()
	if len(up) != 1 || up[0].Name != "memory-demo" {
		t.Errorf("updatable = %+v", up)
	}
	if rep.Skills.Check.Updates != 1 {
		t.Errorf("skills = %+v", rep.Skills)
	}
	if len(rep.Tools.Doctor.Faults) != 1 || rep.Tools.Doctor.Faults[0].Name != "git" {
		t.Errorf("faults = %+v", rep.Tools.Doctor)
	}
	if len(rep.Tools.Updates) != 1 || rep.Tools.Updates[0].Name != "node" || !rep.Tools.UpdatesRan {
		t.Errorf("tool updates = %+v", rep.Tools)
	}
}

func TestProbeToolUpdatesOnlyForInstalledTools(t *testing.T) {
	f := &execx.Fake{}
	f.On(argv("pm", "doctor"), execx.Result{Code: 1, Output: "✓ node 1.2.3\n? opt-tool: not installed\n✗ git: realized bytes do not match recorded digest\n✗ ripgrep: not installed\n"})
	f.On(argv("pm", "update", "--check"), execx.Result{Code: 1, Output: "node 1.0.0 → 1.1.0\nopt-tool 1 → 2\ngit 2.0 → 2.1\nripgrep 1 → 2\nllamacpp-cpu 1 → 2\n"})
	rep := newSvc(f).Probe(context.Background(), Options{Tools: true, PMUpdate: true})
	var got []string
	for _, u := range rep.Tools.Updates {
		got = append(got, u.Name)
	}
	if strings.Join(got, ",") != "node,git" {
		t.Errorf("updates shown for %v, want node,git (installed or installed and failing)", got)
	}
}

func TestProbeOnlyAskedThings(t *testing.T) {
	f := &execx.Fake{}
	f.On(argv("skills", "check"), execx.Result{Output: skillsCurrent})
	rep := newSvc(f).Probe(context.Background(), Options{Skills: true})
	if rep.Plugins != nil || rep.Tools != nil || rep.Skills == nil {
		t.Errorf("report = %+v", rep)
	}
	for _, c := range f.Calls() {
		if c.Argv[1] != "skills" {
			t.Errorf("ran %v", c.Argv)
		}
	}
}

func TestProbeTimeoutAndUnreadable(t *testing.T) {
	f := &execx.Fake{}
	f.On(argv("plugins", "check-updates", "--json"), execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "timed out after 1m30s"})
	f.On(argv("skills", "check"), execx.Result{Output: "something else entirely\n"})
	f.On(argv("pm", "doctor"), execx.Result{Code: 1, Output: "Traceback (most recent call last):\n boom\n"})
	f.On(argv("pm", "update", "--check"), execx.Result{Code: 1, Output: "network request failed (HTTP Error 504)\n"})
	rep := newSvc(f).Probe(context.Background(), Options{Plugins: true, Skills: true, Tools: true, PMUpdate: true})
	if rep.Plugins.Err != "timed out" || len(rep.Plugins.Rows) != 0 {
		t.Errorf("plugins: %+v", rep.Plugins)
	}
	if rep.Skills.Err == "" {
		t.Errorf("reworded skills output read as an answer: %+v", rep.Skills)
	}
	if rep.Tools.DoctorErr == "" || rep.Tools.DoctorRanOK {
		t.Errorf("a traceback is not a healthy doctor: %+v", rep.Tools)
	}
	if rep.Tools.UpdatesErr == "" {
		t.Errorf("pm update with no version line must say it could not check: %+v", rep.Tools)
	}
}

func TestProbeDoctorExitWithoutFaultLineIsNotHealthy(t *testing.T) {
	f := &execx.Fake{}
	f.On(argv("pm", "doctor"), execx.Result{Code: 1, Output: doctorOK})
	rep := newSvc(f).Probe(context.Background(), Options{Tools: true})
	if rep.Tools.DoctorErr == "" || rep.Tools.DoctorRanOK {
		t.Errorf("exit 1 with only good lines was read as healthy: %+v", rep.Tools)
	}
}

func TestProbeOldHermesWithoutJSONFlag(t *testing.T) {
	f := &execx.Fake{}
	f.On(argv("plugins", "check-updates", "--json"), execx.Result{Code: 2, Output: "hermes plugins: error: unrecognized arguments: --json"})
	rep := newSvc(f).Probe(context.Background(), Options{Plugins: true})
	if !strings.Contains(rep.Plugins.Err, "exit 2") {
		t.Errorf("err = %q", rep.Plugins.Err)
	}
}

func TestProbeRunsSideBySide(t *testing.T) {
	// every probe is started before any of them finishes
	started := make(chan string, 4)
	release := make(chan struct{})
	f := &execx.Fake{}
	slow := func(out string) func(execx.Cmd) execx.Result {
		return func(c execx.Cmd) execx.Result {
			started <- c.Argv[1]
			<-release
			return execx.Result{Output: out}
		}
	}
	f.OnFunc(argv("plugins"), slow(pluginsCurrentJSON))
	f.OnFunc(argv("skills"), slow(skillsCurrent))
	f.OnFunc(argv("pm", "doctor"), slow(doctorOK))
	done := make(chan struct{})
	go func() {
		newSvc(f).Probe(context.Background(), Options{Plugins: true, Skills: true, Tools: true})
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d probe(s) started while the others were still running: they are not side by side", i)
		}
	}
	close(release)
	<-done
}

func TestUpdateSkills(t *testing.T) {
	cases := []struct {
		name string
		res  execx.Result
		ok   bool
		want string
	}{
		{"updated", execx.Result{Output: "Updating: demo-skill\nUpdated 1 skill(s).\n"}, true, "updated"},
		{"kept edits", execx.Result{Output: "Skipping: x\n1 skill(s) kept your local edits: x.\n"}, true, "kept"},
		{"failed", execx.Result{Code: 1, Output: "Not updated: demo-skill (see the messages above).\n"}, false, "Not updated"},
		{"not updated at rc 0", execx.Result{Output: "Not updated: demo-skill\n"}, false, "Not updated"},
		{"timeout", execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "timed out"}, false, "timed out"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &execx.Fake{}
			f.On(argv("skills", "update"), c.res)
			o := newSvc(f).UpdateSkills(context.Background())
			if o.OK != c.ok || !strings.Contains(o.Detail, c.want) {
				t.Errorf("outcome = %+v", o)
			}
			for _, call := range f.Calls() {
				for _, a := range call.Argv {
					if a == "--force" {
						t.Error("skills update ran with --force: it would overwrite local edits")
					}
				}
			}
		})
	}
}

// ---- stale ----

func TestCheckStale(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	git := "/fake/git"
	mk := func() (*execx.Fake, *Svc) {
		f := &execx.Fake{}
		s := New(Deps{Runner: f, Launcher: launcher, Git: git, Checkout: "/co", Now: func() time.Time { return now }})
		return f, s
	}
	date := func(f *execx.Fake, d string) {
		f.OnFunc([]string{git, "-C", "/co", "-c", "core.quotepath=off", "log", "-1"}, func(execx.Cmd) execx.Result { return execx.Result{Output: d + "\n"} })
	}
	count := func(f *execx.Fake, dir, n string) {
		f.OnFunc([]string{git, "-C", dir, "-c", "core.quotepath=off", "rev-list", "--count"}, func(execx.Cmd) execx.Result { return execx.Result{Output: n + "\n"} })
	}
	subjects := func(f *execx.Fake, dir, s string) {
		f.OnFunc([]string{git, "-C", dir, "-c", "core.quotepath=off", "log", "--format=%s"}, func(execx.Cmd) execx.Result { return execx.Result{Output: s} })
	}
	p := StaleParams{Tag: "v0.22.6", StaleDays: 14}

	t.Run("fresh and quiet", func(t *testing.T) {
		f, s := mk()
		date(f, "2026-10-30T09:00:00-07:00")
		count(f, "/co", "5")
		subjects(f, "/co", "fix: typo\nfeat: x\n")
		st, ok := s.CheckStale(context.Background(), p)
		if !ok || st.Show() || st.AgeDays != 2 || st.MainAhead != 5 {
			t.Errorf("stale = %+v ok=%v", st, ok)
		}
	})
	t.Run("old", func(t *testing.T) {
		f, s := mk()
		date(f, "2026-10-10T09:00:00-07:00")
		count(f, "/co", "3367")
		subjects(f, "/co", "fix: typo\n")
		st, ok := s.CheckStale(context.Background(), p)
		if !ok || !st.Show() || !st.OldEnough || st.AgeDays != 22 || st.SecurityN != 0 {
			t.Errorf("22 days must be stale at a limit of 14: %+v", st)
		}
		p30 := p
		p30.StaleDays = 30
		if st, _ = s.CheckStale(context.Background(), p30); st.Show() {
			t.Errorf("22 days must not be stale at a limit of 30: %+v", st)
		}
	})
	t.Run("security subjects", func(t *testing.T) {
		f, s := mk()
		date(f, "2026-10-30T09:00:00-07:00")
		count(f, "/co", "40")
		subjects(f, "/co", "fix(security): block x\nchore: bump\nfix: CVE-2026-1234 in y\nGHSA-abcd-efgh fix\nfix: insecure default\nfeat: Security review tool\nfix: unsecurity\n")
		st, _ := s.CheckStale(context.Background(), p)
		// "fix(security)" no: \b before security needs a non-word char before it; "(" is one -> yes
		if !st.Show() || st.SecurityN != 4 || len(st.SecuritySub) != 3 {
			t.Errorf("security count = %d (%v) show=%v", st.SecurityN, st.SecuritySub, st.Show())
		}
	})
	t.Run("mirror first", func(t *testing.T) {
		f, s := mk()
		date(f, "2026-10-30T09:00:00-07:00")
		count(f, "/mirror", "7")
		subjects(f, "/mirror", "fix: CVE-2026-9\n")
		count(f, "/co", "1")
		pm := p
		pm.Mirror = "/mirror"
		st, _ := s.CheckStale(context.Background(), pm)
		if st.Source != "mirror" || st.MainAhead != 7 || st.SecurityN != 1 {
			t.Errorf("stale = %+v", st)
		}
	})
	t.Run("unknown date shows nothing", func(t *testing.T) {
		f, s := mk()
		count(f, "/co", "3")
		if st, ok := s.CheckStale(context.Background(), p); ok || st.Show() {
			t.Errorf("stale = %+v ok=%v", st, ok)
		}
	})
	t.Run("off and bad tag", func(t *testing.T) {
		f, s := mk()
		date(f, "2026-01-01T09:00:00-07:00")
		count(f, "/co", "9")
		if st, ok := s.CheckStale(context.Background(), StaleParams{Tag: "v0.22.6", StaleDays: 0}); ok || st.Show() {
			t.Error("stale_days 0 must turn it off")
		}
		if _, ok := s.CheckStale(context.Background(), StaleParams{Tag: "v0.22.6+canary.1", StaleDays: 14}); ok {
			t.Error("a canary tag is not a release")
		}
	})
}
