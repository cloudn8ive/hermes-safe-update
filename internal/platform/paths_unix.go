package platform

// Shared macOS + Linux path resolution. No build constraint on purpose: it
// is pure string logic (package "path", never "path/filepath"), so its tests
// run on every development OS, including Windows. Layout facts come from
// .work/platforms.md and are UNVERIFIED on real machines.

import (
	"errors"
	"path"
	"strings"
)

// unixPaths implements Paths for darwin and linux. goos and goarch are
// injected so tests can exercise both OSes anywhere.
type unixPaths struct {
	goos, goarch string
}

var _ Paths = unixPaths{}

// newUnixPaths returns the Paths backend for goos ("darwin" or "linux") and
// goarch ("arm64", "amd64", ...).
func newUnixPaths(goos, goarch string) unixPaths { return unixPaths{goos: goos, goarch: goarch} }

var errNoHome = errors.New("cannot find the user's home directory (HOME is not set)")

func envGet(env Env, key string) string {
	if env.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(env.Getenv(key))
}

// HermesHome is $HERMES_HOME, else <userData override>/hermes-home when
// HERMES_DESKTOP_USER_DATA_DIR is set, else ~/.hermes plus the literal
// HERMES_DATA_DIR_SUFFIX (data-paths.mjs). A HERMES_HOME inside
// "profiles/<name>" names the parent as the home. Untested on a real
// macOS or Linux machine.
func (p unixPaths) HermesHome(env Env) (string, error) {
	if h := envGet(env, "HERMES_HOME"); h != "" {
		return normalizeHome(h), nil
	}
	if ud := envGet(env, "HERMES_DESKTOP_USER_DATA_DIR"); ud != "" {
		return path.Join(path.Clean(ud), "hermes-home"), nil
	}
	if env.UserHome == "" {
		return "", errNoHome
	}
	return path.Join(env.UserHome, ".hermes") + envGet(env, "HERMES_DATA_DIR_SUFFIX"), nil
}

func normalizeHome(h string) string {
	h = path.Clean(h)
	parent := path.Dir(h)
	if strings.EqualFold(path.Base(parent), "profiles") {
		return path.Dir(parent)
	}
	return h
}

// UserData is the Electron userData dir of the stable "Hermes" app:
// ~/Library/Application Support/Hermes (macOS) or $XDG_CONFIG_HOME/Hermes
// (Linux, default ~/.config/Hermes), plus HERMES_DATA_DIR_SUFFIX;
// HERMES_DESKTOP_USER_DATA_DIR replaces the whole path. Untested on a real
// macOS or Linux machine.
func (p unixPaths) UserData(env Env) (string, error) {
	if ud := envGet(env, "HERMES_DESKTOP_USER_DATA_DIR"); ud != "" {
		return path.Clean(ud), nil
	}
	suffix := envGet(env, "HERMES_DATA_DIR_SUFFIX")
	if p.goos == "darwin" {
		if env.UserHome == "" {
			return "", errNoHome
		}
		return path.Join(env.UserHome, "Library", "Application Support", "Hermes") + suffix, nil
	}
	if x := envGet(env, "XDG_CONFIG_HOME"); path.IsAbs(x) {
		return path.Join(x, "Hermes") + suffix, nil
	}
	if env.UserHome == "" {
		return "", errNoHome
	}
	return path.Join(env.UserHome, ".config", "Hermes") + suffix, nil
}

// LauncherCandidates follows upstream's desktop update shim: the checkout's
// .hermes/bin/hermes, ~/.local/bin/hermes, then $HERMES_HOME/bin/hermes.
// Never venv/bin/hermes. Untested on a real macOS or Linux machine.
func (p unixPaths) LauncherCandidates(env Env, home, checkout string) []string {
	var out []string
	if checkout != "" {
		out = append(out, path.Join(checkout, ".hermes", "bin", "hermes"))
	}
	if env.UserHome != "" {
		out = append(out, path.Join(env.UserHome, ".local", "bin", "hermes"))
	}
	if home != "" {
		out = append(out, path.Join(home, "bin", "hermes"))
	}
	return out
}

// DesktopAppCandidates lists the source-built app under
// <checkout>/apps/desktop/release, the host architecture first. Which Linux
// spelling (hermes/Hermes) electron-builder emits is unverified, so both are
// listed. Untested on a real macOS or Linux machine.
func (p unixPaths) DesktopAppCandidates(checkout string) []string {
	rel := path.Join(checkout, "apps", "desktop", "release")
	var out []string
	if p.goos == "darwin" {
		dirs := []string{"mac", "mac-universal"}
		if p.goarch == "arm64" {
			dirs = []string{"mac-arm64", "mac-universal", "mac"}
		}
		for _, d := range dirs {
			out = append(out, path.Join(rel, d, "Hermes.app", "Contents", "MacOS", "Hermes"))
		}
		return out
	}
	dirs := []string{"linux-unpacked"}
	if p.goarch == "arm64" {
		dirs = []string{"linux-arm64-unpacked", "linux-unpacked"}
	}
	for _, d := range dirs {
		for _, leaf := range []string{"hermes", "Hermes"} {
			out = append(out, path.Join(rel, d, leaf))
		}
	}
	return out
}

// DesktopProcessMarkers are lower-case command-line substrings of the
// desktop main process. Untested on a real macOS or Linux machine.
func (p unixPaths) DesktopProcessMarkers() []string {
	if p.goos == "darwin" {
		return []string{"/contents/macos/hermes"}
	}
	return []string{"/release/linux-unpacked/", "/release/linux-arm64-unpacked/"}
}

// ManagedPythonGlobs guesses Hermes' bundled Python (tools/python-*), the
// POSIX counterpart of the Windows layout; the exact layout is unverified.
// Untested on a real macOS or Linux machine.
func (p unixPaths) ManagedPythonGlobs(home string) []string {
	return []string{
		path.Join(home, "tools", "python-*", "bin", "python3"),
		path.Join(home, "tools", "python-*", "bin", "python"),
	}
}

// BundledGitGlobs is empty: no bundled git is known on macOS/Linux, so the
// locator falls back to PATH (D4). Untested on a real macOS or Linux machine.
func (p unixPaths) BundledGitGlobs(string) []string { return nil }

// ElectronLeaf is the Electron executable under node_modules/electron/dist
// (install.js), used only when path.txt is missing. Untested on a real
// macOS or Linux machine.
func (p unixPaths) ElectronLeaf() string {
	if p.goos == "darwin" {
		return "Electron.app/Contents/MacOS/Electron"
	}
	return "electron"
}

// PackagedInstallHints lists places a packaged (non-checkout) install can
// live. Untested on a real macOS or Linux machine.
func (p unixPaths) PackagedInstallHints(env Env) []PackagedHint {
	var out []PackagedHint
	if p.goos == "darwin" {
		const use = "Hermes.app updates itself; use its in-app updater, or install the source checkout to use this tool."
		out = append(out, PackagedHint{Kind: "macos-app", Path: "/Applications/Hermes.app", Use: use})
		if env.UserHome != "" {
			out = append(out, PackagedHint{Kind: "macos-app", Path: path.Join(env.UserHome, "Applications", "Hermes.app"), Use: use})
		}
	}
	out = append(out,
		PackagedHint{Kind: "docker", Path: "/.dockerenv", Use: "Pull a newer Hermes image instead; `hermes update` does not apply in a container."},
		PackagedHint{Kind: "nix", Path: "/nix/store", Use: "Update through your Nix configuration instead."},
	)
	return out
}
