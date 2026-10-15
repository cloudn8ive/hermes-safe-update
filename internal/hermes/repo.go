package hermes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
// locked pack and hangs the updater). `--no-optional-locks` keeps the
// read-only calls (`git status`) from refreshing the index on disk, so a
// check leaves the checkout's index byte for byte as it was.
type GitRepo struct {
	Git      string // absolute git path (Install.Git); "" = unavailable
	Checkout string
	Run      execx.Runner
}

var _ Repo = (*GitRepo)(nil)

func (r *GitRepo) argv(rest ...string) []string {
	return append([]string{r.Git, "-c", "gc.auto=0", "--no-optional-locks", "-C", r.Checkout}, rest...)
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

// CommitCount counts the commits reachable from `to` but not from `from`.
// Only full 40-character SHAs are accepted, so an odd value can never be
// read by git as an option or a ref name.
func (r *GitRepo) CommitCount(ctx context.Context, from, to string) (int, bool) {
	if len(from) != fullSHALen || len(to) != fullSHALen {
		return 0, false
	}
	res, err := r.run(ctx, gitTimeout, "rev-list", "--count", from+".."+to)
	if err != nil || res.TimedOut || res.Code != 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(res.Output))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// IsAncestor is `git merge-base --is-ancestor <ancestor> <descendant>`: exit
// 0 = yes, 1 = no, anything else (a missing object, a timeout, no git) =
// ok=false. A commit is its own ancestor. ancestor must be a full 40-digit
// sha; descendant that or the word HEAD, so no argument can be read as an
// option. It looks only at what is local (no lazy fetch).
func (r *GitRepo) IsAncestor(ctx context.Context, ancestor, descendant string) (yes, ok bool) {
	if !fullHexRe.MatchString(ancestor) || (descendant != "HEAD" && !fullHexRe.MatchString(descendant)) {
		return false, false
	}
	if r.Git == "" {
		return false, false
	}
	res, err := r.Run.Run(ctx, execx.Cmd{
		Argv:    r.argv("merge-base", "--is-ancestor", ancestor, descendant),
		Env:     map[string]string{"GIT_NO_LAZY_FETCH": "1"},
		Timeout: gitTimeout,
	})
	if err != nil || res.TimedOut {
		return false, false
	}
	switch res.Code {
	case 0:
		return true, true
	case 1:
		return false, true
	}
	return false, false
}

// IsShallow is `git rev-parse --is-shallow-repository`. A shallow clone lacks
// history, so "this commit is not here" proves nothing about it; a partial
// clone (blob:none) has every commit and is not shallow. ok=false when git
// cannot say.
func (r *GitRepo) IsShallow(ctx context.Context) (shallow, ok bool) {
	res, err := r.run(ctx, gitTimeout, "rev-parse", "--is-shallow-repository")
	if err != nil || res.TimedOut || res.Code != 0 {
		return false, false
	}
	switch strings.TrimSpace(res.Output) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// DiffKinds decides from `git diff --quiet` exit codes only (rule 18: stderr
// carries unrelated pack warnings, so output is never parsed). 0 = unchanged,
// 1 = changed, anything else (including a timeout) = unknown. ref is what the
// update would install: MainRef, a release tag `refs/tags/vX.Y.Z` or a full
// commit sha; anything else (an option, a name that could be misread) is
// unknown without running git.
func (r *GitRepo) DiffKinds(ctx context.Context, ref string) (npm, pydeps *bool) {
	if !validDiffRef(ref) {
		return nil, nil
	}
	npm = r.diffQuiet(ctx, ref, ":(glob)**/package.json", ":(glob)**/package-lock.json")
	pydeps = r.diffQuiet(ctx, ref, PyFilesPathspecs...)
	return npm, pydeps
}

func (r *GitRepo) diffQuiet(ctx context.Context, ref string, paths ...string) *bool {
	args := append([]string{"diff", "--quiet", "HEAD", ref, "--"}, paths...)
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

// MainRef is what a main update installs, as DiffKinds takes it.
const MainRef = "origin/main"

var (
	fullHexRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hexPrefixRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// validDiffRef is the whitelist of DiffKinds targets: never an option, never
// a name git could read as something else.
func validDiffRef(ref string) bool {
	if ref == MainRef || fullHexRe.MatchString(ref) {
		return true
	}
	tag, ok := strings.CutPrefix(ref, "refs/tags/")
	return ok && StableTagRe.MatchString(tag)
}

// ResolveRelease finds the commit of a release in this checkout, offline:
//
//  1. the tag, when it exists here and (if Hermes named a commit) points at
//     that commit: ref = "refs/tags/<tag>";
//  2. else the commit Hermes named (sha12, a 7-40 digit prefix), when exactly
//     one commit here starts with it: ref = the full sha;
//  3. else nothing ("", ""): the caller treats the prediction as unknown. It
//     never falls back to the main diff or to a guess, and an ambiguous
//     prefix is "nothing".
//
// sha is always the full 40-digit commit, for counting. Lazy fetching from a
// partial clone's remote is switched off: this only looks at what is local.
func (r *GitRepo) ResolveRelease(ctx context.Context, tag, sha12 string) (ref, sha string) {
	if tag != "" && !StableTagRe.MatchString(tag) {
		tag = ""
	}
	if sha12 != "" && !hexPrefixRe.MatchString(sha12) {
		sha12 = ""
	}
	if tag != "" {
		if full := r.commitOf(ctx, "refs/tags/"+tag); full != "" && (sha12 == "" || strings.HasPrefix(full, sha12)) {
			return "refs/tags/" + tag, full
		}
	}
	if sha12 != "" {
		if full := r.commitOf(ctx, sha12); full != "" {
			return full, full
		}
	}
	return "", ""
}

// commitOf is the full sha of the commit a validated name resolves to, or "".
func (r *GitRepo) commitOf(ctx context.Context, name string) string {
	if r.Git == "" {
		return ""
	}
	res, err := r.Run.Run(ctx, execx.Cmd{
		Argv:    r.argv("rev-parse", "--verify", "--quiet", name+"^{commit}"),
		Env:     map[string]string{"GIT_NO_LAZY_FETCH": "1"},
		Timeout: gitTimeout,
	})
	if err != nil || res.TimedOut || res.Code != 0 {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(res.Output), "\n", 2)[0])
	if fullHexRe.MatchString(line) {
		return line
	}
	return ""
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
