//go:build !windows && !darwin && !linux

package platform

// New returns stubs on operating systems Hermes does not run on, so that
// `go vet` and builds for other GOOS values still compile.
func New() *Platform {
	return newStub("unsupported", false)
}
