package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// relWorld is a real upstream with a release tag on a main commit the mirror
// already has, so the tag moves no branch:
//
//	c1  the checkout's HEAD (and origin/main there)
//	c2  tagged v1.2.3 (and carrying decoy tags) AFTER the mirror was cloned
//	c3  main's tip; the mirror is at c3, without any tag
type relWorld struct {
	e                *env
	upstream, work   string
	mirror, checkout string
	c1, c2, c3       string
}

func newRelWorld(t *testing.T) *relWorld {
	t.Helper()
	w := &relWorld{e: newEnv(t)}
	w.upstream = newBare(t, "up")
	w.work = newWork(t, w.upstream)
	w.c1 = commit(t, w.work, "a.txt", "1")
	g(t, w.work, "push", "origin", "main")
	w.checkout = checkoutOf(t, w.upstream)
	w.c2 = commit(t, w.work, "b.txt", "2")
	w.c3 = commit(t, w.work, "c.txt", "3")
	g(t, w.work, "push", "origin", "main")
	w.mirror = mirrorOf(t, w.e, w.upstream)
	return w
}

// tagUpstream creates tags on upstream without moving main.
func (w *relWorld) tagUpstream(t *testing.T, at string, tags ...string) {
	t.Helper()
	for _, tag := range tags {
		g(t, w.work, "tag", tag, at)
	}
	g(t, w.work, "push", "origin", "--tags")
}

func (w *relWorld) deps(t *testing.T) (Deps, *recRunner) {
	d := w.e.realDeps()
	d.CloneURL = w.upstream
	rec := &recRunner{inner: execx.New()}
	d.Runner = rec
	return d, rec
}

func (r *recRunner) joined() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, strings.Join(c.Argv, " "))
	}
	return out
}

