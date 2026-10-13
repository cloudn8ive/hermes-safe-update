package mirror

import (
	"context"
	"strings"
	"time"
)

// Short limits: this is a courtesy look after a finished update. It must
// never hold the summary back for long.
const (
	newerAPITimeout = 4 * time.Second
	newerGitTimeout = 5 * time.Second
)

// NewerThan reports whether main moved past head, a full commit sha. It
// reads only: the GitHub API's compare, then `git ls-remote` against the
// official URL. It never uses the local mirror (which may be missing or
// stale) and never writes to the checkout, its index or its refs.
// ok=false means "could not tell"; the caller leaves the answer out.
func (s *Svc) NewerThan(ctx context.Context, checkout, head string) (Newer, bool) {
	if !shaRe.MatchString(head) {
		return Newer{}, false
	}
	if c := s.apiCompare(ctx, head, newerAPITimeout); c != nil {
		n := c.Commits
		return Newer{Ahead: n > 0, Commits: &n}, true
	}
	res, err := s.git(ctx, "", newerGitTimeout, nil, "ls-remote", s.d.CloneURL, "refs/heads/"+Branch)
	if err != nil || res.TimedOut || res.Code != 0 {
		return Newer{}, false
	}
	var remote string
	for _, line := range strings.Split(res.Output, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "refs/heads/"+Branch && shaRe.MatchString(f[0]) {
			remote = f[0]
			break
		}
	}
	if remote == "" {
		return Newer{}, false
	}
	if remote == head {
		return Newer{}, true
	}
	if checkout != "" {
		// A tip we already have inside our own history is not "newer"
		// (rc 0). rc 1 = not an ancestor; an object we do not have at all
		// (git exits 128) is something we never installed, so newer.
		r, err := s.git(ctx, checkout, newerGitTimeout, nil, "merge-base", "--is-ancestor", remote, head)
		if err == nil && !r.TimedOut && r.Code == 0 {
			return Newer{}, true
		}
	}
	return Newer{Ahead: true}, true
}
