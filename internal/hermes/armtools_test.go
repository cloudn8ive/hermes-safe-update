package hermes

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

func TestManagedTarget(t *testing.T) {
	cases := map[string]string{
		filepath.Join("h", "tools", "python-3.14.7+20260901-win32-arm64", "python.exe"): "win32-arm64",
		filepath.Join("h", "tools", "python-3.14.7+20260901-win32-x64", "python.exe"):   "win32-x64",
		filepath.Join("h", "tools", "python-3.14.7-linux-arm64", "bin", "python3"):      "",
		filepath.Join("h", "tools", "git-2.53.0-win32-arm64", "python.exe"):             "", // not the python folder
		"": "",
	}
	for in, want := range cases {
		if got := ManagedTarget(in); got != want {
			t.Errorf("ManagedTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// The pinned candidate lists match the (synthetic excerpt of the) script.
func TestARM64ContractMatchesFixture(t *testing.T) {
	src := readFixture(t, "windows-build-deps-clang.txt")
	args := regexp.MustCompile(`& \$vswhere (.+)`).FindStringSubmatch(src)
	if args == nil || strings.ReplaceAll(args[1], "'", "") != strings.Join(ARM64VswhereArgs, " ") {
		t.Fatalf("vswhere args %v do not match the script line %v", ARM64VswhereArgs, args)
	}
	if !strings.Contains(src, `${env:`+ARM64VswhereEnv+`}\`+strings.Join(ARM64VswhereRel, `\`)) {
		t.Errorf("vswhere path %v / %s not in the script", ARM64VswhereRel, ARM64VswhereEnv)
	}
	var want []string
	want = append(want, strings.Join(ARM64ClangLLVMRel, `\`)+`\`+ARM64ClangExe)
	for _, rel := range ARM64ClangVSRels {
		want = append(want, strings.Join(rel, `\`)+`\`+ARM64ClangExe)
	}
	var got []string
	for _, m := range regexp.MustCompile(`Join-Path [^ ]+ '([^']+clang\.exe)'`).FindAllStringSubmatch(src, -1) {
		got = append(got, m[1])
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("script candidates %q, pinned %q", got, want)
	}
}

type armWorld struct {
	env    map[string]string
	files  map[string]bool
	vswOut string
	vswRC  int
	run    *execx.Fake
}

func newArmWorld() *armWorld {
	w := &armWorld{
		env: map[string]string{
			"ProgramFiles":      "pf",
			"ProgramFiles(x86)": "pf86",
			"PATH":              strings.Join([]string{"tools", "bin"}, string(filepath.ListSeparator)),
		},
		files: map[string]bool{},
		run:   &execx.Fake{},
	}
	w.files[w.vswhere()] = true
	w.run.OnFunc([]string{w.vswhere()}, func(execx.Cmd) execx.Result {
		return execx.Result{Code: w.vswRC, Output: w.vswOut}
	})
	return w
}

func (w *armWorld) vswhere() string {
	return filepath.Join(append([]string{"pf86"}, ARM64VswhereRel...)...)
}

func (w *armWorld) probe() ARM64Report {
	return ARM64Tools{
		Getenv: func(k string) string { return w.env[k] },
		Exists: func(p string) bool { return w.files[p] },
		Run:    w.run,
	}.Probe(context.Background())
}

var vsRoot = filepath.Join("vs", "2022", "BuildTools")

func (w *armWorld) file(parts ...string) string {
	p := filepath.Join(parts...)
	w.files[p] = true
	return p
}

func TestARM64HealthyViaProgramFilesLLVM(t *testing.T) {
	w := newArmWorld()
	w.vswOut = vsRoot + "\r\n"
	want := w.file("pf", "LLVM", "bin", "clang.exe")
	rep := w.probe()
	if rep.Missing() || rep.Clang != want || rep.VS != vsRoot {
		t.Fatalf("got %+v", rep)
	}
	// vswhere was asked exactly the script's question
	calls := w.run.Calls()
	if len(calls) != 1 || strings.Join(calls[0].Argv[1:], " ") != strings.Join(ARM64VswhereArgs, " ") || calls[0].Timeout != vswhereTimeout {
		t.Errorf("vswhere calls = %+v", calls)
	}
}

func TestARM64ClangInsideVisualStudio(t *testing.T) {
	for _, rel := range ARM64ClangVSRels {
		w := newArmWorld()
		w.vswOut = vsRoot
		want := w.file(append([]string{vsRoot}, append(append([]string{}, rel...), "clang.exe")...)...)
		rep := w.probe()
		if rep.Missing() || rep.Clang != want {
			t.Errorf("%v: got %+v", rel, rep)
		}
	}
}

func TestARM64ClangOnPath(t *testing.T) {
	w := newArmWorld()
	w.vswOut = vsRoot
	want := w.file("tools", "clang.exe")
	if rep := w.probe(); rep.Missing() || rep.Clang != want {
		t.Fatalf("got %+v", rep)
	}
}

// The reporter's machine: Visual Studio with the ARM64 tools, clang-format.exe
// present, clang.exe not.
func TestARM64HalfInstalledClang(t *testing.T) {
	w := newArmWorld()
	w.vswOut = vsRoot
	fmtExe := w.file(vsRoot, "VC", "Tools", "Llvm", "ARM64", "bin", "clang-format.exe")
	rep := w.probe()
	if !rep.Missing() || rep.Clang != "" || rep.VS != vsRoot || rep.ClangFormatOnly != fmtExe {
		t.Fatalf("got %+v", rep)
	}
}

func TestARM64NoVisualStudioToolsEvenWithClang(t *testing.T) {
	w := newArmWorld()
	w.vswOut = "\r\n" // vswhere runs, finds nothing
	w.file("pf", "LLVM", "bin", "clang.exe")
	rep := w.probe()
	// the script goes to the installer when EITHER is missing
	if !rep.Missing() || rep.VS != "" || rep.Clang == "" {
		t.Fatalf("got %+v", rep)
	}
}

func TestARM64NoVswhereMeansNoVisualStudio(t *testing.T) {
	w := newArmWorld()
	delete(w.files, w.vswhere())
	w.file("pf", "LLVM", "bin", "clang.exe")
	rep := w.probe()
	if !rep.Missing() || rep.VS != "" || len(w.run.Calls()) != 0 {
		t.Fatalf("got %+v calls=%d", rep, len(w.run.Calls()))
	}
}

// A vswhere that cannot answer is unknown: never "missing".
func TestARM64VswhereErrorIsUnknown(t *testing.T) {
	w := newArmWorld()
	w.vswRC = 1
	rep := w.probe()
	if rep.Missing() || rep.Unknown == "" {
		t.Fatalf("got %+v", rep)
	}
	w2 := newArmWorld()
	w2.run = &execx.Fake{} // vswhere present on disk, but the runner has no answer
	if rep := w2.probe(); rep.Missing() || rep.Unknown == "" {
		t.Fatalf("runner failure: got %+v", rep)
	}
}
