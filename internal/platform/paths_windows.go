//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// winPaths implements Paths for Windows (python-behaviour §1.5, platforms.md
// §2). knownFolder is injected so tests can simulate empty environments.
type winPaths struct {
	goarch      string
	knownFolder func(id string) (string, error) // "local" | "roaming"
}

var _ Paths = winPaths{}

var errNoKnown = errors.New("known folder unavailable")

func realKnownFolder(id string) (string, error) {
	f := windows.FOLDERID_LocalAppData
	if id == "roaming" {
		f = windows.FOLDERID_RoamingAppData
	}
	return windows.KnownFolderPath(f, 0)
}

func envGetW(env Env, k string) string {
	if env.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(env.Getenv(k))
}

// appDir resolves %LOCALAPPDATA% / %APPDATA% with the known-folder API and
// %USERPROFILE%\AppData\... as fallbacks (those vars are empty in some
// service / scheduled-task environments).
func (p winPaths) appDir(env Env, envKey, folder, leaf string) (string, error) {
	if v := envGetW(env, envKey); v != "" {
		return filepath.Clean(v), nil
	}
	if p.knownFolder != nil {
		if v, err := p.knownFolder(folder); err == nil && v != "" {
			return filepath.Clean(v), nil
		}
	}
	if env.UserHome != "" {
		return filepath.Join(env.UserHome, "AppData", leaf), nil
	}
	return "", errors.New("cannot find %" + envKey + "% (environment, known folder and user profile all unavailable)")
}

// HermesHome is %LOCALAPPDATA%\hermes plus HERMES_DATA_DIR_SUFFIX. Like the
// Python updater it ignores HERMES_HOME: the update always targets the
// default profile's install.
func (p winPaths) HermesHome(env Env) (string, error) {
	lad, err := p.appDir(env, "LOCALAPPDATA", "local", "Local")
	if err != nil {
		return "", err
	}
	return filepath.Join(lad, "hermes") + envGetW(env, "HERMES_DATA_DIR_SUFFIX"), nil
}

// UserData is %APPDATA%\Hermes (+ suffix); HERMES_DESKTOP_USER_DATA_DIR
// replaces it.
func (p winPaths) UserData(env Env) (string, error) {
	if ud := envGetW(env, "HERMES_DESKTOP_USER_DATA_DIR"); ud != "" {
		return filepath.Clean(ud), nil
	}
	ad, err := p.appDir(env, "APPDATA", "roaming", "Roaming")
	if err != nil {
		return "", err
	}
	return filepath.Join(ad, "Hermes") + envGetW(env, "HERMES_DATA_DIR_SUFFIX"), nil
}

// LauncherCandidates: the installed launcher first (what the Python used),
// then the checkout-local one.
func (winPaths) LauncherCandidates(_ Env, home, checkout string) []string {
	return []string{
		filepath.Join(home, "bin", "hermes.exe"),
		filepath.Join(checkout, ".hermes", "bin", "hermes.exe"),
	}
}

// DesktopAppCandidates lists the source-built desktop app, native arch first.
func (p winPaths) DesktopAppCandidates(checkout string) []string {
	dirs := []string{"win-unpacked", "win-arm64-unpacked", "win-ia32-unpacked"}
	if p.goarch == "arm64" {
		dirs = []string{"win-arm64-unpacked", "win-unpacked", "win-ia32-unpacked"}
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(checkout, "apps", "desktop", "release", d, "Hermes.exe"))
	}
	return out
}

// DesktopProcessMarkers: the desktop main process runs from a *-unpacked
// release dir ("win-unpacked" also matches the arm64/ia32 dirs' prefix
// only for the plain one, so list all three).
func (winPaths) DesktopProcessMarkers() []string {
	return []string{"win-unpacked", "win-arm64-unpacked", "win-ia32-unpacked"}
}

func (winPaths) ManagedPythonGlobs(home string) []string {
	return []string{filepath.Join(home, "tools", "python-*", "python.exe")}
}

func (winPaths) BundledGitGlobs(home string) []string {
	return []string{filepath.Join(home, "tools", "git-*", "mingw64", "bin", "git.exe")}
}

func (winPaths) ElectronLeaf() string { return "electron.exe" }

// PackagedInstallHints finds an MSIX install of the desktop app
// (%LOCALAPPDATA%\Packages\NousResearch.Hermes*). Only consulted when no
// checkout exists.
func (p winPaths) PackagedInstallHints(env Env) []PackagedHint {
	lad, err := p.appDir(env, "LOCALAPPDATA", "local", "Local")
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(lad, "Packages", "NousResearch.Hermes*"))
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			return []PackagedHint{{Kind: "msix", Path: m,
				Use: "the Hermes desktop app was installed as an MSIX package; updates come from the App Installer feed (Settings > Apps, or reinstall the package)"}}
		}
	}
	return nil
}
