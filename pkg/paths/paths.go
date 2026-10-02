// Package paths resolves the per-user directories getfm reads and writes.
//
// It exists so that nothing in the codebase has to know which platform it is
// running on. Each helper defers to the standard library, which already encodes
// the right convention per OS: %AppData% for config on Windows,
// %LocalAppData% for cache, ~/Library/Application Support on macOS, and the XDG
// variables on Linux and the BSDs.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppName is the directory name used under every base directory.
const AppName = "getfm"

// ConfigDir is where a user's configuration lives. Files here are meant to be
// edited by hand.
func ConfigDir() (string, error) {
	return subdir(os.UserConfigDir)
}

// CacheDir is where regenerable state lives. Anything under it can be deleted
// at any time without losing data that cannot be fetched again.
func CacheDir() (string, error) {
	return subdir(os.UserCacheDir)
}

// subdir appends AppName to a base directory returned by the standard library.
func subdir(base func() (string, error)) (string, error) {
	root, err := base()
	if err != nil {
		return "", fmt.Errorf("locate user directory: %w", err)
	}
	if root == "" {
		return "", fmt.Errorf("user directory is empty")
	}
	return filepath.Join(root, AppName), nil
}

// EnsureDir creates a directory and its parents, tolerating one that exists.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}
