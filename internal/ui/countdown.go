package ui

import "fmt"

// CountdownText words the time left on a prompt: whole minutes (rounded up,
// so "10 min" holds until the last minute starts) from two minutes on, plain
// seconds below. A ten-minute wait reads better as "10 min" than "600s".
func CountdownText(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds >= 120 {
		return fmt.Sprintf("%d min", (seconds+59)/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
