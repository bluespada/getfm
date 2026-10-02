package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bluespada/getfm/pkg/provider"
	"github.com/bluespada/getfm/pkg/store"
)

// errFetch stands in for a provider that could not be reached.
var errFetch = errors.New("network unreachable")

// pressKey applies a keystroke to the model.
func pressKey(m *model, s string) { send(m, press(s)) }

// withStore attaches a store holding the given ids for each provider.
func withStore(m *model, known map[string][]string) *model {
	s := &store.Store{Version: store.Version, Models: map[string]store.ProviderRecord{}}
	now := time.Now().UTC()
	for name, ids := range known {
		s.Record(name, ids, now)
	}
	m.store = s
	m.hasStore = true
	// Rebuild the snapshot the same way newModel does, so the test exercises
	// the real path rather than a shortcut.
	m.prior = map[string]map[string]bool{}
	m.newIDs = map[string]map[string]bool{}
	for _, p := range m.opts.Catalog.Providers {
		k := map[string]bool{}
		for id := range s.Known(p.Name) {
			k[id] = true
		}
		m.prior[p.Name] = k
		m.newIDs[p.Name] = map[string]bool{}
	}
	return m
}

// fetchResult builds a successful provider result. Ids in this file end in
// ":free" so they survive the free-only filter the browser starts in; the real
// rules engine decides, not a hand-set Reason.
func fetchResult(name string, ids ...string) provider.FetchResult {
	models := make([]provider.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, provider.Model{Provider: name, ID: id})
	}
	return provider.FetchResult{Provider: name, Models: models}
}

// TestNewModelsAreFlaggedAgainstTheStartupSnapshot is the behaviour a user sees:
// an id the store did not know is marked new, a known one is not.
func TestNewModelsAreFlaggedAgainstTheStartupSnapshot(t *testing.T) {
	m := withStore(testModel(t), map[string][]string{"alpha": {"a-old:free", "a-also-old:free"}})
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free", "a-also-old:free"),
	}})

	if m.newCount != 1 {
		t.Errorf("newCount = %d, want 1", m.newCount)
	}
	if !m.newIDs["alpha"]["a-new:free"] {
		t.Error("a-new should be flagged as new")
	}
	if m.newIDs["alpha"]["a-old:free"] {
		t.Error("a-old was in the store and should not be new")
	}
}

// TestRefreshKeepsNewMarks is the reason the snapshot is immutable: pressing S
// must not erase the models the refresh just revealed.
func TestRefreshKeepsNewMarks(t *testing.T) {
	m := withStore(testModel(t), map[string][]string{"alpha": {"a-old:free"}})
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free"),
	}})

	// A second fetch of the same catalogue, as happens after pressing S.
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free"),
	}})

	if !m.newIDs["alpha"]["a-new:free"] {
		t.Error("a-new stopped being new after a refresh")
	}
	if m.newCount != 1 {
		t.Errorf("newCount = %d, want it not to double-count on refresh", m.newCount)
	}
}

// TestFailedProviderDoesNotResetItsHistory covers a real risk: a provider that
// fails must not record an empty catalogue, which would make every one of its
// models look new on the next run.
func TestFailedProviderDoesNotResetItsHistory(t *testing.T) {
	m := withStore(testModel(t), map[string][]string{"alpha": {"a-old:free"}})
	send(m, fetchMsg{results: []provider.FetchResult{
		{Provider: "alpha", Err: errFetch},
	}})

	if m.newCount != 0 {
		t.Errorf("newCount = %d, want 0 for a failed fetch", m.newCount)
	}
	rec := m.store.Models["alpha"]
	if len(rec.Seen) != 1 || rec.Seen["a-old:free"].IsZero() {
		t.Errorf("failed fetch erased alpha's record: %+v", rec)
	}
}

