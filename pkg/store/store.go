// Package store persists the model catalogue between runs.
//
// Its purpose is answering "what changed since last time". On every refresh the
// tool records which models a provider offered; on the next run anything that was
// not in that record is flagged as new. The record lives in the user cache
// directory because it is derived entirely from the providers and can be thrown
// away and rebuilt at any time.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bluespada/getfm/pkg/paths"
)

// Version is the schema version. A store written by a newer getfm is ignored
// rather than misread.
const Version = 1

// FileName is the store's name inside the cache directory.
const FileName = "models-store.json"

// Store is the on-disk record of every model seen, per provider.
type Store struct {
	Version int `json:"version"`
	// Updated is when the file was last written. It is part of the on-disk
	// format, so a stale cache can be recognised by hand; nothing in getfm
	// reads it back.
	Updated time.Time                 `json:"updated"`
	Models  map[string]ProviderRecord `json:"models"`
}

// ProviderRecord tracks one provider's catalogue.
type ProviderRecord struct {
	// Updated is when this provider's record was last replaced. It stays in the
	// file for the same reason the store's own timestamp does.
	Updated time.Time `json:"updated"`
	// Seen maps a model id to the first time it was observed. Keeping the
	// original timestamp rather than overwriting it is what makes the history
	// meaningful after several refreshes.
	Seen map[string]time.Time `json:"seen"`
}

// DefaultPath is where the store lives for the current platform.
func DefaultPath() (string, error) {
	dir, err := paths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// Load reads the store. A missing file is not an error: it yields an empty
// store, which makes the first run report everything as new. A file that cannot
// be parsed is also treated as empty, because refusing to start over a corrupt
// cache would be worse than rebuilding it.
func Load(path string) (*Store, error) {
	s := newStore()

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var onDisk Store
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		return s, nil
	}
	if onDisk.Version != Version {
		return s, nil
	}
	if onDisk.Models == nil {
		onDisk.Models = map[string]ProviderRecord{}
	}
	return &onDisk, nil
}

// Save writes the store atomically, so an interrupted write cannot leave a
// half-written file that the next run would reject.
func (s *Store) Save(path string) error {
	dir := filepath.Dir(path)
	if err := paths.EnsureDir(dir); err != nil {
		return err
	}

	s.Version = Version
	s.Updated = time.Now().UTC()

	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".models-store-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace store: %w", err)
	}
	return nil
}

func newStore() *Store {
	return &Store{Version: Version, Models: map[string]ProviderRecord{}}
}

// Known reports the ids previously recorded for a provider. The result is a
// copy, so a caller comparing against a snapshot cannot mutate the record.
func (s *Store) Known(provider string) map[string]bool {
	if s == nil {
		return nil
	}
	rec, ok := s.Models[provider]
	if !ok {
		return nil
	}
	out := make(map[string]bool, len(rec.Seen))
	for id := range rec.Seen {
		out[id] = true
	}
	return out
}

// Record replaces a provider's entry with the catalogue just fetched and
// returns the ids that had not been seen before.
//
// The record is a snapshot rather than an append-only log, so a model a provider
// has dropped stops being known and a file that tracks a churning catalogue
// stays a reasonable size. First-seen times survive for models that persist,
// which is what makes the history meaningful across several refreshes.
func (s *Store) Record(provider string, ids []string, now time.Time) []string {
	if s == nil {
		return nil
	}
	if s.Models == nil {
		s.Models = map[string]ProviderRecord{}
	}

	previous := s.Models[provider].Seen
	if previous == nil {
		previous = map[string]time.Time{}
	}

	seen := make(map[string]time.Time, len(ids))
	var fresh []string
	for _, id := range ids {
		if first, existed := previous[id]; existed {
			seen[id] = first
			continue
		}
		seen[id] = now
		fresh = append(fresh, id)
	}

	s.Models[provider] = ProviderRecord{Updated: now, Seen: seen}
	return fresh
}
