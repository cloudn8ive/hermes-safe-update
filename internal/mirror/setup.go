package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// SetupExitCode maps a Setup error to the documented `mirror setup` exit
// codes: 0 ok, 1 download/refresh/job failure, 2 no space or no location.
// (3 = cancelled is the CLI's, it owns the Y/n prompt.)
func SetupExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrNoSpace), errors.Is(err, ErrNoLocation):
		return 2
	default:
		return 1
	}
}

const gb = 1e9

func (s *Svc) spaceNeededGB(repoGB float64) float64 {
	return repoGB*(1+s.d.Cfg.Growth) + s.d.Cfg.KeepFreeGB
}

// repoSizeGB asks GitHub for the repo size, else measures the checkout's
// packs (a lower bound), else guesses 1.5 GB. The second value says which.
func (s *Svc) repoSizeGB(ctx context.Context) (float64, string) {
	if body, ok := s.apiGet(ctx, "/repos/"+Repo, "application/vnd.github+json", 10*time.Second, 1<<20); ok {
		var v struct {
			Size float64 `json:"size"`
		}
		if json.Unmarshal(body, &v) == nil && v.Size > 0 {
			return v.Size * 1000 / gb, "GitHub" // size is in KB
		}
	}
	if s.d.Checkout != "" {
		var total int64
		_ = filepath.Walk(filepath.Join(s.d.Checkout, ".git", "objects", "pack"), func(_ string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() {
				total += fi.Size()
			}
			return nil
		})
		if total > 0 {
			return float64(total) / gb, "local checkout (lower bound)"
		}
	}
	return 1.5, "estimate"
}

func mirrorDirIn(root string) string {
	return filepath.Join(root, mirrorDirNm, repoDirNm)
}

// Location proposes where a mirror would go: the non-system fixed volume
// with the most free space that covers the need, else the fallback next to
// the Hermes home (outside it on purpose: a backup job zips the home), else
// "" with the reason. No drive letter is hard-coded.
func (s *Svc) Location(ctx context.Context) (string, float64, string) {
	repoGB, src := s.repoSizeGB(ctx)
	need := s.spaceNeededGB(repoGB)
	vols, _ := s.d.Disk.Volumes()
	var best *platform.Volume
	var system *platform.Volume
	maxFree := 0.0
	for i := range vols {
		v := &vols[i]
		if !v.Fixed {
			continue
		}
		if f := float64(v.Free) / gb; f > maxFree {
			maxFree = f
		}
		if v.System {
			system = v
			continue
		}
		if float64(v.Free)/gb >= need && (best == nil || v.Free > best.Free) {
			best = v
		}
	}
	why := fmt.Sprintf("%.1f GB needed, repo size from %s", need, src)
	if best != nil {
		return mirrorDirIn(best.Root), repoGB, fmt.Sprintf("%s; %.0f GB free there", why, float64(best.Free)/gb)
	}
	sysFree := 0.0
	switch {
	case system != nil:
		sysFree = float64(system.Free) / gb
	case s.d.FallbackDir != "":
		if f, err := s.d.Disk.Free(existingAncestor(s.d.FallbackDir)); err == nil {
			sysFree = float64(f) / gb
			if sysFree > maxFree {
				maxFree = sysFree
			}
		}
	}
	if s.d.FallbackDir != "" && sysFree >= need {
		return mirrorDirIn(s.d.FallbackDir), repoGB, fmt.Sprintf("%s; %.0f GB free there", why, sysFree)
	}
	return "", repoGB, fmt.Sprintf("no drive has the %.1f GB it needs (most free: %.1f GB)", need, maxFree)
}

// existingAncestor is the nearest parent of p that exists (for free space).
func existingAncestor(p string) string {
	for p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
	}
	return p
}

