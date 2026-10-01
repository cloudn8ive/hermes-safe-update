package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// git runs one git command the way every mirror call does: bundled git
// (D4), gc and maintenance off (auto-gc in the checkout loops on a locked
// pack), no prompts, no console window, HERMES_HOME removed (execx).
func (s *Svc) git(ctx context.Context, cwd string, timeout time.Duration, extraEnv map[string]string, args ...string) (execx.Result, error) {
	argv := []string{s.d.Git, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	if cwd != "" {
		argv = append(argv, "-C", cwd)
	}
	argv = append(argv, args...)
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return s.d.Runner.Run(ctx, execx.Cmd{Argv: argv, Env: env, Timeout: timeout})
}

func (s *Svc) head(ctx context.Context, mirror string) string {
	res, err := s.git(ctx, mirror, 30*time.Second, nil, "rev-parse", "refs/heads/"+Branch)
	if err != nil || res.Code != 0 {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(res.Output), "\n", 2)[0])
	if shaRe.MatchString(line) {
		return line
	}
	return ""
}

// apiGet does one GitHub REST GET. It returns the body, or ok=false on any
// problem (the caller falls back to git).
func (s *Svc) apiGet(ctx context.Context, path, accept string, timeout time.Duration, limit int64) ([]byte, bool) {
	if s.d.APIBase == "" {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.d.APIBase+path, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.d.HTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, false
	}
	return body, true
}

// APIMainSHA is the current tip of main from the REST API (10 s), or
// ok=false. Unauthenticated: 60 calls an hour is plenty.
func (s *Svc) APIMainSHA(ctx context.Context) (string, bool) {
	body, ok := s.apiGet(ctx, "/repos/"+Repo+"/commits/"+Branch, "application/vnd.github.sha", 10*time.Second, 100)
	if !ok {
		return "", false
	}
	sha := strings.TrimSpace(string(body))
	if !shaRe.MatchString(sha) {
		return "", false
	}
	return sha, true
}

// APICompare asks GitHub for base...main (15 s). Only "ahead"/"identical"
// with an integer ahead_by is accepted; anything else is nil ("let git
// decide"). GitHub lists at most 300 files, so 300 names = partial list.
func (s *Svc) APICompare(ctx context.Context, base string) *Compare {
	body, ok := s.apiGet(ctx, "/repos/"+Repo+"/compare/"+base+"..."+Branch+"?per_page=1",
		"application/vnd.github+json", 15*time.Second, 16<<20)
	if !ok {
		return nil
	}
	var data struct {
		Status  string `json:"status"`
		AheadBy *int   `json:"ahead_by"`
		Files   []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if json.Unmarshal(body, &data) != nil || data.AheadBy == nil || (data.Status != "ahead" && data.Status != "identical") {
		return nil
	}
	c := &Compare{Commits: *data.AheadBy}
	for _, f := range data.Files {
		c.Files = append(c.Files, f.Filename)
	}
	c.FilesComplete = len(c.Files) < 300
	return c
}

// Refresh fetches main and the v* tags from GitHub into the mirror, taking
// the lock without waiting (the cron job and the interactive paths both use
// it; the updater's own pre-flight refresh waits via refreshAt).
func (s *Svc) Refresh(ctx context.Context) error {
	path := s.mirrorPath()
	if path == "" {
		return ErrNotSetUp
	}
	return s.refreshAt(ctx, path, netTimeout, 0)
}

// refreshAt is the shared refresh: lock, `git fetch`, state file.
func (s *Svc) refreshAt(ctx context.Context, path string, timeout, wait time.Duration) error {
	release, err := s.acquireLock(ctx, wait)
	if err != nil {
		return err
	}
	defer release()
	t0 := s.d.Now()
	res, err := s.git(ctx, path, timeout, nil, "fetch", "--prune", "--no-tags", "origin",
		"+refs/heads/"+Branch+":refs/heads/"+Branch, "+refs/tags/v*:refs/tags/v*")
	if err != nil {
		return err
	}
	st, raw := s.readState()
	st.LastTry = float64(s.d.Now().Unix())
	if res.Code == 0 {
		st.LastOK, st.LastError, st.Head = float64(s.d.Now().Unix()), nil, s.head(ctx, path)
		if werr := s.writeState(st, raw); werr != nil {
			s.logf("mirror state not saved: " + werr.Error())
		}
		s.logf("refresh ok: refreshed in " + strconv.Itoa(int(s.d.Now().Sub(t0).Seconds())) + "s")
		return nil
	}
	msg := tailN(res.Output, 300)
	st.LastError = &msg
	if werr := s.writeState(st, raw); werr != nil {
		s.logf("mirror state not saved: " + werr.Error())
	}
	return fmt.Errorf("git fetch rc=%d: %s", res.Code, tailN(res.Output, 200))
}

func tailN(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// CronRefresh is what `mirror refresh` (the cron job) runs: silent, exit
// codes only. 0 = ok / nothing to do / another refresh is running; 1 = the
// fetch failed (the reason goes to the log file, never stdout, so the job
// delivers nothing).
func (s *Svc) CronRefresh(ctx context.Context) int {
	path := s.mirrorPath()
	if path == "" {
		return 0 // not set up here (the job may have come over from another machine)
	}
	if pid := s.markerPID(); pid > 0 && s.alive(pid) {
		return 0 // an update is running; it refreshes the mirror itself
	}
	if target, ok := s.APIMainSHA(ctx); ok && s.head(ctx, path) == target {
		return 0 // already current: no git round trip at all
	}
	err := s.refreshAt(ctx, path, netTimeout, 0)
	switch {
	case err == nil:
		return 0
	case err == ErrBusy:
		return 0
	default:
		s.logf("refresh FAILED: " + err.Error())
		return 1
	}
}
