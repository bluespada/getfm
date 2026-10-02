package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var modalBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colAccent).
	Padding(0, 1)

var modalTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
var modalHintStyle = lipgloss.NewStyle().Foreground(colMuted)

// renderBox draws the overlay: a title row showing the current page, the
// scrollable body, and a hint row.
func (d *modal) renderBox() string {
	var b strings.Builder

	title := modalTitleStyle.Render(pageTitles[d.page])
	if d.page == pageRequest {
		title += modalHintStyle.Render("  (tab: model card)")
	} else {
		title += modalHintStyle.Render("  (tab: request / response)")
	}
	b.WriteString(title + "\n")
	b.WriteString(d.view.View() + "\n")
	b.WriteString(modalHintStyle.Render("j/k scroll · tab page · enter test · esc close"))

	// Width pins the content block so the overlay is a predictable size; the
	// border and padding then add to it to give d.width overall.
	return modalBoxStyle.Width(d.contentWidth()).Render(b.String())
}

// overlay centers box over the frame. The box has an opaque background, so the
// rows it covers are replaced rather than composited; that keeps the result
// readable without splicing styled text character by character.
func overlay(frame, box string, termW, termH int) string {
	fg := strings.Split(frame, "\n")
	bg := strings.Split(box, "\n")

	boxW := lipgloss.Width(bg[0])
	left := (termW - boxW) / 2
	if left < 0 {
		left = 0
	}
	top := (termH - len(bg)) / 2
	if top < 0 {
		top = 0
	}

	for i, line := range bg {
		row := top + i
		if row < 0 || row >= len(fg) {
			continue
		}
		fg[row] = strings.Repeat(" ", left) + line
	}
	return strings.Join(fg, "\n")
}

// handleModal routes keys while the overlay is open.
func (m *model) handleModal(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "ctrl+c", "enter":
		// Enter runs a probe rather than closing, since the card shows the test
		// result and that is the natural thing to do from here. The others
		// close the card: ctrl+c cancels what is on screen rather than quitting,
		// which is what it means while the card has the keyboard.
		if msg.String() == "enter" {
			return m.probeSelected()
		}
		m.closeModal()
		return nil
	case "tab", "right":
		return m.nextPage()
	case "shift+tab", "left":
		m.modal.page = (m.modal.page - 1 + pageCount) % pageCount
		m.modal.syncContent()
		m.modal.view.GotoTop()
		return nil
	case "t":
		return m.probeSelected()
	}

	var cmd tea.Cmd
	m.modal.view, cmd = m.modal.view.Update(msg)
	return cmd
}

// openModalForSelected is the command bound to enter in the list.
func (m *model) openModalForSelected() tea.Cmd {
	sel, ok := m.selected()
	if !ok {
		m.status = "no model selected"
		return nil
	}
	_, known := m.byName[sel.Provider]
	return m.openModal(sel, known)
}
