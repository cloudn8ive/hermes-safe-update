package hermes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

const versionTimeout = 60 * time.Second

// defaultStepAside is shown for a packaged install whose hint has no text.
const defaultStepAside = "this Hermes install is not a managed source checkout; update it the way it was installed"

// RealLocator implements Locator from the platform path tables, the config
// overrides and what is on disk. It never changes anything.
type RealLocator struct {
	Paths platform.Paths
	Run   execx.Runner
	Env   platform.Env
	Cfg   config.Paths
	// LookPath finds git on PATH; default exec.LookPath.
	LookPath func(string) (string, error)
}

var _ Locator = (*RealLocator)(nil)

// NewLocator wires the locator to a platform and the real environment.
func NewLocator(p *platform.Platform, run execx.Runner, cfg config.Paths) *RealLocator {
	home, _ := os.UserHomeDir()
	return &RealLocator{Paths: p.Paths, Run: run, Cfg: cfg, Env: platform.Env{Getenv: os.Getenv, UserHome: home}}
}

func (l *RealLocator) getenv(k string) string {
	if l.Env.Getenv == nil {
		return ""
	}
	return l.Env.Getenv(k)
}

func (l *RealLocator) expand(p string) string {
	return config.ExpandPath(p, l.Env.UserHome, l.getenv)
}

// Locate resolves the install. A packaged (non-checkout) install is not an
// error: Kind says so and StepAside holds the advice. A checkout without a
// usable launcher is apperr.ErrLauncherMissing.
func (l *RealLocator) Locate(ctx context.Context) (Install, error) {
	home := l.expand(l.Cfg.HermesHome)
	if home == "" {
		h, err := l.Paths.HermesHome(l.Env)
		if err != nil {
			return Install{}, fmt.Errorf("locate the Hermes home: %w", err)
		}
		home = h
	}
	in := Install{Home: home, Checkout: l.expand(l.Cfg.Checkout)}
	if in.Checkout == "" {
		in.Checkout = joinPath(home, "hermes-agent")
	}
	in.UserData = l.expand(l.Cfg.UserData)
	if in.UserData == "" {
		in.UserData, _ = l.Paths.UserData(l.Env) // optional: "" when unknown
	}

	if st, err := os.Stat(filepath.Join(in.Checkout, ".git")); err != nil || st == nil {
		in.Kind = KindUnknown
		for _, h := range l.Paths.PackagedInstallHints(l.Env) {
			if h.Path != "" && exists(h.Path) {
				in.Kind = InstallKind(h.Kind)
				in.StepAside = h.Use
				if in.StepAside == "" {
					in.StepAside = defaultStepAside
				}
				break
			}
		}
		return in, nil
	}
	in.Kind = KindCheckout

	launcher, err := l.pickLauncher(ctx, in)
	if err != nil {
		return in, err
	}
	in.Launcher = launcher
	in.Python = lastGlob(l.Paths.ManagedPythonGlobs(home))
	in.Git = l.findGit(home)
	in.Electron = l.findElectron(in.Checkout)
	for _, c := range l.Paths.DesktopAppCandidates(in.Checkout) {
		if exists(c) {
			in.Desktop = c
			break
		}
	}
	return in, nil
}

// pickLauncher returns the first existing candidate whose `--version`
// "Install directory:" is this checkout (platforms.md §2). A candidate that
// prints no such line is accepted (older builds), one that cannot run is
// skipped.
func (l *RealLocator) pickLauncher(ctx context.Context, in Install) (string, error) {
	var tried []string
	for _, c := range l.Paths.LauncherCandidates(l.Env, in.Home, in.Checkout) {
		if !exists(c) {
			continue
		}
		res, err := l.Run.Run(ctx, execx.Cmd{Argv: []string{c, "--version"}, Timeout: versionTimeout})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			tried = append(tried, c)
			continue
		}
		v := ParseVersion(res.Output)
		if v.InstallDir == "" || samePath(v.InstallDir, in.Checkout) {
			return c, nil
		}
		tried = append(tried, c)
	}
	if len(tried) > 0 {
		return "", fmt.Errorf("%w: none of %s belongs to %s", apperr.ErrLauncherMissing, strings.Join(tried, ", "), in.Checkout)
	}
	return "", fmt.Errorf("%w: no launcher found for %s", apperr.ErrLauncherMissing, in.Checkout)
}

// findGit prefers Hermes' bundled git, then PATH (D4).
func (l *RealLocator) findGit(home string) string {
	if g := lastGlob(l.Paths.BundledGitGlobs(home)); g != "" {
		return g
	}
	lp := l.LookPath
	if lp == nil {
		lp = exec.LookPath
	}
	if g, err := lp("git"); err == nil {
		return g
	}
	return ""
}

func (l *RealLocator) findElectron(checkout string) string {
	nm := filepath.Join(checkout, "apps", "desktop", "node_modules", "electron")
	leaf := l.Paths.ElectronLeaf()
	if b, err := os.ReadFile(filepath.Join(nm, ElectronPathFile)); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			leaf = s
		}
	}
	if leaf == "" {
		return ""
	}
	p := filepath.Join(nm, "dist", filepath.FromSlash(leaf))
	if exists(p) {
		return p
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// lastGlob returns the last (sorted) existing match over all patterns, "" if none.
func lastGlob(patterns []string) string {
	var all []string
	for _, pat := range patterns {
		m, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, p := range m {
			if exists(p) {
				all = append(all, p)
			}
		}
	}
	if len(all) == 0 {
		return ""
	}
	sort.Strings(all)
	return all[len(all)-1]
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if strings.EqualFold(a, b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && strings.EqualFold(ra, rb)
}

// CurrentVersion returns the version `launcher --version` reports (e.g.
// "v0.21.5+5279.g4e74031"), or "" when it cannot be read: callers only
// display it (settings backup manifests), so unknown is not an error.
func CurrentVersion(ctx context.Context, run execx.Runner, launcher string) string {
	if run == nil || launcher == "" {
		return ""
	}
	res, err := run.Run(ctx, execx.Cmd{Argv: []string{launcher, "--version"}, Timeout: versionTimeout})
	if err != nil || res.Code != 0 {
		return ""
	}
	return ParseVersion(res.Output).Version
}

// VersionInfo is `hermes --version` parsed.
type VersionInfo struct {
	Version    string // "v0.21.5+5279.g4e74031"
	Build      string // "2026.9.24" (the parenthesised release date), may be ""
	InstallDir string
	Method     string // "git"
}

// ParseVersion reads `hermes --version` output; absent fields stay "".
func ParseVersion(out string) VersionInfo {
	var v VersionInfo
	if m := VersionHeadRe.FindStringSubmatch(out); m != nil {
		v.Version, v.Build = m[1], m[2]
	}
	if m := VersionInstallDirRe.FindStringSubmatch(out); m != nil {
		v.InstallDir = m[1]
	}
	if m := VersionMethodRe.FindStringSubmatch(out); m != nil {
		v.Method = m[1]
	}
	return v
}
