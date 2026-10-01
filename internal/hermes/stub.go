package hermes

import "path/filepath"

func joinPath(elem ...string) string {
	if len(elem) == 0 || elem[0] == "" {
		return ""
	}
	return filepath.Join(elem...)
}
