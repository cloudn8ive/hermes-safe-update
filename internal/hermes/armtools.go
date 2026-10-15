package hermes

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

// On Windows ARM64 some locked Python dependencies (cryptography) have no
// wheel, so the dependency build needs MSVC, Clang, Rust and OpenSSL, and
// Hermes' scripts/windows-build-deps.ps1 prepares them. When either the
// ARM64 C++ tools or clang.exe is missing it downloads and runs the Visual
// Studio Build Tools installer (several GB, a UAC prompt) in the middle of
// `hermes update`, after Hermes was closed (hermes-agent #134121; a
// half-installed Clang component leaves clang-format.exe without clang.exe and
// the installer then stalls). This probe repeats the script's lookups,
// read-only, so Safe Update can say so before it closes anything. It
// installs nothing and changes no environment.

// ManagedTarget is the target in the name of Hermes' managed Python folder
// (tools/python-<version>-<target>/python.exe, e.g. "win32-arm64"), or ""
// when the path does not name one. Hermes picks the folder by the machine it
// runs on (pm/store.py current_target), so this is the build target of the
// dependency step even for an emulated Safe Update binary.
func ManagedTarget(python string) string {
	if python == "" {
		return ""
	}
	dir := filepath.Base(filepath.Dir(python))
	if !strings.HasPrefix(dir, "python-") {
		return ""
	}
	for _, t := range []string{ARM64Target, "win32-x64"} {
		if strings.HasSuffix(dir, "-"+t) {
			return t
		}
	}
	return ""
}

const vswhereTimeout = 10 * time.Second

// ARM64Tools looks for the ARM64 build tools the way Hermes' script does.
type ARM64Tools struct {
	Getenv func(string) string
	Exists func(string) bool // a regular file exists
	Run    execx.Runner
}

// ARM64Report is what the probe found.
type ARM64Report struct {
	// Unknown is why the probe could not decide ("" = decided). Any error
	// running vswhere lands here: unknown is never reported as missing.
	Unknown string
	// VS is the Visual Studio with the ARM64 C++ tools ("" = none found).
	VS string
	// Clang is the clang.exe the script would use ("" = none).
	Clang string
	// ClangFormatOnly is a clang-format.exe found beside where clang.exe
	// should be: the Clang component is half-installed.
	ClangFormatOnly string
}

// Missing reports whether the script would go to the installer.
func (r ARM64Report) Missing() bool { return r.Unknown == "" && (r.VS == "" || r.Clang == "") }

// Probe repeats Get-HermesArm64VisualStudio and Get-HermesClang.
func (t ARM64Tools) Probe(ctx context.Context) ARM64Report {
	var rep ARM64Report
	vs, err := t.visualStudio(ctx)
	if err != nil {
		rep.Unknown = err.Error()
		return rep
	}
	rep.VS = vs

	candidates := []string{}
	for _, d := range filepath.SplitList(t.getenv("PATH")) {
		if d = strings.Trim(d, `"`); d != "" {
			candidates = append(candidates, filepath.Join(d, ARM64ClangExe))
		}
	}
	var dirs []string
	if pf := t.getenv("ProgramFiles"); pf != "" {
		d := filepath.Join(append([]string{pf}, ARM64ClangLLVMRel...)...)
		dirs = append(dirs, d)
		candidates = append(candidates, filepath.Join(d, ARM64ClangExe))
	}
	if vs != "" {
		for _, rel := range ARM64ClangVSRels {
			d := filepath.Join(append([]string{vs}, rel...)...)
			dirs = append(dirs, d)
			candidates = append(candidates, filepath.Join(d, ARM64ClangExe))
		}
	}
	for _, c := range candidates {
		if t.exists(c) {
			rep.Clang = c
			return rep
		}
	}
	for _, d := range dirs {
		if f := filepath.Join(d, ARM64ClangFormatExe); t.exists(f) {
			rep.ClangFormatOnly = f
			break
		}
	}
	return rep
}

func (t ARM64Tools) getenv(k string) string {
	if t.Getenv == nil {
		return ""
	}
	return t.Getenv(k)
}

func (t ARM64Tools) exists(p string) bool { return t.Exists != nil && t.Exists(p) }

// visualStudio is Get-HermesArm64VisualStudio: no vswhere = none found; a
// vswhere that fails is an error (the script throws "Visual Studio
// discovery failed"); our probe calls that unknown.
func (t ARM64Tools) visualStudio(ctx context.Context) (string, error) {
	base := t.getenv(ARM64VswhereEnv)
	if base == "" {
		return "", nil
	}
	vswhere := filepath.Join(append([]string{base}, ARM64VswhereRel...)...)
	if !t.exists(vswhere) {
		return "", nil
	}
	if t.Run == nil {
		return "", fmt.Errorf("no command runner")
	}
	res, err := t.Run.Run(ctx, execx.Cmd{Argv: append([]string{vswhere}, ARM64VswhereArgs...), Timeout: vswhereTimeout})
	if err != nil {
		return "", fmt.Errorf("vswhere could not run: %w", err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("vswhere exited with code %d", res.Code)
	}
	for _, line := range strings.Split(res.Output, "\n") {
		if line = strings.TrimSpace(strings.TrimSuffix(line, "\r")); line != "" {
			return line, nil
		}
	}
	return "", nil
}
