package settings

import (
	"embed"
	"io/fs"
)

//go:embed migrator/*.js
var migratorFS embed.FS

// MigratorFiles returns the embedded migrator scripts (written to a temp
// dir at run time and run with Hermes' Electron).
func MigratorFiles() fs.FS {
	sub, err := fs.Sub(migratorFS, "migrator")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return sub
}
