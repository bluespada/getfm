package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/free"
	"github.com/bluespada/getfm/pkg/provider"
)

const testCatalog = `{
  "providers": [
    {
      "name": "alpha",
      "label": "Alpha",
      "base_url": "https://alpha.example",
      "env_keys": ["ALPHA_KEY"],
      "mapping": {"list": "data", "id": "id", "prompt_price": "pricing.prompt", "completion_price": "pricing.completion"},
      "completions": {"path": "/v1/chat/completions", "auth_header": "Authorization", "auth_prefix": "Bearer ", "body": "{\"model\":\"{{.Model}}\",\"max_tokens\":1}"}
    },
    {
      "name": "beta",
      "label": "Beta",
      "base_url": "https://beta.example",
      "env_keys": [],
      "free_ids": ["beta-declared"],
      "mapping": {"list": "data", "id": "id"},
      "completions": {"path": "/v1/chat/completions", "auth_header": "Authorization", "auth_prefix": "Bearer ", "body": "{\"model\":\"{{.Model}}\"}"}
    }
  ]
}`

func testModel(t *testing.T) *model {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(testCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	rules, err := free.ParseSpec("all", ":free", "", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := newModel(Options{
		Catalog: file,
		Rules:   rules,
		Key:     func(config.Provider) string { return "" },
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	m.resize()
	return m
}

// send applies a message and discards the command, which is enough because
// rebuild mutates state directly.
func send(m *model, msg tea.Msg) {
	_, _ = m.Update(msg)
}

// press builds a real key message. Named keys go through their bubbletea key
// type so a test exercises the same String() the browser matches on, and a
// multi-character name is never mistaken for a rune sequence.
func press(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+q":
		return tea.KeyMsg{Type: tea.KeyCtrlQ}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func models() []provider.Model {
	return []provider.Model{
		{ID: "zeta/one:free", Provider: "alpha", Priced: true, Context: 1000,
			Raw: map[string]any{
				"id":                   "zeta/one:free",
				"name":                 "Zeta One",
				"context_length":       float64(1000),
				"description":          "A model used by the tests.",
				"supported_parameters": []any{"tools", "temperature"},
				"pricing":              map[string]any{"prompt": "0", "completion": "0"},
			}},
		{ID: "zeta/two", Provider: "alpha", Priced: true, PromptPrice: 1,
			Raw: map[string]any{"id": "zeta/two"}},
		{ID: "alpha/paid", Provider: "alpha", Priced: true, PromptPrice: 2,
			Raw: map[string]any{"id": "alpha/paid"}},
	}
}

func fetch(m *model) {
	send(m, fetchMsg{results: []provider.FetchResult{
		{Provider: "alpha", Models: models(), Elapsed: 10},
		{Provider: "beta", Models: []provider.Model{{ID: "beta-declared", Provider: "beta"}}},
	}})
}

func ids(m *model) []string {
	out := make([]string, 0, len(m.visible))
	for _, v := range m.visible {
		out = append(out, v.ID)
	}
	return out
}

func TestFetchShowsFreeModelsOnlyByDefault(t *testing.T) {
	m := testModel(t)
	fetch(m)

	// alpha contributes the ":free" suffix match; beta contributes its
	// declared free id. The paid alpha models must not appear.
	got := ids(m)
	want := map[string]bool{"zeta/one:free": true, "beta-declared": true}
	if len(got) != len(want) {
		t.Fatalf("visible = %v, want %d entries", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected visible model %q", id)
		}
	}
}

func TestToggleFreeShowsEverything(t *testing.T) {
	m := testModel(t)
	fetch(m)

	send(m, press("f"))
	if m.freeOnly {
		t.Fatal("f should have left free-only mode")
	}
	if len(m.visible) != 4 {
		t.Errorf("visible = %v, want all 4 models", ids(m))
	}

	send(m, press("f"))
	if !m.freeOnly {
		t.Fatal("f should have returned to free-only mode")
	}
	if len(m.visible) != 2 {
		t.Errorf("visible = %v, want the 2 free models", ids(m))
	}
}

func TestSearchNarrowsResults(t *testing.T) {
	m := testModel(t)
	fetch(m)

	m.search.SetValue("zeta")
	mode := m.mode
	_ = m.rebuild()
	m.mode = mode

	got := ids(m)
	if len(got) != 1 || got[0] != "zeta/one:free" {
		t.Errorf("visible = %v, want only zeta/one:free", got)
	}

	m.search.SetValue("")
	_ = m.rebuild()
	if len(m.visible) != 2 {
		t.Errorf("clearing the search should restore 2 models, got %v", ids(m))
	}
}

func TestVimMovement(t *testing.T) {
	m := testModel(t)
	fetch(m)
	m.freeOnly = false
	_ = m.rebuild()
	if len(m.visible) != 4 {
		t.Fatalf("need 4 visible models to test movement, got %d", len(m.visible))
	}

	start := m.list.Index()
	if start != 0 {
		t.Fatalf("cursor should start at 0, got %d", start)
	}

	// j and down both move forward.
	send(m, press("j"))
	if m.list.Index() != start+1 {
		t.Errorf("j moved to %d, want %d", m.list.Index(), start+1)
	}
	send(m, press("j"))
	if m.list.Index() != start+2 {
		t.Errorf("j moved to %d, want %d", m.list.Index(), start+2)
	}

	// k moves back.
	send(m, press("k"))
	if m.list.Index() != start+1 {
		t.Errorf("k moved to %d, want %d", m.list.Index(), start+1)
	}

	// A single g must not jump; it only arms the sequence.
	send(m, press("g"))
	if m.list.Index() != start+1 {
		t.Error("a lone g should not move the cursor")
	}
	// gg jumps to the top.
	send(m, press("g"))
	if m.list.Index() != 0 {
		t.Errorf("gg moved to %d, want 0", m.list.Index())
	}

	// G jumps to the bottom.
	send(m, press("G"))
	if m.list.Index() != 3 {
		t.Errorf("G moved to %d, want 3", m.list.Index())
	}

	// Movement must not wrap past either end.
	send(m, press("j"))
	if m.list.Index() != 3 {
		t.Errorf("cursor went past the end to %d", m.list.Index())
	}
}

// TestJumpingToTheEndOfAnEmptyListKeepsTheCursorUsable guards a real trap:
// G on an empty list computes Select(-1), the list never clamps it, and
// clearing the search does not restore it, so rows come back with nothing
// selected and enter, t and c all answer "nothing selected".
func TestJumpingToTheEndOfAnEmptyListKeepsTheCursorUsable(t *testing.T) {
	m := testModel(t)
	fetch(m)

	m.search.SetValue("no-such-model")
	_ = m.rebuild()
	if len(m.visible) != 0 {
		t.Fatalf("expected an empty list, got %v", ids(m))
	}
	send(m, press("G"))

	m.search.SetValue("")
	_ = m.rebuild()
	if len(m.visible) == 0 {
		t.Fatal("clearing the search should restore the models")
	}
	if _, ok := m.selected(); !ok {
		t.Errorf("nothing selected after clearing the search (index %d)", m.list.Index())
	}
	if !strings.Contains(plain(m.View()), "▌") {
		t.Error("no row carries the cursor mark")
	}
}

// TestHomeAndEndDriveTheDeclaredBindings covers the keys keyMap declares for
// the two ends of the list. They are bindings rather than literals so that
// editing the key map actually changes the behaviour.
func TestHomeAndEndDriveTheDeclaredBindings(t *testing.T) {
	m := testModel(t)
	fetch(m)
	m.freeOnly = false
	_ = m.rebuild()
	if len(m.visible) < 2 {
		t.Fatalf("need several rows to test the ends, got %d", len(m.visible))
	}

	send(m, tea.KeyMsg{Type: tea.KeyEnd})
	if got := m.list.Index(); got != len(m.visible)-1 {
		t.Errorf("end moved to %d, want %d", got, len(m.visible)-1)
	}
	send(m, tea.KeyMsg{Type: tea.KeyHome})
	if got := m.list.Index(); got != 0 {
		t.Errorf("home moved to %d, want 0", got)
	}
}

func TestSearchModeCapturesTyping(t *testing.T) {
	m := testModel(t)
	fetch(m)

	// / enters search mode, where plain letters are query text, not commands.
	send(m, press("/"))
	if m.mode != modeSearch {
		t.Fatal("/ should enter search mode")
	}
	send(m, press("q"))
	if m.mode != modeSearch {
		t.Error("typing in search mode must not quit")
	}
	if m.search.Value() != "q" {
		t.Errorf("query = %q, want %q", m.search.Value(), "q")
	}
	if len(m.visible) != 0 {
		t.Errorf("no model matches q, but %v are visible", ids(m))
	}
}

func TestEnterLeavesSearchModeKeepingQuery(t *testing.T) {
	m := testModel(t)
	fetch(m)

	send(m, press("/"))
	send(m, press("zeta"))
	send(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.mode != modeNormal {
		t.Fatal("enter should return to normal mode")
	}
	if m.search.Value() != "zeta" {
		t.Errorf("query after enter = %q, want it kept", m.search.Value())
	}
	if len(m.visible) != 1 {
		t.Errorf("visible = %v, want the filter still applied", ids(m))
	}
}

func TestProbeResultIsRecorded(t *testing.T) {
	m := testModel(t)
	fetch(m)

	m.probing[probeKey("alpha", "zeta/one:free")] = true
	if !m.probeRunning() {
		t.Fatal("probe should be reported as running")
	}

	send(m, probeMsg{
		key: probeKey("alpha", "zeta/one:free"),
		item: probeKeyed{
			Model:   provider.Model{Provider: "alpha", ID: "zeta/one:free"},
			OK:      true,
			Status:  200,
			Latency: 120 * time.Millisecond,
			Tokens:  7,
		},
	})

	if m.probeRunning() {
		t.Error("probe should no longer be running")
	}
	got, ok := m.probes[probeKey("alpha", "zeta/one:free")]
	if !ok {
		t.Fatal("probe result was not recorded")
	}
	if !got.OK || got.Status != 200 || got.Tokens != 7 {
		t.Errorf("unexpected recorded probe: %+v", got)
	}
}

func TestFailedProviderIsReportedNotHidden(t *testing.T) {
	m := testModel(t)
	send(m, fetchMsg{results: []provider.FetchResult{
		{Provider: "alpha", Models: models()},
		{Provider: "beta", Err: errBoom{}},
	}})

	if m.status == "" {
		t.Error("a failed provider should produce a status message")
	}
	if len(m.providerErrors()) != 1 {
		t.Errorf("providerErrors = %v, want exactly the failure", m.providerErrors())
	}
	// The healthy provider still contributes its free models.
	if len(m.visible) != 1 {
		t.Errorf("visible = %v, want the healthy provider's free model", ids(m))
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom: dial tcp: connection refused" }

func TestSelectedTracksCursor(t *testing.T) {
	m := testModel(t)
	fetch(m)
	m.freeOnly = false
	_ = m.rebuild()

	sel, ok := m.selected()
	if !ok {
		t.Fatal("nothing selected with 4 models visible")
	}
	if sel.ID != "zeta/one:free" {
		t.Errorf("first model = %q", sel.ID)
	}

	send(m, press("j"))
	sel, _ = m.selected()
	if sel.ID != "zeta/two" {
		t.Errorf("after j the selection is %q, want zeta/two", sel.ID)
	}
}
