//go:build !windows

package platform

// ApplyAppID is the Windows-only console identity child mode; elsewhere it
// does nothing and reports "not a window" (exit 2).
func ApplyAppID(appID, hwndText string) int { return 2 }
