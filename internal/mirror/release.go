package mirror

// Release channels: what the mirror does for a stable update, whose target
// is a release tag (vX.Y.Z) and not the tip of main.
//
// A release is never chosen here. Hermes names it (tag and commit prefix, in
// its own `--check` output) and this file only looks it up; tags are never
// sorted, because CalVer (v2026.9.24), canary (v0.22.0+canary.<ts>) and rc
// tags sit beside the real ones.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// stableTagRe is Hermes' STABLE_TAG_RE, the same pattern as
// hermes.StableTagRe (this package cannot import that one; a test in
// internal/update pins the two together through StableTag).
var stableTagRe = regexp.MustCompile(`^v(?:0|[1-9]\d{0,2})\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$`)

// hexPrefixRe is a commit prefix as Hermes prints it (at most 12 digits).
var hexPrefixRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// StableTag reports whether s is a strict stable release tag, vX.Y.Z.
func StableTag(s string) bool { return stableTagRe.MatchString(s) }

// SeedRelease puts the commit of release tag into checkout from the mirror,
// before Hermes closes, the way Seed does for main:
//
//  1. the mirror must have refs/tags/<tag> at the commit Hermes named
//     (sha12); if it does not, it is refreshed first, and if it still does
//     not, the seed is not used;
//  2. the tag is fetched into the checkout through the one-shot
//     `-c url.<mirror>.insteadOf=<origin>` (the checkout's origin is never
//     changed; Hermes treats any other origin as a fork);
//  3. on a partial clone the trees and the changed blobs are pulled from the
//     mirror now, not lazily from GitHub during the pull;
//  4. proof: `git diff --quiet HEAD <tag>` with lazy fetching off needs no
//     network, so Complete says nothing is left to download;
//  5. Commits counts HEAD..<tag>.
//
// The main seed (origin/main, its count) does not run on a release channel.
func (s *Svc) SeedRelease(ctx context.Context, checkout, tag, sha12 string) ReleaseSeed {
	t0 := s.d.Now()
	res := s.seedRelease(ctx, checkout, tag, sha12)
	res.Took = s.d.Now().Sub(t0)
	if !res.Used {
		s.logf("mirror not used for the release: " + logReason(res.Reason))
	}
	return res
}

func (s *Svc) seedRelease(ctx context.Context, checkout, tag, sha12 string) ReleaseSeed {
	no := func(format string, a ...any) ReleaseSeed {
		return ReleaseSeed{SeedResult: SeedResult{Reason: fmt.Sprintf(format, a...)}}
	}
	if !stableTagRe.MatchString(tag) {
		return no("%q is not a release tag", tag)
	}
	if sha12 != "" && !hexPrefixRe.MatchString(sha12) {
		return no("odd release commit %q", sha12)
	}
	p, why := s.seedPrep(ctx, checkout)
	if why != "" {
		return no("%s", why)
	}
	ref := "refs/tags/" + tag

	refreshed := false
	if !s.mirrorHasRelease(ctx, p.path, ref, sha12) {
		s.logf("mirror: release " + tag + " not there yet, refreshing")
		if err := s.refreshAt(ctx, p.path, 300*time.Second, 120*time.Second); err != nil {
			return no("mirror refresh failed (%v)", err)
		}
		refreshed = true
		if !s.mirrorHasRelease(ctx, p.path, ref, sha12) {
			return no("the mirror does not have release %s at the commit Hermes named", tag)
		}
	}

	run := func(timeout time.Duration, env map[string]string, args ...string) (int, string) {
		return s.seedGit(ctx, checkout, p, timeout, env, args...)
	}
	fetchArgs := []string{"fetch", "--no-tags"}
	if p.partial && p.filter != "" {
		fetchArgs = append(fetchArgs, "--filter="+p.filter)
	}
	fetchArgs = append(fetchArgs, "origin", "+"+ref+":"+ref)
	rc, out := run(600*time.Second, nil, fetchArgs...)
	if rc != 0 && p.partial && strings.Contains(out, packCrash) {
		n := markPacksPromisor(checkout)
		s.logf(fmt.Sprintf("mirror: marked %d pack(s) as partial-clone packs (git 2.53 fetch crash); retrying", n))
		rc, out = run(600*time.Second, nil, fetchArgs...)
	}
	if rc != 0 {
		return no("seeding the release from the mirror failed: %s", tailN(out, 200))
	}
	if p.partial {
		run(600*time.Second, nil, "ls-tree", "-r", "-t", ref)
		run(900*time.Second, nil, "diff", "--binary", "HEAD", ref, "--")
	}

	pr, err := s.git(ctx, checkout, 300*time.Second, map[string]string{"GIT_NO_LAZY_FETCH": "1"},
		"diff", "--quiet", "HEAD", ref, "--")
	complete := err == nil && (pr.Code == 0 || pr.Code == 1)

	target := s.commitOf(ctx, checkout, ref)
	if target == "" || (sha12 != "" && !strings.HasPrefix(target, sha12)) {
		return no("the fetched %s is not the commit Hermes named", tag)
	}
	out2 := ReleaseSeed{SeedResult: SeedResult{Used: true, Reason: "ok", Complete: complete, Refreshed: refreshed}, TargetSHA: target}
	if cr, err := s.git(ctx, checkout, 60*time.Second, nil, "rev-list", "--count", "HEAD.."+ref, "--"); err == nil && cr.Code == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(cr.Output)); err == nil && n >= 0 {
			out2.Commits = &n
		}
	}
	return out2
}