// Setup creates (or adopts) the mirror at dir ("" = the existing mirror,
// else Location), records it in safe-update.json, refreshes it and makes
// sure the refresh job exists. The caller asks "Continue? [Y/n]" first.
func (s *Svc) Setup(ctx context.Context, dir string) error {
	path, why := dir, "chosen with --path"
	if cur := s.mirrorPath(); dir == "" && cur != "" {
		path, why = cur, "already set up"
	} else if dir == "" {
		var gbs float64
		path, gbs, why = s.Location(ctx)
		_ = gbs
		if path == "" {
			s.say("Not set up: " + why + ". Run with --path <folder> to choose a location.")
			return fmt.Errorf("%w: %s", ErrNoLocation, why)
		}
	}
	s.say(fmt.Sprintf("Location: %s  (%s)", path, why))
	exists := isFile(filepath.Join(path, "HEAD"))
	if !exists {
		// Space is checked again right here: it may have changed since the
		// updater offered the setup.
		repoGB, src := s.repoSizeGB(ctx)
		need := s.spaceNeededGB(repoGB)
		free := 0.0
		if f, err := s.d.Disk.Free(existingAncestor(filepath.Dir(path))); err == nil {
			free = float64(f) / gb
		}
		s.say(fmt.Sprintf("Space: repo is %.2f GB (%s); needs %.1f GB free, %.1f GB available", repoGB, src, need, free))
		if free < need {
			s.logf(fmt.Sprintf("setup refused: %.1f GB free, needs %.1f GB", free, need))
			return fmt.Errorf("%w: %s has %.1f GB free, needs %.1f GB", ErrNoSpace, path, free, need)
		}
	}
	s.logf("setup started")
	if exists {
		s.say("Mirror already exists; refreshing it.")
	} else if err := s.clone(ctx, path); err != nil {
		return err
	}
	for _, kv := range [][2]string{{"gc.auto", "0"}, {"uploadpack.allowFilter", "true"},
		{"uploadpack.allowAnySHA1InWant", "true"}, {"remote.origin.tagopt", "--no-tags"}} {
		if _, err := s.git(ctx, path, 30*time.Second, nil, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	ms := s.loadMachine()
	ms.Mirror = machineMirror(path, s.d.Now())
	ms.MirrorOffer = ""
	if err := writeMachine(s.d.MachinePath, ms); err != nil {
		return err
	}
	rerr := s.refreshAt(ctx, path, netTimeout, 60*time.Second)
	if rerr == nil {
		s.say("Release tags and latest commits: ok")
	} else {
		s.say("Release tags and latest commits: " + rerr.Error())
	}
	jmsg, jerr := s.ensureCronJob(ctx)
	s.say("Background refresh: " + jmsg)
	s.say(fmt.Sprintf("Done. Mirror: %s (%.1f GB). The next Safe Update uses it automatically.", path, dirSizeGB(path)))
	s.logf(fmt.Sprintf("setup done; refresh err=%v; job: %s", rerr, jmsg))
	if rerr != nil || jerr != nil {
		return fmt.Errorf("%w (refresh: %v; job: %v)", ErrIncomplete, rerr, jerr)
	}
	return nil
}

func dirSizeGB(p string) float64 {
	var total int64
	_ = filepath.Walk(p, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			total += fi.Size()
		}
		return nil
	})
	return float64(total) / gb
}

