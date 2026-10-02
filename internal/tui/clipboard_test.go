package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"testing"

	"github.com/bluespada/getfm/pkg/provider"
)

// recorder captures clipboard writes instead of touching a real one.
type recorder struct {
	copied []string
	err    error
}

func (r *recorder) copy(text string) error {
	if r.err != nil {
		return r.err
	}
	r.copied = append(r.copied, text)
	return nil
}

func withClipboard(m *model, r *recorder) *model {
	m.opts.Copy = r.copy
	return m
}

func loadedModel(t *testing.T) *model {
	t.Helper()
	m := testModel(t)
	send(m, fetchMsg{results: []provider.FetchResult{
		fetchResult("alpha", "alpha-one:free", "alpha-two:free"),
	}})
	return m
}

// TestCopyPutsTheModelIdOnTheClipboard is the feature: c copies the id of the
// model under the cursor, not the row text or the provider/id pair.
func TestCopyPutsTheModelIdOnTheClipboard(t *testing.T) {
	rec := &recorder{}
	m := withClipboard(loadedModel(t), rec)

	send(m, press("c"))

	if len(rec.copied) != 1 {
		t.Fatalf("clipboard received %v, want exactly one copy", rec.copied)
	}
	if rec.copied[0] != "alpha-one:free" {
		t.Errorf("copied %q, want %q", rec.copied[0], "alpha-one:free")
	}
	if m.status != "copied alpha-one:free" {
		t.Errorf("status = %q, want it to confirm the copy", m.status)
	}
}

// TestCtrlCCopiesToo covers the second binding.
func TestCtrlCCopiesToo(t *testing.T) {
	rec := &recorder{}
	m := withClipboard(loadedModel(t), rec)

	send(m, press("ctrl+c"))

	if len(rec.copied) != 1 || rec.copied[0] != "alpha-one:free" {
		t.Errorf("ctrl+c copied %v, want the selected model id", rec.copied)
	}
}

// TestCtrlCNoLongerQuits is the behaviour change this feature makes, pinned so
// a future refactor cannot silently turn it back into an exit.
func TestCtrlCNoLongerQuits(t *testing.T) {
	m := withClipboard(loadedModel(t), &recorder{})

	if _, cmd := m.Update(press("ctrl+c")); isQuit(cmd) {
		t.Error("ctrl+c quit the browser; it should copy instead")
	}
}

// TestCopyFollowsTheCursor checks c acts on whatever is selected, not the
// first row.
func TestCopyFollowsTheCursor(t *testing.T) {
	rec := &recorder{}
	m := withClipboard(loadedModel(t), rec)

	send(m, press("j"))
	send(m, press("c"))

	if len(rec.copied) != 1 {
		t.Fatalf("clipboard received %v, want one copy", rec.copied)
	}
	if rec.copied[0] != "alpha-two:free" {
		t.Errorf("copied %q after moving down, want the second model", rec.copied[0])
	}
}

// TestCopyWithNothingSelected keeps the key harmless on an empty list.
func TestCopyWithNothingSelected(t *testing.T) {
	rec := &recorder{}
	m := withClipboard(testModel(t), rec)

	send(m, press("c"))

	if len(rec.copied) != 0 {
		t.Errorf("copied %v with no model selected, want nothing", rec.copied)
	}
	if !strings.Contains(m.status, "nothing selected") {
		t.Errorf("status = %q, want it to say there was nothing to copy", m.status)
	}
}

// TestCopyFailureIsReported covers a headless machine with no clipboard tool,
// which is a normal way to run this.
func TestCopyFailureIsReported(t *testing.T) {
	rec := &recorder{err: errors.New("no clipboard tool found")}
	m := withClipboard(loadedModel(t), rec)

	send(m, press("c"))

	if !strings.Contains(m.status, "copy failed") {
		t.Errorf("status = %q, want the failure surfaced", m.status)
	}
	if !strings.Contains(m.status, "no clipboard tool") {
		t.Errorf("status = %q, want the reason included", m.status)
	}
}

// TestCopyDoesNotFireInSearchMode matters because ctrl+c keeps its meaning
// there: cancel the prompt, not copy.
func TestCopyDoesNotFireInSearchMode(t *testing.T) {
	rec := &recorder{}
	m := withClipboard(loadedModel(t), rec)

	send(m, press("/"))
	send(m, press("c"))

	if len(rec.copied) != 0 {
		t.Errorf("c copied %v while searching, want the key typed instead", rec.copied)
	}
}

// TestCopyDoesNotFireInModal keeps the card from stealing the key.
func TestCopyDoesNotFireInModal(t *testing.T) {
	rec := &recorder{}
	m := withClipboard(loadedModel(t), rec)

	send(m, press("enter"))
	send(m, press("c"))

	if len(rec.copied) != 0 {
		t.Errorf("c copied %v while a card was open, want nothing", rec.copied)
	}
}

// TestQuitStillWorks guards the replacement bindings after ctrl+c was taken
// over by copy.
func TestQuitStillWorks(t *testing.T) {
	for _, k := range []string{"q", "ctrl+q"} {
		m := testModel(t)
		_, cmd := m.Update(press(k))
		if !isQuit(cmd) {
			t.Errorf("%s did not quit", k)
		}
	}
}

// isQuit reports whether a command is the standard quit.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestCtrlCCancelsInSearchAndCard pins the other two meanings ctrl+c has. The
// README documents it as "cancel" wherever a prompt or the card has the
// keyboard, and only copy in normal mode, so it must never quit from them.
func TestCtrlCCancelsInSearchAndCard(t *testing.T) {
	t.Run("search prompt", func(t *testing.T) {
		m := withClipboard(loadedModel(t), &recorder{})
		send(m, press("/"))
		send(m, press("zeta"))

		_, cmd := m.Update(press("ctrl+c"))
		if isQuit(cmd) {
			t.Error("ctrl+c quit from the search prompt")
		}
		if m.mode != modeNormal {
			t.Errorf("mode = %v, want normal after cancelling the prompt", m.mode)
		}
		if m.search.Value() != "" {
			t.Errorf("query = %q, want it cleared", m.search.Value())
		}
	})

	t.Run("model card", func(t *testing.T) {
		m := withClipboard(loadedModel(t), &recorder{})
		send(m, press("enter"))
		if !m.modal.open {
			t.Fatal("the card did not open")
		}

		_, cmd := m.Update(press("ctrl+c"))
		if isQuit(cmd) {
			t.Error("ctrl+c quit from the card")
		}
		if m.modal.open {
			t.Error("ctrl+c should have closed the card")
		}
		if m.mode != modeNormal {
			t.Errorf("mode = %v, want normal after closing the card", m.mode)
		}
	})
}

// TestCopyDefaultsToTheSystemClipboard makes sure a model built without an
// injected copier still has one, rather than panicking on a nil call.
func TestCopyDefaultsToTheSystemClipboard(t *testing.T) {
	m := testModel(t)
	if m.opts.Copy == nil {
		t.Fatal("newModel left opts.Copy nil")
	}
}
