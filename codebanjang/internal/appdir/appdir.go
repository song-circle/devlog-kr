// Package appdir locates the program's data folder.
package appdir

import (
	"os"
	"path/filepath"
)

// Dir is %LOCALAPPDATA%\codebanjang. A PC that used the program under its
// working title keeps %LOCALAPPDATA%\ai-office: its work folders are
// registered in the projects' git metadata by absolute path, so the
// folder is not moved.
func Dir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "codebanjang-data"
	}
	dir := filepath.Join(base, "codebanjang")
	legacy := filepath.Join(base, "ai-office")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(legacy, "aio.db")); err == nil {
			return legacy
		}
	}
	return dir
}