// clone downloads into <path>.partial and renames it into place, so a
// half-finished download is never mistaken for a mirror.
func (s *Svc) clone(ctx context.Context, path string) error {
	partial := path + ".partial"
	if isDir(partial) {
		s.say("Removing an unfinished earlier attempt: " + partial)
		if err := os.RemoveAll(partial); err != nil {
			return fmt.Errorf("remove unfinished download %q: %w", partial, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(partial), 0o755); err != nil {
		return fmt.Errorf("create %q: %w", filepath.Dir(partial), err)
	}
	s.say("Downloading (10-20 min on a slow link; the progress is git's own)...")
	t0 := s.d.Now()
	res, err := s.d.Runner.Stream(ctx, execx.Cmd{
		Argv: []string{s.d.Git, "-c", "gc.auto=0", "clone", "--bare", "--single-branch", "--branch", Branch,
			"--no-tags", "--progress", s.d.CloneURL, partial},
		Env: map[string]string{"GIT_TERMINAL_PROMPT": "0"}, Timeout: 2 * time.Hour,
	}, s.say)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		s.logf("setup clone failed rc=" + strconv.Itoa(res.Code))
		_ = os.RemoveAll(partial)
		return fmt.Errorf("%w (rc=%d): %s. Nothing was configured; re-run to try again", ErrCloneFailed, res.Code, tailN(res.Output, 200))
	}
	if err := fsx.Rename(partial, path); err != nil {
		return fmt.Errorf("move the download into place: %w", err)
	}
	s.say(fmt.Sprintf("Downloaded in %.0f min.", s.d.Now().Sub(t0).Minutes()))
	return nil
}

// Decline records "never offer the mirror again".
func (s *Svc) Decline() error {
	ms := s.loadMachine()
	ms.DeclineMirrorOffer()
	return writeMachine(s.d.MachinePath, ms)
}

// Remove forgets the mirror and removes its refresh job. The folder stays
// (delete it by hand).
func (s *Svc) Remove(ctx context.Context) error {
	ms := s.loadMachine()
	old := ""
	if ms.Mirror != nil {
		old = ms.Mirror.Path
	}
	ms.Mirror = nil
	if err := writeMachine(s.d.MachinePath, ms); err != nil {
		return err
	}
	if j := s.cronJob(); j != nil {
		rc, out := s.hermes(ctx, "cron", "remove", fmt.Sprint(j["id"]))
		s.say(fmt.Sprintf("cron job removed (rc=%d)", rc))
		if rc != 0 {
			s.logf("cron remove failed: " + tailN(out, 200))
		}
	}
	if old == "" {
		old = "(none configured)"
	}
	s.say("Mirror forgotten. Its folder was left in place: " + old)
	return nil
}

func (s *Svc) hermes(ctx context.Context, args ...string) (int, string) {
	res, err := s.d.Runner.Run(ctx, execx.Cmd{Argv: append([]string{s.d.Launcher}, args...), Timeout: 180 * time.Second})
	if err != nil {
		return 1, err.Error()
	}
	return res.Code, res.Output
}

// ensureCronJob makes sure an active refresh job exists: reuse an active
// one, resume a paused one (it may have come over from another machine),
// else write the shim and create it.
func (s *Svc) ensureCronJob(ctx context.Context) (string, error) {
	job := s.cronJob()
	if jobActive(job) {
		return "cron job already active", nil
	}
	if job != nil {
		rc, out := s.hermes(ctx, "cron", "resume", fmt.Sprint(job["id"]))
		if rc == 0 && jobActive(s.cronJob()) {
			return fmt.Sprintf("cron job resumed (rc=%d)", rc), nil
		}
		return "cron resume failed: " + tailN(out, 200), errors.New("cron resume failed")
	}
	if err := s.writeShim(); err != nil {
		return "cannot write the refresh script: " + err.Error(), err
	}
	rc, out := s.hermes(ctx, "cron", "create", s.d.Cfg.CronSchedule, "--name", s.d.Cfg.JobName,
		"--script", shimName, "--no-agent", "--deliver", "local", "--failure-deliver", "local")
	if rc == 0 && jobActive(s.cronJob()) {
		return fmt.Sprintf("cron job created (%s)", s.d.Cfg.CronSchedule), nil
	}
	return "cron create failed: " + tailN(out, 300), errors.New("cron create failed")
}

// shimTemplate is the script Hermes' cron runs: it only starts this tool's
// `mirror refresh` with no console window and passes the exit code through.
// Hermes runs cron scripts with a Python or bash interpreter, never an exe.
const shimTemplate = `# Written by hermes-safe-update; regenerated by "mirror setup". Do not edit.
import subprocess, sys
EXE = %s
flags = getattr(subprocess, "CREATE_NO_WINDOW", 0)
r = subprocess.run([EXE, "mirror", "refresh"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, creationflags=flags)
sys.exit(r.returncode)
`

func (s *Svc) writeShim() error {
	if s.d.Exe == "" {
		return errors.New("path of this program unknown")
	}
	lit, _ := json.Marshal(s.d.Exe) // a JSON string is a valid Python string literal
	dir := filepath.Join(s.d.Home, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, shimName), []byte(fmt.Sprintf(shimTemplate, strings.TrimSpace(string(lit)))), 0o644)
}
