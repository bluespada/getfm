package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// renderAt sizes the browser to a terminal and returns the rendered frame.
func renderAt(t *testing.T, w, h int) string {
	t.Helper()
	m := testModel(t)
	send(m, tea.WindowSizeMsg{Width: w, Height: h})
	fetch(m)
	return m.View()
}

// TestFrameFitsTerminal is the layout regression test: the frame must occupy
// exactly the terminal height, and no line may spill past its width. Styling is
// stripped first so the measurement reflects printable content.
func TestFrameFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}, {160, 50}, {70, 20}} {
		w, h := size[0], size[1]
		frame := renderAt(t, w, h)
		text := plain(frame)
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")

		if len(lines) != h {
			t.Errorf("%dx%d: frame is %d lines, want %d", w, h, len(lines), h)
		}
		for i, line := range lines {
			if visible := lipgloss.Width(line); visible > w {
				t.Errorf("%dx%d: line %d is %d wide, want <= %d: %q",
					w, h, i+1, visible, w, line)
			}
		}
	}
}

// TestFrameIsStableWithNoModels checks the empty state still fills the screen,
// so the layout does not collapse before the first fetch lands.
func TestFrameIsStableWithNoModels(t *testing.T) {
	m := testModel(t)
	send(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	plainText := plain(m.View())
	lines := strings.Split(strings.TrimRight(plainText, "\n"), "\n")
	if len(lines) != 30 {
		t.Errorf("empty frame is %d lines, want 30", len(lines))
	}
	if !strings.Contains(plainText, "no models match") {
		t.Error("empty state should say so")
	}
}

// TestDetailPaneShowsSelection checks the pane reports real values rather than
// truncating everything to a placeholder width.
func TestDetailPaneShowsSelection(t *testing.T) {
	m := testModel(t)
	send(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	fetch(m)

	text := plain(m.View())
	if !strings.Contains(text, "zeta/one:free") {
		t.Error("detail pane should name the selected model")
	}
	if !strings.Contains(text, "not tested") {
		t.Error("detail pane should invite the user to probe")
	}
	// The pane must not have shrunk values into ellipses at a narrow width.
	if strings.Contains(text, "openroute…") {
		t.Error("detail pane truncated the provider name; pane width is wrong")
	}
}

// TestNarrowTerminalStillRenders guards the minimum-size path.
func TestNarrowTerminalStillRenders(t *testing.T) {
	frame := renderAt(t, 40, 12)
	text := plain(frame)
	if text == "" {
		t.Fatal("frame rendered nothing at 40x12")
	}
	for i, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if lipgloss.Width(line) > 40 {
			t.Errorf("line %d overflows: %q", i+1, line)
		}
	}
}
