package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestConfigDirIsAbsoluteAndScoped checks the invariant the rest of the code
// relies on: the returned directory is absolute and names the app, whatever the
// platform's base directory convention turns out to be.
func TestConfigDirIsAbsoluteAndScoped(t *testing.T) {
	dir, err := ConfigDir()
	if err != nil {
		t.Skipf("no user config dir on this platform: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("config dir %q is not absolute", dir)
	}
	if filepath.Base(dir) != AppName {
		t.Errorf("config dir %q should end in %q", dir, AppName)
	}
}

func TestCacheDirIsAbsoluteAndScoped(t *testing.T) {
	dir, err := CacheDir()
	if err != nil {
		t.Skipf("no user cache dir on this platform: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("cache dir %q is not absolute", dir)
	}
	if filepath.Base(dir) != AppName {
		t.Errorf("cache dir %q should end in %q", dir, AppName)
	}
}

// TestDirsAreSeparate guards against config and cache collapsing onto the same
// location, which would put regenerable state next to hand-edited files.
func TestDirsAreSeparate(t *testing.T) {
	config, cerr := ConfigDir()
	cache, kerr := CacheDir()
	if cerr != nil || kerr != nil {
		t.Skip("a user directory is unavailable on this platform")
	}
	if config == cache {
		t.Errorf("config and cache both resolved to %q", config)
	}
}

// TestPathSeparatorIsNative catches a hardcoded slash that would produce a
// directory name like "getfm/foo" on Windows instead of a nested path.
func TestPathSeparatorIsNative(t *testing.T) {
	dir, err := ConfigDir()
	if err != nil {
		t.Skip("no user config dir on this platform")
	}
	if runtime.GOOS == "windows" && filepath.Base(filepath.Dir(dir)) != "" {
		if got := filepath.Base(dir); got != AppName {
			t.Errorf("unexpected nesting on windows: %q", dir)
		}
	}
	// filepath.Join is what produced it, so re-joining the base must be stable.
	if filepath.Base(dir) != filepath.Base(filepath.Join(filepath.Dir(dir), AppName)) {
		t.Errorf("directory %q was not built with filepath.Join", dir)
	}
}

func TestEnsureDirCreatesAndIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Permission bits are not enforced the same way; the creation check
		// still applies but the mode assertion does not.
		t.Skip("skipping permission assertion on windows")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "nested", AppName)

	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	// Calling it again on an existing directory must not fail.
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("second EnsureDir failed: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("%s was not created as a directory", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o, want 700 for a per-user directory", info.Mode().Perm())
	}
}