// commitOf is the full sha of the commit ref (validated by the caller) names
// in the repository at dir, or "".
func (s *Svc) commitOf(ctx context.Context, dir, ref string) string {
	res, err := s.git(ctx, dir, 30*time.Second, map[string]string{"GIT_NO_LAZY_FETCH": "1"}, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || res.TimedOut || res.Code != 0 {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(res.Output), "\n", 2)[0])
	if shaRe.MatchString(line) {
		return line
	}
	return ""
}

// mirrorHasRelease: the mirror has ref, and (when Hermes named a commit)
// it points at that commit.
func (s *Svc) mirrorHasRelease(ctx context.Context, mirror, ref, sha12 string) bool {
	full := s.commitOf(ctx, mirror, ref)
	return full != "" && (sha12 == "" || strings.HasPrefix(full, sha12))
}

// releaseTagsBehind reports whether GitHub has a stable release tag the
// mirror lacks (or has at another object): the cron job refreshes then even
// though main did not move. Only strict vX.Y.Z tags count: a new canary,
// CalVer or rc tag is no reason to fetch. One `git ls-remote --tags`; when
// that or the mirror cannot answer the answer is "no" (the next run asks
// again; a refresh that cannot reach GitHub would only fail noisily).
func (s *Svc) releaseTagsBehind(ctx context.Context, mirror string) bool {
	remote, ok := s.stableTags(ctx, "", "ls-remote", "--tags", s.d.CloneURL, "refs/tags/v*")
	if !ok {
		s.logf("mirror: could not list GitHub's release tags; not checking them this time")
		return false
	}
	local, ok := s.stableTags(ctx, mirror, "for-each-ref", "--format=%(objectname) %(refname)", "refs/tags/")
	if !ok {
		return false
	}
	for tag, obj := range remote {
		if local[tag] != obj {
			return true
		}
	}
	return false
}

// stableTags runs a git command whose lines read "<object> refs/tags/<name>"
// and returns the strict stable tags in it, name -> object. Peeled lines
// ("<name>^{}") are not tags and are skipped.
func (s *Svc) stableTags(ctx context.Context, dir string, args ...string) (map[string]string, bool) {
	res, err := s.git(ctx, dir, 60*time.Second, nil, args...)
	if err != nil || res.TimedOut || res.Code != 0 {
		return nil, false
	}
	tags := map[string]string{}
	for _, line := range strings.Split(res.Output, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !shaRe.MatchString(f[0]) {
			continue
		}
		name, ok := strings.CutPrefix(f[1], "refs/tags/")
		if ok && stableTagRe.MatchString(name) {
			tags[name] = f[0]
		}
	}
	return tags, true
}

// NewerRelease says whether a release newer than head (a full sha) was
// published: GitHub's releases/latest (the same pick Hermes' stable channel
// makes: strict vX.Y.Z, not a draft, not a prerelease), then compare
// head...<tag>, and only status "ahead" counts. API only, a few seconds
// (newerAPITimeout each, the caller's context bounds the pair); ok=false when
// it cannot tell, and nothing is written anywhere.
func (s *Svc) NewerRelease(ctx context.Context, head string) (NewerRelease, bool) {
	if !shaRe.MatchString(head) {
		return NewerRelease{}, false
	}
	body, ok := s.apiGet(ctx, "/repos/"+Repo+"/releases/latest", "application/vnd.github+json", newerAPITimeout, 1<<20)
	if !ok {
		return NewerRelease{}, false
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(body, &rel) != nil || rel.Draft || rel.Prerelease || !stableTagRe.MatchString(rel.Tag) {
		return NewerRelease{}, false
	}
	c := s.compareRefs(ctx, head, rel.Tag, newerAPITimeout)
	if c == nil {
		return NewerRelease{}, false
	}
	n := NewerRelease{Tag: rel.Tag}
	if c.Status == "ahead" && c.Commits > 0 {
		n.Ahead = true
		n.Commits = &c.Commits
	}
	return n, true
}