// TestNewFlagSurvivesIntoTheList checks the mark actually reaches the rendered
// item, not just the internal map.
func TestNewFlagSurvivesIntoTheList(t *testing.T) {
	m := withStore(testModel(t), map[string][]string{"alpha": {"a-old:free"}})
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free"),
	}})

	var sawNew, sawOld bool
	for _, item := range m.list.Items() {
		it, ok := item.(modelItem)
		if !ok {
			t.Fatalf("list holds %T, want modelItem", item)
		}
		switch it.m.ID {
		case "a-new:free":
			sawNew = it.isNew
		case "a-old:free":
			sawOld = it.isNew
		}
	}
	if !sawNew {
		t.Error("a-new reached the list without its new mark")
	}
	if sawOld {
		t.Error("a-old reached the list marked as new")
	}
}

// TestSaveIsDeferredUntilTheFetchLands checks shift+S does not write a store
// describing the previous fetch.
func TestSaveIsDeferredUntilTheFetchLands(t *testing.T) {
	m := testModel(t)
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free"),
	}})

	pressKey(m, "S")
	if !m.persistAfterFetch {
		t.Fatal("S did not arm the persist flag")
	}
	if m.status != "refreshing and saving" {
		t.Errorf("status = %q, want it to say it is saving", m.status)
	}

	// The write happens when the fetch comes back, not before.
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free"),
	}})
	if m.persistAfterFetch {
		t.Error("the persist flag was never cleared after the fetch landed")
	}
	if !strings.Contains(m.status, m.storePath) {
		t.Errorf("status = %q, want it to name the store path %q", m.status, m.storePath)
	}
}

// TestPlainRefreshDoesNotPersist keeps r from quietly writing the store.
func TestPlainRefreshDoesNotPersist(t *testing.T) {
	m := testModel(t)
	pressKey(m, "r")

	if m.persistAfterFetch {
		t.Error("r armed the persist flag; only S should write the store")
	}
	if m.status != "refreshing catalogs" {
		t.Errorf("status = %q, want the plain refresh message", m.status)
	}
}

// TestNewCountAppearsInTheHeader confirms the summary line reports the count.
func TestNewCountAppearsInTheHeader(t *testing.T) {
	m := withStore(testModel(t), map[string][]string{"alpha": {"a-old:free"}})
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free"),
	}})

	if got := m.headerView(); !strings.Contains(got, "1 new") {
		t.Errorf("header %q does not mention the new model", got)
	}
	if got := newCountLabel(1); got != "1 new" {
		t.Errorf("newCountLabel(1) = %q", got)
	}
	if got := newCountLabel(3); got != "3 new" {
		t.Errorf("newCountLabel(3) = %q", got)
	}
}

// TestBrowserWorksWithoutAStore is the fallback path: no store, no crash, and
// nothing is claimed to be new.
func TestBrowserWorksWithoutAStore(t *testing.T) {
	m := testModel(t)
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-1", "a-2"),
	}})

	if m.newCount != 0 {
		t.Errorf("newCount = %d, want 0 without a store", m.newCount)
	}
	if got := m.headerView(); strings.Contains(got, "new") {
		t.Errorf("header %q claims new models without a store", got)
	}
	if err := m.saveStore(); err != nil {
		t.Errorf("saveStore without a store returned %v, want nil", err)
	}
}

// TestPersistWritesWhatWasFetched ties the whole feature together: after S, the
// file on disk knows the catalogue, so a later run sees nothing as new.
func TestPersistWritesWhatWasFetched(t *testing.T) {
	m := testModel(t)
	m.store = &store.Store{Version: store.Version, Models: map[string]store.ProviderRecord{}}
	m.storePath = t.TempDir() + "/models-store.json"
	withStore(m, nil)

	pressKey(m, "S")
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "a-old:free", "a-new:free"),
	}})

	reloaded, err := store.Load(m.storePath)
	if err != nil {
		t.Fatal(err)
	}
	fresh := reloaded.Record("alpha", []string{"a-old:free", "a-new:free"}, time.Now())
	if len(fresh) != 0 {
		t.Errorf("re-reading the saved store reported %v as new, want none", fresh)
	}
}