func anyContains(lines []string, subs ...string) bool {
	for _, l := range lines {
		ok := true
		for _, s := range subs {
			if !strings.Contains(l, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestSeedReleaseFetchesTagAndProvesComplete(t *testing.T) {
	ctx := context.Background()
	w := newRelWorld(t)
	w.tagUpstream(t, w.c2, "v1.2.3", "v2026.9.24", "v1.2.3+canary.20261001T000000Z", "rc.1-v1.2.4")
	d, rec := w.deps(t)

	res := New(d).SeedRelease(ctx, w.checkout, "v1.2.3", w.c2[:12])
	if !res.Used || res.Reason != "ok" {
		t.Fatalf("SeedRelease = %+v (logs %v)", res, w.e.logs)
	}
	if res.TargetSHA != w.c2 {
		t.Errorf("TargetSHA = %s, want %s", res.TargetSHA, w.c2)
	}
	if res.Commits == nil || *res.Commits != 1 {
		t.Errorf("Commits = %v, want 1 (c1..v1.2.3)", res.Commits)
	}
	if !res.Complete {
		t.Error("Complete = false: after seeding nothing should be left to download")
	}
	if !res.Refreshed {
		t.Error("the mirror had no tag, so it must have been refreshed")
	}
	// the release commit is in the checkout, under its tag
	if got := g(t, w.checkout, "rev-parse", "refs/tags/v1.2.3^{commit}"); got != w.c2 {
		t.Errorf("checkout's v1.2.3 = %s, want %s", got, w.c2)
	}
	// only the named tag came: no decoy, nothing sorted into a "latest"
	if got := g(t, w.checkout, "tag", "-l"); got != "v1.2.3" {
		t.Errorf("tags in the checkout: %q", got)
	}
	// the main seed did not run: origin/main and the count are main's business
	if got := g(t, w.checkout, "rev-parse", "refs/remotes/origin/main"); got != w.c1 {
		t.Errorf("origin/main moved to %s: the main seed ran on a release", got)
	}
	lines := rec.joined()
	if anyContains(lines, "refs/heads/main:refs/remotes/origin/main") || anyContains(lines, "HEAD..origin/main") {
		t.Errorf("main seed commands ran: %v", lines)
	}
	// origin stays GitHub; the rewrite is a -c option of our own fetch only
	if got := g(t, w.checkout, "config", "--get", "remote.origin.url"); got != officialURL {
		t.Errorf("origin URL changed to %q", got)
	}
	if !anyContains(lines, "fetch", "url."+filepath.ToSlash(w.mirror)+".insteadOf="+officialURL, "+refs/tags/v1.2.3:refs/tags/v1.2.3") {
		t.Errorf("the tag was not fetched through the mirror rewrite: %v", lines)
	}
	cfg, _ := os.ReadFile(filepath.Join(w.checkout, ".git", "config"))
	if strings.Contains(strings.ToLower(string(cfg)), "insteadof") {
		t.Errorf("insteadOf written to the checkout config:\n%s", cfg)
	}
	// the proof runs without lazy fetching, so it cannot have gone to the network
	rec.mu.Lock()
	proved := false
	for _, c := range rec.calls {
		if strings.Contains(strings.Join(c.Argv, " "), "diff --quiet HEAD refs/tags/v1.2.3") && c.Env["GIT_NO_LAZY_FETCH"] == "1" {
			proved = true
		}
	}
	rec.mu.Unlock()
	if !proved {
		t.Errorf("no GIT_NO_LAZY_FETCH proof: %v", lines)
	}
}

func TestSeedReleaseRefreshesWhenTagMissing(t *testing.T) {
	ctx := context.Background()
	t.Run("tag only upstream: the mirror is refreshed first", func(t *testing.T) {
		w := newRelWorld(t)
		w.tagUpstream(t, w.c2, "v1.2.3")
		if got := g(t, w.mirror, "tag", "-l", "v1.2.3"); got != "" {
			t.Fatalf("setup: the mirror already has %q", got)
		}
		d, rec := w.deps(t)
		res := New(d).SeedRelease(ctx, w.checkout, "v1.2.3", w.c2[:12])
		if !res.Used || !res.Refreshed {
			t.Fatalf("SeedRelease = %+v", res)
		}
		if got := g(t, w.mirror, "rev-parse", "refs/tags/v1.2.3^{commit}"); got != w.c2 {
			t.Errorf("mirror's v1.2.3 = %s", got)
		}
		if !anyContains(rec.joined(), "fetch", "--prune") {
			t.Errorf("no mirror refresh ran: %v", rec.joined())
		}
	})
	t.Run("tag already in the mirror: no refresh", func(t *testing.T) {
		w := newRelWorld(t)
		w.tagUpstream(t, w.c2, "v1.2.3")
		g(t, w.mirror, "fetch", "--no-tags", "origin", "+refs/tags/v1.2.3:refs/tags/v1.2.3")
		d, rec := w.deps(t)
		res := New(d).SeedRelease(ctx, w.checkout, "v1.2.3", w.c2[:12])
		if !res.Used || res.Refreshed {
			t.Fatalf("SeedRelease = %+v", res)
		}
		if anyContains(rec.joined(), "--prune") {
			t.Errorf("the mirror was refreshed although it had the tag: %v", rec.joined())
		}
	})
	t.Run("the tag is at another commit than Hermes named: not used", func(t *testing.T) {
		w := newRelWorld(t)
		w.tagUpstream(t, w.c2, "v1.2.3")
		d, _ := w.deps(t)
		res := New(d).SeedRelease(ctx, w.checkout, "v1.2.3", w.c3[:12])
		if res.Used || !strings.Contains(res.Reason, "does not have release v1.2.3") {
			t.Fatalf("SeedRelease = %+v", res)
		}
		if got := g(t, w.checkout, "tag", "-l"); got != "" {
			t.Errorf("a tag reached the checkout: %q", got)
		}
	})
	t.Run("no such tag anywhere: not used", func(t *testing.T) {
		w := newRelWorld(t)
		d, _ := w.deps(t)
		res := New(d).SeedRelease(ctx, w.checkout, "v9.9.9", "")
		if res.Used || !strings.Contains(res.Reason, "does not have release v9.9.9") {
			t.Fatalf("SeedRelease = %+v", res)
		}
	})
}

// Anything that is not a strict tag or a hex prefix is refused before git
// runs, and a fork's checkout is never seeded.
func TestSeedReleaseRefusals(t *testing.T) {
	ctx := context.Background()
	w := newRelWorld(t)
	w.tagUpstream(t, w.c2, "v1.2.3")
	for _, c := range []struct{ tag, sha12 string }{
		{"", ""}, {"main", ""}, {"--all", ""}, {"v2026.9.24", ""}, {"v1.2.3+canary.20261001T000000Z", ""},
		{"rc.1-v1.2.4", ""}, {"v1.2", ""}, {"refs/tags/v1.2.3", ""}, {"v1.2.3", "--all"}, {"v1.2.3", "abc"}, {"v1.2.3", "ZZZZZZZZ"},
	} {
		d, rec := w.deps(t)
		res := New(d).SeedRelease(ctx, w.checkout, c.tag, c.sha12)
		if res.Used {
			t.Errorf("SeedRelease(%q, %q) was used: %+v", c.tag, c.sha12, res)
		}
		if n := len(rec.joined()); n != 0 {
			t.Errorf("SeedRelease(%q, %q) ran git: %v", c.tag, c.sha12, rec.joined())
		}
	}
	g(t, w.checkout, "remote", "set-url", "origin", "https://example.invalid/fork.git")
	d, _ := w.deps(t)
	if res := New(d).SeedRelease(ctx, w.checkout, "v1.2.3", w.c2[:12]); res.Used || res.Reason != "checkout origin is not the official repo" {
		t.Errorf("fork checkout: %+v", res)
	}
	if res := New(w.e.deps()).SeedRelease(ctx, t.TempDir(), "v1.2.3", ""); res.Used {
		t.Errorf("no mirror: %+v", res)
	}
}

// A partial clone takes the same steps as for main, against the tag.
func TestSeedReleasePartialClone(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	m := filepath.Join(t.TempDir(), "m.git")
	makeBareShape(t, m)
	saveMachine(t, e, m)
	sha := strings.Repeat("ab", 20)
	var argvs []string
	fetches := 0
	e.runner.OnFunc([]string{"git-under-test"}, func(c execx.Cmd) execx.Result {
		j := strings.Join(c.Argv, " ")
		argvs = append(argvs, j)
		switch {
		case strings.Contains(j, "config --get remote.origin.url"):
			return execx.Result{Output: officialURL + "\n"}
		case strings.Contains(j, "config --get remote.origin.promisor"):
			return execx.Result{Output: "true\n"}
		case strings.Contains(j, "config --get remote.origin.partialclonefilter"):
			return execx.Result{Output: "blob:none\n"}
		case strings.Contains(j, "rev-parse --verify --quiet refs/tags/v1.2.3^{commit}"):
			return execx.Result{Output: sha + "\n"}
		case strings.Contains(j, "+refs/tags/v1.2.3:refs/tags/v1.2.3"):
			fetches++
			if fetches == 1 {
				return execx.Result{Code: 128, Output: "fatal: " + packCrash}
			}
			return execx.Result{}
		case strings.Contains(j, "rev-list --count"):
			return execx.Result{Output: "7\n"}
		}
		return execx.Result{}
	})
	co := t.TempDir()
	packDir := filepath.Join(co, ".git", "objects", "pack")
	_ = os.MkdirAll(packDir, 0o755)
	_ = os.WriteFile(filepath.Join(packDir, "pack-aaa.pack"), []byte("x"), 0o644)

	res := New(e.deps()).SeedRelease(ctx, co, "v1.2.3", sha[:12])
	if !res.Used || res.TargetSHA != sha || res.Commits == nil || *res.Commits != 7 || !res.Complete || res.Refreshed {
		t.Fatalf("SeedRelease = %+v logs=%v", res, e.logs)
	}
	if fetches != 2 || !isFile(filepath.Join(packDir, "pack-aaa.promisor")) {
		t.Errorf("crash retry: fetches=%d", fetches)
	}
	joined := strings.Join(argvs, "\n")
	for _, want := range []string{"--filter=blob:none", "ls-tree -r -t refs/tags/v1.2.3", "diff --binary HEAD refs/tags/v1.2.3 --", "diff --quiet HEAD refs/tags/v1.2.3 --", "HEAD..refs/tags/v1.2.3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("partial path missing %q:\n%s", want, joined)
		}
	}
}

// The cron job: main's tip did not move, but a release tag was pushed onto a
// commit the mirror already has.
func TestCronRefreshFetchesTagWhenMainUnchanged(t *testing.T) {
	ctx := context.Background()
	w := newRelWorld(t)
	api := newAPI(t)
	api.Main = w.c3 // GitHub's main is exactly what the mirror has
	w.tagUpstream(t, w.c2, "v1.2.3")
	d, rec := w.deps(t)
	d.APIBase = api.URL
	if code := New(d).CronRefresh(ctx); code != 0 {
		t.Fatalf("code = %d (logs %v)", code, w.e.logs)
	}
	if got := g(t, w.mirror, "rev-parse", "refs/tags/v1.2.3^{commit}"); got != w.c2 {
		t.Fatalf("the new release tag never reached the mirror: %q", got)
	}
	if !anyContains(rec.joined(), "ls-remote", "--tags") {
		t.Errorf("the tags were not compared: %v", rec.joined())
	}
}

func TestCronRefreshLeavesAnUpToDateMirrorAlone(t *testing.T) {
	ctx := context.Background()
	w := newRelWorld(t)
	api := newAPI(t)
	api.Main = w.c3
	w.tagUpstream(t, w.c2, "v1.2.3")
	g(t, w.mirror, "fetch", "--no-tags", "origin", "+refs/tags/v1.2.3:refs/tags/v1.2.3")

	// tags that are not releases are no reason to fetch
	w.tagUpstream(t, w.c3, "v2026.9.24", "v1.2.3+canary.20261001T000000Z", "rc.1-v1.2.4", "v1.2.4-rc.1")
	d, rec := w.deps(t)
	d.APIBase = api.URL
	if code := New(d).CronRefresh(ctx); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if anyContains(rec.joined(), "fetch") {
		t.Errorf("fetched although only non-release tags appeared: %v", rec.joined())
	}
	if got := g(t, w.mirror, "tag", "-l"); got != "v1.2.3" {
		t.Errorf("mirror tags: %q", got)
	}
}

// If GitHub's tags cannot be listed the cron job stays quiet and does not
// refresh on a guess.
func TestCronRefreshWhenTagsCannotBeListed(t *testing.T) {
	ctx := context.Background()
	w := newRelWorld(t)
	api := newAPI(t)
	api.Main = w.c3
	d, rec := w.deps(t)
	d.APIBase = api.URL
	d.CloneURL = filepath.Join(t.TempDir(), "nowhere.git")
	if code := New(d).CronRefresh(ctx); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if anyContains(rec.joined(), "fetch") {
		t.Errorf("refreshed on a guess: %v", rec.joined())
	}
}

// The mirror carries every v* tag (CalVer and canary ones too, harmless) but
// not rc tags; a release is only ever one of the strict vX.Y.Z ones, which
// are told apart by shape, never by sorting.
func TestRefreshIgnoresRcAndKeepsStableTags(t *testing.T) {
	ctx := context.Background()
	w := newRelWorld(t)
	w.tagUpstream(t, w.c2, "v1.2.3", "v2026.9.24", "v1.2.3+canary.20261001T000000Z", "rc.1-v1.2.4")
	if err := New(w.e.realDeps()).Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(g(t, w.mirror, "tag", "-l"))
	has := map[string]bool{}
	for _, tag := range got {
		has[tag] = true
	}
	if !has["v1.2.3"] || has["rc.1-v1.2.4"] {
		t.Errorf("mirror tags %v: want the stable tag and no rc tag", got)
	}
	// the sorted-last tag is CalVer; it must not be what a release lookup finds
	stable, ok := New(w.e.realDeps()).stableTags(ctx, w.mirror, "for-each-ref", "--format=%(objectname) %(refname)", "refs/tags/")
	if !ok || len(stable) != 1 || stable["v1.2.3"] != w.c2 {
		t.Errorf("stable tags of the mirror = %v (ok=%v), want only v1.2.3", stable, ok)
	}
	for _, name := range []string{"v1.2.3", "v0.22.0", "v999.0.0", "v0.0.0"} {
		if !StableTag(name) {
			t.Errorf("%s should be a stable tag", name)
		}
	}
	for _, name := range []string{"v2026.9.24", "v1.2.3+canary.20261001T000000Z", "rc.1-v1.2.4", "v1.2.4-rc.1", "v1.2", "1.2.3", "v01.2.3", "v1000.0.0", ""} {
		if StableTag(name) {
			t.Errorf("%q must not be a stable tag", name)
		}
	}
}

func TestNewerRelease(t *testing.T) {
	ctx := context.Background()
	head := strings.Repeat("d4", 20)
	const latest = `{"tag_name":"v1.2.4","draft":false,"prerelease":false}`
	cases := []struct {
		name, latest, compare string
		wantOK, wantAhead     bool
		wantCommits           int
		requests              int // how many API requests it takes to get there
	}{
		{"ahead", latest, `{"status":"ahead","ahead_by":5,"files":[]}`, true, true, 5, 2},
		{"identical", latest, `{"status":"identical","ahead_by":0,"files":[]}`, true, false, 0, 2},
		{"behind", latest, `{"status":"behind","ahead_by":0,"behind_by":3}`, true, false, 0, 2},
		{"diverged", latest, `{"status":"diverged","ahead_by":2,"behind_by":1}`, true, false, 0, 2},
		{"unknown status", latest, `{"status":"mystery","ahead_by":2}`, false, false, 0, 2},
		{"no count", latest, `{"status":"ahead"}`, false, false, 0, 2},
		{"releases/latest is down", "", `{"status":"ahead","ahead_by":5}`, false, false, 0, 1},
		{"compare is down", latest, "", false, false, 0, 2},
		{"a draft", `{"tag_name":"v1.2.4","draft":true,"prerelease":false}`, `{"status":"ahead","ahead_by":5}`, false, false, 0, 1},
		{"a prerelease", `{"tag_name":"v1.2.4","draft":false,"prerelease":true}`, `{"status":"ahead","ahead_by":5}`, false, false, 0, 1},
		{"a canary tag", `{"tag_name":"v1.2.4+canary.20261001T000000Z","draft":false,"prerelease":false}`, `{"status":"ahead","ahead_by":5}`, false, false, 0, 1},
		{"a CalVer tag", `{"tag_name":"v2026.9.24","draft":false,"prerelease":false}`, `{"status":"ahead","ahead_by":5}`, false, false, 0, 1},
		{"not JSON", "<html>rate limited</html>", `{"status":"ahead","ahead_by":5}`, false, false, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			api := newAPI(t)
			api.Latest, api.Compare = c.latest, c.compare
			d := e.deps()
			d.APIBase = api.URL
			got, ok := New(d).NewerRelease(ctx, head)
			if ok != c.wantOK || got.Ahead != c.wantAhead {
				t.Fatalf("NewerRelease = %+v ok=%v, want ok=%v ahead=%v", got, ok, c.wantOK, c.wantAhead)
			}
			if len(api.Paths) != c.requests {
				t.Errorf("requests: %v, want %d (no compare for a release that does not count)", api.Paths, c.requests)
			}
			if c.wantAhead {
				if got.Tag != "v1.2.4" || got.Commits == nil || *got.Commits != c.wantCommits {
					t.Errorf("got %+v", got)
				}
				// the pair of requests: the latest release, then head...that tag
				if !strings.HasPrefix(api.Paths[0], "/repos/"+Repo+"/releases/latest") ||
					!strings.HasPrefix(api.Paths[1], "/repos/"+Repo+"/compare/"+head+"...v1.2.4") {
					t.Errorf("requests: %v", api.Paths)
				}
			}
		})
	}
	t.Run("no API configured", func(t *testing.T) {
		if _, ok := New(newEnv(t).deps()).NewerRelease(ctx, head); ok {
			t.Error("answered without an API")
		}
	})
	t.Run("odd head", func(t *testing.T) {
		for _, h := range []string{"", "abc", "--all", strings.Repeat("D4", 20)} {
			api := newAPI(t)
			d := newEnv(t).deps()
			d.APIBase = api.URL
			if _, ok := New(d).NewerRelease(ctx, h); ok || len(api.Paths) != 0 {
				t.Errorf("head %q: ok=%v requests=%v", h, ok, api.Paths)
			}
		}
	})
}

func TestAPICompareTo(t *testing.T) {
	ctx := context.Background()
	head := strings.Repeat("d4", 20)
	files := `{"filename":"package.json"},{"filename":"README.md"}`
	t.Run("ahead lists the files", func(t *testing.T) {
		api := newAPI(t)
		api.Compare = `{"status":"ahead","ahead_by":7,"files":[` + files + `]}`
		d := newEnv(t).deps()
		d.APIBase = api.URL
		c := New(d).APICompareTo(ctx, head, "v1.2.3")
		if c == nil || c.Commits != 7 || c.Status != "ahead" || len(c.Files) != 2 || !c.FilesComplete {
			t.Fatalf("got %+v", c)
		}
		if len(api.Paths) != 1 || !strings.HasPrefix(api.Paths[0], "/repos/"+Repo+"/compare/"+head+"...v1.2.3?") {
			t.Errorf("requests: %v", api.Paths)
		}
	})
	for _, st := range []string{"behind", "diverged"} {
		t.Run(st+" has a count but no file list", func(t *testing.T) {
			api := newAPI(t)
			api.Compare = `{"status":"` + st + `","ahead_by":3,"files":[` + files + `]}`
			d := newEnv(t).deps()
			d.APIBase = api.URL
			c := New(d).APICompareTo(ctx, head, strings.Repeat("a1", 20))
			if c == nil || c.Status != st || c.Commits != 3 || len(c.Files) != 0 || c.FilesComplete {
				t.Fatalf("got %+v: the files of a %s compare are not the change to the target", c, st)
			}
		})
	}
	t.Run("refused targets make no request", func(t *testing.T) {
		api := newAPI(t)
		api.Compare = `{"status":"ahead","ahead_by":7,"files":[]}`
		d := newEnv(t).deps()
		d.APIBase = api.URL
		for _, target := range []string{"", "main", "v1.2", "v2026.9.24", "v1.2.3+canary.1", "../x", "v1.2.3/../../x", "abc", "--all"} {
			if c := New(d).APICompareTo(ctx, head, target); c != nil {
				t.Errorf("target %q answered %+v", target, c)
			}
		}
		if c := New(d).APICompareTo(ctx, "abc", "v1.2.3"); c != nil {
			t.Errorf("odd base answered %+v", c)
		}
		if len(api.Paths) != 0 {
			t.Errorf("requests: %v", api.Paths)
		}
	})
	t.Run("main's compare still wants ahead or identical", func(t *testing.T) {
		api := newAPI(t)
		api.Compare = `{"status":"behind","ahead_by":0,"files":[]}`
		d := newEnv(t).deps()
		d.APIBase = api.URL
		if c := New(d).APICompare(ctx, head); c != nil {
			t.Errorf("APICompare took a behind answer: %+v", c)
		}
	})
}
