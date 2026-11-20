package extras

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// SecurityRe picks the commit subjects that count as security fixes waiting
// on main (the v0.7.0 stale-stable row).
var SecurityRe = regexp.MustCompile(`(?i)\b(cve-\d|ghsa-|security)`)

// maxScan bounds the subjects looked at (a stable release can be thousands of
// commits behind main).
const maxScan = 20000

// Stale is what is known about the latest stable release.
type Stale struct {
	Tag         string
	Released    time.Time // zero = unknown
	AgeDays     int
	OldEnough   bool // Released is older than the limit
	MainAhead   int  // commits on main since the release (0 = unknown or none)
	AheadKnown  bool
	SecurityN   int      // those whose subject matches SecurityRe
	SecuritySub []string // up to 3 of them, for the log
	// Source names where the main commits were read: "mirror", "checkout" or "".
	Source string
}

// Show is true when the row is due.
func (s Stale) Show() bool { return s.OldEnough || s.SecurityN > 0 }

// StaleParams are the inputs of CheckStale.
type StaleParams struct {
	Tag       string // strict release tag, vX.Y.Z
	StaleDays int    // 0 = off
	Mirror    string // path of the local mirror ("" = none)
}

var tagRe = regexp.MustCompile(`^v(?:0|[1-9]\d{0,2})\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$`)

func (s *Svc) git(ctx context.Context, dir string, args ...string) (execx.Result, bool) {
	if s.d.Git == "" || dir == "" {
		return execx.Result{}, false
	}
	argv := append([]string{s.d.Git, "-C", dir, "-c", "core.quotepath=off"}, args...)
	res, err := s.d.Runner.Run(ctx, execx.Cmd{Argv: argv, Timeout: 60 * time.Second, Env: map[string]string{"GIT_NO_LAZY_FETCH": "1", "GIT_TERMINAL_PROMPT": "0"}})
	return res, err == nil && !res.TimedOut && res.Code == 0
}

// CheckStale reads the release date and the security-looking subjects since
// the release from git only (the checkout has the tag; the mirror, when
// there is one, has an up-to-date main; otherwise the checkout's origin/main
// as of its last fetch). It never fetches and never asks GitHub. ok=false
// when the tag's date is not known (then there is nothing to show).
func (s *Svc) CheckStale(ctx context.Context, p StaleParams) (Stale, bool) {
	st := Stale{Tag: p.Tag}
	if !tagRe.MatchString(p.Tag) || p.StaleDays <= 0 {
		return st, false
	}
	now := s.d.Now()
	// date of the release commit, in the checkout (tag present once installed)
	if res, ok := s.git(ctx, s.d.Checkout, "log", "-1", "--format=%cI", "refs/tags/"+p.Tag+"^{commit}", "--"); ok {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(res.Output)); err == nil {
			st.Released = t
			st.AgeDays = int(now.Sub(t).Hours() / 24)
			st.OldEnough = st.AgeDays > p.StaleDays
		}
	}
	if st.Released.IsZero() {
		s.logf("extras: stale check: the date of %s is not known", p.Tag)
		return st, false
	}
	type src struct{ name, dir, ref string }
	var srcs []src
	if p.Mirror != "" {
		srcs = append(srcs, src{"mirror", p.Mirror, "refs/heads/main"})
	}
	srcs = append(srcs, src{"checkout", s.d.Checkout, "refs/remotes/origin/main"})
	for _, c := range srcs {
		rng := "refs/tags/" + p.Tag + ".." + c.ref
		cnt, ok := s.git(ctx, c.dir, "rev-list", "--count", rng, "--")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(cnt.Output))
		if err != nil {
			continue
		}
		st.Source, st.MainAhead, st.AheadKnown = c.name, n, true
		if n > 0 {
			if subj, ok := s.git(ctx, c.dir, "log", "--format=%s", fmt.Sprintf("--max-count=%d", maxScan), rng, "--"); ok {
				for _, line := range strings.Split(subj.Output, "\n") {
					line = strings.TrimSpace(line)
					if line != "" && SecurityRe.MatchString(line) {
						st.SecurityN++
						if len(st.SecuritySub) < 3 {
							st.SecuritySub = append(st.SecuritySub, line)
						}
					}
				}
			}
		}
		break
	}
	s.logf("extras: stale check: %s is %d day(s) old (limit %d); main ahead %d (%s); %d security-looking subject(s) %v",
		p.Tag, st.AgeDays, p.StaleDays, st.MainAhead, st.Source, st.SecurityN, st.SecuritySub)
	return st, true
}
