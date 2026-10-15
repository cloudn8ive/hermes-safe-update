package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// packCrash is git 2.53's partial-clone fetch crash (upstream #124272).
const packCrash = "should_include_obj should only be called on existing objects"

var officialURLs = map[string]bool{
	"https://github.com/" + Repo: true,
	"git@github.com:" + Repo:     true,
}

// isOfficial compares an origin URL with the official repo, ignoring
// whitespace, a trailing "/" and a trailing ".git".
func isOfficial(url string) bool {
	url = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(url), "/"), ".git")
	return officialURLs[url]
}

// markPacksPromisor creates an empty .promisor file beside every pack that
// lacks one and returns how many it created.
func markPacksPromisor(checkout string) int {
	packs, _ := filepath.Glob(filepath.Join(checkout, ".git", "objects", "pack", "pack-*.pack"))
	n := 0
	for _, p := range packs {
		f, err := os.OpenFile(strings.TrimSuffix(p, ".pack")+".promisor", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		_ = f.Close()
		n++
	}
	return n
}

func (s *Svc) cfgGet(ctx context.Context, checkout, key string) (string, bool) {
	res, err := s.git(ctx, checkout, 30*time.Second, nil, "config", "--get", key)
	if err != nil || res.Code != 0 {
		return "", false
	}
	return strings.TrimSpace(res.Output), true
}

// Seed copies new commits, trees, changed files and tags from the mirror
// into checkout before Hermes closes. The checkout's origin stays GitHub
// (Hermes treats any other origin as a fork): the mirror is substituted with
// a `-c url.<mirror>.insteadOf=<origin>` option on these git calls only,
// never in the environment of `hermes update`. Any problem returns
// Used=false with a reason; the caller then asks GitHub as before.
func (s *Svc) Seed(ctx context.Context, checkout string) SeedResult {
	t0 := s.d.Now()
	res := s.seed(ctx, checkout)
	res.Took = s.d.Now().Sub(t0)
	if !res.Used {
		// The reason can name the mirror folder, which is the user's
		// choice and may be personal: the log keeps only the category.
		s.logf("mirror not used: " + logReason(res.Reason))
	}
	return res
}

// logReason drops the folder from a "mirror not found at <path>" reason.
func logReason(reason string) string {
	if strings.HasPrefix(reason, "mirror not found at ") {
		return "mirror not found"
	}
	return reason
}

func (s *Svc) seed(ctx context.Context, checkout string) SeedResult {
	no := func(format string, a ...any) SeedResult {
		return SeedResult{Reason: fmt.Sprintf(format, a...)}
	}
	p, why := s.seedPrep(ctx, checkout)
	if why != "" {
		return no("%s", why)
	}
	path, partial := p.path, p.partial

	target, haveTarget := s.APIMainSHA(ctx)
	have := s.head(ctx, path)
	refreshed := false
	if !haveTarget || target != have {
		if haveTarget {
			s.logf("mirror: behind GitHub, refreshing")
		} else {
			s.logf("mirror: GitHub API unavailable, refreshing")
		}
		if err := s.refreshAt(ctx, path, 300*time.Second, 120*time.Second); err != nil {
			return no("mirror refresh failed (%v)", err)
		}
		refreshed = true
		have = s.head(ctx, path)
		if haveTarget && have != target {
			return no("mirror still behind GitHub after refresh")
		}
	}
	if have == "" {
		return no("%s", errNoMainBranch.Error())
	}

	run := func(timeout time.Duration, env map[string]string, args ...string) (int, string) {
		return s.seedGit(ctx, checkout, p, timeout, env, args...)
	}

	fetchArgs := []string{"fetch", "--no-tags"}
	if partial && p.filter != "" {
		fetchArgs = append(fetchArgs, "--filter="+p.filter)
	}
	fetchArgs = append(fetchArgs, "origin", "+refs/heads/"+Branch+":refs/remotes/origin/"+Branch)
	rc, out := run(600*time.Second, nil, fetchArgs...)
	if rc != 0 && partial && strings.Contains(out, packCrash) {
		n := markPacksPromisor(checkout)
		s.logf(fmt.Sprintf("mirror: marked %d pack(s) as partial-clone packs (git 2.53 fetch crash); retrying", n))
		rc, out = run(600*time.Second, nil, fetchArgs...)
	}
	if rc != 0 {
		return no("seeding from the mirror failed: %s", tailN(out, 200))
	}
	if partial {
		// Pull trees and changed blobs from the mirror now, not lazily from
		// GitHub during the pull.
		run(600*time.Second, nil, "ls-tree", "-r", "-t", "origin/"+Branch)
		run(900*time.Second, nil, "diff", "--binary", "HEAD", "origin/"+Branch)
	}
	run(300*time.Second, nil, "fetch", "--no-tags", "origin", "refs/tags/v*:refs/tags/v*")

	// Proof that nothing is left to download: a diff that needs no network.
	pr, err := s.git(ctx, checkout, 300*time.Second, map[string]string{"GIT_NO_LAZY_FETCH": "1"},
		"diff", "--quiet", "HEAD", "origin/"+Branch)
	complete := err == nil && (pr.Code == 0 || pr.Code == 1)

	out2 := SeedResult{Used: true, Reason: "ok", Complete: complete, Refreshed: refreshed,
		Current: haveTarget && have == target}
	if cr, err := s.git(ctx, checkout, 60*time.Second, nil, "rev-list", "--count", "HEAD..origin/"+Branch); err == nil && cr.Code == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(cr.Output)); err == nil && n >= 0 {
			out2.Commits = &n
		}
	}
	return out2
}

// seedSetup is what a seed learns about the mirror and the checkout before
// it fetches anything.
type seedSetup struct {
	path    string   // the mirror
	origin  string   // the checkout's origin URL (official GitHub)
	partial bool     // the checkout is a partial clone
	filter  string   // its partial-clone filter
	rewrite []string // the one-shot `-c url.<mirror>.insteadOf=<origin>`
}

// seedPrep checks a seed can run: a mirror exists, the checkout's origin is
// the official repo (Hermes treats any other origin as a fork, and we never
// change it). why is the reason when it cannot.
func (s *Svc) seedPrep(ctx context.Context, checkout string) (p seedSetup, why string) {
	p.path = s.mirrorPath()
	if p.path == "" {
		if st := s.loadMachine(); st.Mirror != nil && st.Mirror.Path != "" {
			return p, "mirror not found at " + st.Mirror.Path
		}
		return p, "no mirror set up"
	}
	origin, ok := s.cfgGet(ctx, checkout, "remote.origin.url")
	if !ok || !isOfficial(origin) {
		return p, "checkout origin is not the official repo"
	}
	p.origin = origin
	promisor, _ := s.cfgGet(ctx, checkout, "remote.origin.promisor")
	p.partial = promisor == "true"
	if p.partial {
		p.filter, _ = s.cfgGet(ctx, checkout, "remote.origin.partialclonefilter")
	}
	p.rewrite = []string{"-c", "url." + filepath.ToSlash(p.path) + ".insteadOf=" + origin}
	return p, ""
}

// seedGit runs git in the checkout with the mirror substituted for origin on
// this call only; it returns the exit code and output.
func (s *Svc) seedGit(ctx context.Context, checkout string, p seedSetup, timeout time.Duration, env map[string]string, args ...string) (int, string) {
	r, err := s.git(ctx, checkout, timeout, env, append(append([]string{}, p.rewrite...), args...)...)
	if err != nil {
		return 1, err.Error()
	}
	return r.Code, r.Output
}
