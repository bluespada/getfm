package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), FileName)
}

// TestLoadMissingFileIsEmpty covers the first run: nothing on disk is not an
// error, and it yields a store that will report everything as new.
func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(tempStore(t))
	if err != nil {
		t.Fatalf("Load on a missing file returned %v, want nil", err)
	}
	if s == nil {
		t.Fatal("Load returned a nil store")
	}
	if len(s.Models) != 0 {
		t.Errorf("Models = %v, want empty", s.Models)
	}
	if s.Known("openrouter") != nil {
		t.Error("Known on an empty store should be nil")
	}
}

// TestRecordReportsOnlyNewIds is the core contract: a second fetch of the same
// catalogue must report nothing as new.
func TestRecordReportsOnlyNewIds(t *testing.T) {
	s, err := Load(tempStore(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	fresh := s.Record("openrouter", []string{"a", "b"}, now)
	if len(fresh) != 2 {
		t.Fatalf("first Record reported %d new, want 2", len(fresh))
	}

	// Same catalogue again, with one addition and one removal.
	later := now.Add(time.Hour)
	fresh = s.Record("openrouter", []string{"b", "c"}, later)
	if len(fresh) != 1 || fresh[0] != "c" {
		t.Errorf("second Record reported %v, want [c]", fresh)
	}

	// "a" was dropped from the catalogue, so it is no longer known.
	if s.Known("openrouter")["a"] {
		t.Error("a should not be known after being dropped from the catalogue")
	}
	if !s.Known("openrouter")["b"] {
		t.Error("b should still be known")
	}
}

// TestRecordPreservesFirstSeen guards the reason the store keeps timestamps at
// all: a model seen yesterday must not be re-dated by today's fetch.
func TestRecordPreservesFirstSeen(t *testing.T) {
	s, err := Load(tempStore(t))
	if err != nil {
		t.Fatal(err)
	}

	first := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s.Record("kilocode", []string{"x", "y"}, first)

	later := first.Add(72 * time.Hour)
	s.Record("kilocode", []string{"x", "y", "z"}, later)

	rec := s.Models["kilocode"]
	if !rec.Seen["x"].Equal(first) {
		t.Errorf("x first seen = %v, want the original %v", rec.Seen["x"], first)
	}
	if !rec.Seen["y"].Equal(first) {
		t.Errorf("y first seen = %v, want the original %v", rec.Seen["y"], first)
	}
	if !rec.Seen["z"].Equal(later) {
		t.Errorf("z first seen = %v, want %v", rec.Seen["z"], later)
	}
	if !rec.Updated.Equal(later) {
		t.Errorf("provider Updated = %v, want %v", rec.Updated, later)
	}
}

func TestProvidersAreIndependent(t *testing.T) {
	s, err := Load(tempStore(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	s.Record("openrouter", []string{"shared"}, now)
	fresh := s.Record("kilocode", []string{"shared"}, now)

	// A model id shared by two providers is still new to the second one.
	if len(fresh) != 1 {
		t.Errorf("kilocode reported %d new, want 1", len(fresh))
	}
}

// TestRoundTrip covers save followed by load, which is the whole point of the
// file: a model must not look new again on the next run.
func TestRoundTrip(t *testing.T) {
	path := tempStore(t)
	now := time.Now().UTC()

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Record("openrouter", []string{"a", "b"}, now)
	s.Record("kilocode", []string{"k1"}, now)

	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fresh := reloaded.Record("openrouter", []string{"a", "b", "c"}, now.Add(time.Hour))
	if len(fresh) != 1 || fresh[0] != "c" {
		t.Errorf("after reload, Record reported %v, want [c]", fresh)
	}
	if !reloaded.Known("openrouter")["a"] {
		t.Error("a should still be known after reload")
	}
	if !reloaded.Known("kilocode")["k1"] {
		t.Error("kilocode data did not survive the round trip")
	}
}

// TestSaveIsAtomic checks that saving leaves no temporary files behind, which is
// what makes a crash mid-write recoverable.
func TestSaveIsAtomic(t *testing.T) {
	path := tempStore(t)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Record("openrouter", []string{"a"}, time.Now().UTC())
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the store file", names)
	}
}

// TestSaveCreatesMissingDirectory covers the first save, when the per-user
// cache directory does not exist yet.
func TestSaveCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", FileName)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Record("openrouter", []string{"a"}, time.Now().UTC())

	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store not written: %v", err)
	}
}

// TestCorruptStoreDegradesToEmpty keeps a damaged cache from making the tool
// unusable. The cost is that models look new again, which is far better than
// refusing to start.
func TestCorruptStoreDegradesToEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"truncated json": `{"version":1,"models":`,
		"wrong type":     `["not","an","object"]`,
		"empty":          ``,
		"random bytes":   "\x00\x01\x02\xff",
	} {
		t.Run(name, func(t *testing.T) {
			path := tempStore(t)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Load(path)
			if err != nil {
				t.Fatalf("Load returned %v for a corrupt store, want nil", err)
			}
			if s == nil || len(s.Models) != 0 {
				t.Errorf("corrupt store should load as empty, got %+v", s)
			}
		})
	}
}

// TestFutureVersionIsIgnored means a store written by a newer getfm is treated
// as absent rather than misread.
func TestFutureVersionIsIgnored(t *testing.T) {
	path := tempStore(t)
	body, err := json.Marshal(map[string]any{
		"version": Version + 1,
		"models":  map[string]any{"openrouter": map[string]any{"seen": map[string]string{"a": "2026-01-01T00:00:00Z"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Known("openrouter")) != 0 {
		t.Error("a store from a newer version should be ignored, not merged")
	}
}

// TestKnownReturnsACopy catches a caller mutating the store through the map it
// was handed.
func TestKnownReturnsACopy(t *testing.T) {
	s, err := Load(tempStore(t))
	if err != nil {
		t.Fatal(err)
	}
	s.Record("openrouter", []string{"a"}, time.Now().UTC())

	known := s.Known("openrouter")
	known["a"] = false
	known["injected"] = true

	if !s.Known("openrouter")["a"] {
		t.Error("mutating the returned map changed the store")
	}
	if s.Known("openrouter")["injected"] {
		t.Error("the returned map aliases the store's own map")
	}
}

// TestNilStoreIsInert lets the browser run without persistence at all.
func TestNilStoreIsInert(t *testing.T) {
	var s *Store
	if got := s.Known("openrouter"); got != nil {
		t.Errorf("Known on a nil store = %v, want nil", got)
	}
	if got := s.Record("openrouter", []string{"a"}, time.Now()); got != nil {
		t.Errorf("Record on a nil store = %v, want nil", got)
	}
}

func TestDefaultPathIsPortable(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Skipf("no user cache dir on this platform: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("DefaultPath = %q, want an absolute path", path)
	}
	if filepath.Base(path) != FileName {
		t.Errorf("DefaultPath = %q, want it to end in %q", path, FileName)
	}
}
