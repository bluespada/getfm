package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/probe"
	"github.com/bluespada/getfm/pkg/provider"
)

// enterMsg is the enter key, which is not a rune.
func enterMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func tabMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyTab} }

func readyModel(t *testing.T) *model {
	t.Helper()
	m := testModel(t)
	send(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	fetch(m)
	return m
}

func TestEnterOpensCardAndEscCloses(t *testing.T) {
	m := readyModel(t)

	send(m, enterMsg())
	if !m.modal.open {
		t.Fatal("enter should open the card")
	}
	if m.mode != modeModal {
		t.Errorf("mode = %v, want modal", m.mode)
	}
	if m.modal.page != pageCard {
		t.Errorf("card should open on page %d, got %d", pageCard, m.modal.page)
	}
	if m.modal.model.ID != "zeta/one:free" {
		t.Errorf("card opened for %q, want the selected model", m.modal.model.ID)
	}

	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modal.open {
		t.Error("esc should close the card")
	}
	if m.mode != modeNormal {
		t.Errorf("mode = %v, want normal after closing", m.mode)
	}
}

func TestModalSwallowsKeysSoQClosesRatherThanQuits(t *testing.T) {
	m := readyModel(t)
	send(m, enterMsg())

	// q inside the modal must close it, not quit the program.
	_, cmd := m.Update(press("q"))
	if m.modal.open {
		t.Error("q should close the modal")
	}
	if cmd != nil {
		t.Error("q must not produce a quit command")
	}
}

func TestTabPagesBetweenCardAndRequest(t *testing.T) {
	m := readyModel(t)
	send(m, enterMsg())

	send(m, tabMsg())
	if m.modal.page != pageRequest {
		t.Fatalf("tab moved to page %d, want %d", m.modal.page, pageRequest)
	}

	// Tab wraps back around to the card.
	send(m, tabMsg())
	if m.modal.page != pageCard {
		t.Errorf("second tab landed on %d, want the card", m.modal.page)
	}
}

func TestCardShowsModelFactsAndProviderRecord(t *testing.T) {
	m := readyModel(t)
	send(m, enterMsg())

	body := plain(m.modal.render())
	for _, want := range []string{"zeta/one:free", "context", "free", "provider record", "1k tokens"} {
		if !strings.Contains(body, want) {
			t.Errorf("card is missing %q:\n%s", want, body)
		}
	}
}

func TestRequestPageShowsMethodURLAndBody(t *testing.T) {
	m := readyModel(t)
	send(m, enterMsg())
	send(m, tabMsg())

	body := plain(m.modal.render())
	for _, want := range []string{"POST", "/v1/chat/completions", "body", "content-type"} {
		if !strings.Contains(body, want) {
			t.Errorf("request page is missing %q:\n%s", want, body)
		}
	}
	// The body must be rendered as indented JSON, not one long line.
	if !strings.Contains(body, "\n  {") {
		t.Errorf("request body was not pretty printed:\n%s", body)
	}
}

func TestRequestPageRedactsCredential(t *testing.T) {
	m := testModel(t)
	send(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	fetch(m)

	// Attach a result as if a probe had run with a real key.
	req, ok := m.request(m.visible[0])
	if !ok {
		t.Fatal("could not build a probe request")
	}
	prep, err := probe.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	safe := prep.Safe("Authorization", "Bearer ")
	m.probes[probeKey(m.visible[0].Provider, m.visible[0].ID)] = probeKeyed{
		Model:   m.visible[0],
		OK:      true,
		Request: safe,
		Response: &probe.Exchange{
			Body: `{"usage":{"total_tokens":5}}`,
		},
	}

	send(m, enterMsg())
	send(m, tabMsg())
	body := plain(m.modal.render())

	if !strings.Contains(body, "[redacted]") {
		t.Errorf("credential slot should be marked redacted:\n%s", body)
	}
	if strings.Contains(body, "Bearer sk-") {
		t.Error("request page leaked a credential")
	}
}

func TestRequestPageShowsResponseWhenProbed(t *testing.T) {
	m := readyModel(t)
	sel := m.visible[0]
	m.probes[probeKey(sel.Provider, sel.ID)] = probeKeyed{
		Model:   sel,
		OK:      true,
		Status:  200,
		Latency: 120000000,
		Tokens:  9,
		Request: probe.Prepared{Method: "POST", URL: "https://alpha.example/v1/chat/completions", Body: "{}"},
		Response: &probe.Exchange{
			Body: `{"usage":{"total_tokens":9}}`,
		},
	}

	send(m, enterMsg())
	send(m, tabMsg())
	body := plain(m.modal.render())

	for _, want := range []string{"last probe", "200", "response", "total_tokens"} {
		if !strings.Contains(body, want) {
			t.Errorf("response page is missing %q:\n%s", want, body)
		}
	}
}

func TestModalStaysInsideTerminal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {160, 50}, {60, 18}} {
		w, h := size[0], size[1]
		m := readyModel(t)
		send(m, tea.WindowSizeMsg{Width: w, Height: h})
		fetch(m)
		send(m, enterMsg())

		if m.modal.width > w {
			t.Errorf("%dx%d: modal width %d exceeds terminal", w, h, m.modal.width)
		}
		if m.modal.height > h {
			t.Errorf("%dx%d: modal height %d exceeds terminal", w, h, m.modal.height)
		}

		frame := plain(m.View())
		lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
		for i, line := range lines {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("%dx%d: line %d is %d wide:\n%q", w, h, i+1, got, line)
			}
		}
	}
}

