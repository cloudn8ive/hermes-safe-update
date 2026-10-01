package platform

import "context"

// Stub implements every interface with ErrNotImplemented / zero values.
// Per-OS New() starts from it so the skeleton compiles; implementers replace
// the fields one by one. Delete uses of Stub as backends get implemented.
type Stub struct{}

var (
	_ Paths      = Stub{}
	_ Procs      = Stub{}
	_ AppControl = Stub{}
	_ Console    = Stub{}
	_ Progress   = Stub{}
	_ Autostart  = Stub{}
	_ Disk       = Stub{}
)

func (Stub) HermesHome(Env) (string, error)                  { return "", ErrNotImplemented }
func (Stub) UserData(Env) (string, error)                    { return "", ErrNotImplemented }
func (Stub) LauncherCandidates(Env, string, string) []string { return nil }
func (Stub) DesktopAppCandidates(string) []string            { return nil }
func (Stub) DesktopProcessMarkers() []string                 { return nil }
func (Stub) ManagedPythonGlobs(string) []string              { return nil }
func (Stub) BundledGitGlobs(string) []string                 { return nil }
func (Stub) ElectronLeaf() string                            { return "" }
func (Stub) PackagedInstallHints(Env) []PackagedHint         { return nil }
func (Stub) List(context.Context) ([]Process, error)         { return nil, ErrNotImplemented }
func (Stub) Alive(int) bool                                  { return false }
func (Stub) KillTree(context.Context, Process) error         { return ErrNotImplemented }
func (Stub) IsRunning(int) bool                              { return false }
func (Stub) RequestClose(context.Context, int) error         { return ErrNotImplemented }
func (Stub) ForceClose(context.Context, Process) error       { return ErrNotImplemented }
func (Stub) CloseBlocker(int) string                         { return "" }
func (Stub) Launch(context.Context, string, []string) error  { return ErrNotImplemented }
func (Stub) CanLaunch(string) (bool, string)                 { return false, "not implemented" }
func (Stub) IsTerminal() bool                                { return false }
func (Stub) EnableVT() (func(), error)                       { return func() {}, ErrNotImplemented }
func (Stub) Size() (int, int, bool)                          { return 0, 0, false }
func (Stub) SetTitle(string) error                           { return nil }
func (Stub) SetIdentity(string, []string) string             { return "skipped: not implemented" }
func (Stub) IsForeground() bool                              { return true }
func (Stub) Flash()                                          {}
func (Stub) Set(ProgressState, float64)                      {}
func (Stub) Close()                                          {}
func (Stub) Supported() bool                                 { return false }
func (Stub) Free(string) (uint64, error)                     { return 0, ErrNotImplemented }
func (Stub) Volumes() ([]Volume, error)                      { return nil, ErrNotImplemented }
func (Stub) TaskExecutable(context.Context, string) (string, bool, error) {
	return "", false, ErrNotSupported
}
func (Stub) RunElevated(context.Context, []string) (int, string, error) {
	return -1, "", ErrNotSupported
}

// ReadKey blocks until ctx is done: a stub console never delivers keys.
func (Stub) ReadKey(ctx context.Context) (Key, error) {
	<-ctx.Done()
	return Key{}, ctx.Err()
}
