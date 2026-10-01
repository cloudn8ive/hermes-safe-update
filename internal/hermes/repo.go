package hermes

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

const (
	gitTimeout     = 60 * time.Second
	gitDiffTimeout = 20 * time.Second
	fullSHALen     = 40
)

// ErrNoGit is returned when neither Hermes' bundled git nor a PATH git exists.
var ErrNoGit = errors.New("git not found (neither Hermes' bundled git nor PATH)")

// GitRepo implements Repo with the git CLI. Every call is
// `git -c gc.auto=0 -C <checkout> ...` (auto-gc in the checkout loops on a
// locked pack and hangs the updater).
type GitRepo struct {
	Git      string // absolute git path (Install.Git); "" = unavailable
	Checkout string
	Run      execx.Runner
}

var _ Repo = (*GitRepo)(nil)

func (r *GitRepo) argv(rest ...string) []string {
	return append([]string{r.Git, "-c", "gc.auto=0", "-C", r.Checkout}, rest...)
}

func (r *GitRepo) run(ctx context.Context, timeout time.Duration, rest ...string) (execx.Result, error) {
	if r.Git == "" {
		return execx.Result{}, ErrNoGit
	}
	return r.Run.Run(ctx, execx.Cmd{Argv: r.argv(rest...), Timeout: timeout})
}

// Head returns the short and full HEAD. A full sha that is not exactly 40
// characters is reported as "" (callers compare it later).
func (r *GitRepo) Head(ctx context.Context) (short, full string, err error) {
	res, err := r.run(ctx, gitTimeout, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", "", err
	}
	if res.Code != 0 {
		return "", "", fmt.Errorf("git rev-parse --short HEAD failed (rc=%d): %s", res.Code, execx.Tail(res.Output, 200))
	}
	short = strings.TrimSpace(res.Output)
	res, err = r.run(ctx, gitTimeout, "rev-parse", "HEAD")
	if err != nil {
		return short, "", err
	}
	if f := strings.TrimSpace(res.Output); res.Code == 0 && len(f) == fullSHALen {
		full = f
	}
	return short, full, nil
}

// DirtyCount counts modified tracked files (informational only: the update
// stashes them and keeps the stash).
func (r *GitRepo) DirtyCount(ctx context.Context) (int, error) {
	res, err := r.run(ctx, gitTimeout, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return 0, err
	}
	if res.Code != 0 {
		return 0, fmt.Errorf("git status failed (rc=%d): %s", res.Code, execx.Tail(res.Output, 200))
	}
	n := 0
	for _, l := range strings.Split(res.Output, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n, nil
}

// DiffKinds decides from `git diff --quiet` exit codes only (rule 18: stderr
// carries unrelated pack warnings, so output is never parsed). 0 = unchanged,
// 1 = changed, anything else (including a timeout) = unknown.
func (r *GitRepo) DiffKinds(ctx context.Context) (npm, pydeps *bool) {
	npm = r.diffQuiet(ctx, ":(glob)**/package.json", ":(glob)**/package-lock.json")
	pydeps = r.diffQuiet(ctx, "uv.lock", "pyproject.toml")
	return npm, pydeps
}

func (r *GitRepo) diffQuiet(ctx context.Context, paths ...string) *bool {
	args := append([]string{"diff", "--quiet", "HEAD", "origin/main", "--"}, paths...)
	res, err := r.run(ctx, gitDiffTimeout, args...)
	if err != nil || res.TimedOut {
		return nil
	}
	switch res.Code {
	case 0:
		v := false
		return &v
	case 1:
		v := true
		return &v
	}
	return nil
}

// ClassifyChanged says whether Node packages / Python dependencies changed
// from a list of changed files. A partial list (API truncation) can prove
// "changed" but never "unchanged": that stays nil (unknown).
func ClassifyChanged(files []string, complete bool) (npm, pydeps *bool) {
	var n, p bool
	for _, f := range files {
		f = strings.ReplaceAll(f, "\\", "/")
		if NPMFilesRe.MatchString(f) {
			n = true
		}
		if PyFilesRe.MatchString(f) {
			p = true
		}
	}
	tri := func(found bool) *bool {
		if found {
			return &found
		}
		if complete {
			v := false
			return &v
		}
		return nil
	}
	return tri(n), tri(p)
}

// InterpretUpdateCheck turns `hermes update --check` output into an
// UpdateCheck (Source "git"). rc != 0 means the check itself failed:
// Behind stays nil. The package kinds are filled in by the caller via
// Repo.DiffKinds when Behind is true.
func InterpretUpdateCheck(out string, rc int) UpdateCheck {
	u := UpdateCheck{Source: "git"}
	u.Detail = fmt.Sprintf("update --check (rc=%d): %s", rc, execx.Tail(out, 400))
	if rc != 0 {
		return u
	}
	behind := CheckSaysBehind(out)
	u.Behind = &behind
	if m := CheckCommitsRe.FindStringSubmatch(out); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			u.Commits = &n
		}
	}
	return u
}