// TestCardFieldsWrapToTheModalWidth guards a regression: the card used to wrap
// its fields at a fixed 68 columns regardless of the terminal, so on a narrow
// screen the viewport clipped them with no sign that anything was missing.
func TestCardFieldsWrapToTheModalWidth(t *testing.T) {
	raw := map[string]any{
		"id":                   "zeta/one:free",
		"description":          strings.Repeat("described at length ", 6),
		"supported_parameters": []any{"tools", "temperature", "top_p", "top_k", "reasoning", "seed", "stop", "logprobs"},
	}
	for _, w := range []int{50, 60, 70, 80, 120} {
		m := testModel(t)
		send(m, tea.WindowSizeMsg{Width: w, Height: 30})
		send(m, fetchMsg{results: []provider.FetchResult{{
			Provider: "alpha",
			Models:   []provider.Model{{ID: "zeta/one:free", Provider: "alpha", Priced: true, Raw: raw}},
		}}})
		send(m, enterMsg())
		if !m.modal.open {
			t.Fatalf("%d columns: the card did not open", w)
		}

		width := m.modal.contentWidth()
		text := plain(m.modal.render())
		for i, line := range strings.Split(text, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("%d columns: card line %d is %d wide, the content box is %d: %q",
					w, i+1, got, width, line)
			}
		}

		// Nothing may be dropped either: the tail of a long value has to
		// survive the wrap.
		flat := strings.Join(strings.Fields(text), " ")
		for _, want := range []string{"seed, stop, logprobs", "described"} {
			if !strings.Contains(flat, want) {
				t.Errorf("%d columns: card lost %q:\n%s", w, want, text)
			}
		}
	}
}

func TestModalDoesNotOpenWithoutAKnownSize(t *testing.T) {
	m := testModel(t)
	fetch(m)
	// Simulate a terminal that has never reported its size.
	m.width, m.height = 0, 0
	send(m, enterMsg())
	if m.modal.open {
		t.Error("modal should stay closed without a known terminal size")
	}
}

func TestCardScrollsLongRecords(t *testing.T) {
	m := readyModel(t)
	// A model with a big provider record must not overflow the viewport.
	sel := m.visible[0]
	sel.Raw = map[string]any{
		"id":                   sel.ID,
		"description":          strings.Repeat("a long description ", 40),
		"supported_parameters": []any{"tools", "reasoning", "temperature"},
	}
	m.visible[0] = sel
	m.list.SetItems(nil)
	_ = m.rebuild()

	send(m, enterMsg())

	// The card is free to be longer than the viewport, which is what the
	// viewport is for, but it must only ever paint its own height.
	full := strings.Split(plain(m.modal.render()), "\n")
	if len(full) <= m.modal.innerHeight() {
		t.Fatalf("test needs content taller than the viewport: %d lines", len(full))
	}
	shown := strings.Split(plain(m.modal.view.View()), "\n")
	if len(shown) != m.modal.innerHeight() {
		t.Errorf("viewport painted %d lines, want %d", len(shown), m.modal.innerHeight())
	}
}

func TestParamsTextSummarisesLongLists(t *testing.T) {
	m := provider.Model{Raw: map[string]any{
		"supported_parameters": []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
	}}
	got := paramsText(m)
	if !strings.Contains(got, "10 supported") {
		t.Errorf("paramsText = %q, want a count", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("paramsText = %q, want it truncated", got)
	}
}
