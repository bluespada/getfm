package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bluespada/getfm/pkg/probe"
)

// keyMap describes the normal-mode bindings. vim conventions are the default,
// with the arrows and control keys working alongside them.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Top      key.Binding
	Bottom   key.Binding
	HalfDown key.Binding
	HalfUp   key.Binding
	Search   key.Binding
	Clear    key.Binding
	Free     key.Binding
	Test     key.Binding
	TestAll  key.Binding
	Refresh  key.Binding
	Save     key.Binding
	Copy     key.Binding
	Open     key.Binding
	Quit     key.Binding
}

var keys = keyMap{
	Up:       key.NewBinding(key.WithKeys("k", "up")),
	Down:     key.NewBinding(key.WithKeys("j", "down")),
	Top:      key.NewBinding(key.WithKeys("g", "home")),
	Bottom:   key.NewBinding(key.WithKeys("G", "end")),
	HalfDown: key.NewBinding(key.WithKeys("ctrl+d")),
	HalfUp:   key.NewBinding(key.WithKeys("ctrl+u")),
	Search:   key.NewBinding(key.WithKeys("/")),
	Clear:    key.NewBinding(key.WithKeys("esc")),
	Free:     key.NewBinding(key.WithKeys("f")),
	Test:     key.NewBinding(key.WithKeys("t")),
	TestAll:  key.NewBinding(key.WithKeys("T")),
	Refresh:  key.NewBinding(key.WithKeys("r")),
	Save:     key.NewBinding(key.WithKeys("S")),
	Copy:     key.NewBinding(key.WithKeys("c", "ctrl+c")),
	Open:     key.NewBinding(key.WithKeys("enter")),
	// ctrl+c copies the model, so quitting keeps q and gains ctrl+q.
	Quit: key.NewBinding(key.WithKeys("q", "ctrl+q")),
}

// pageSize is how far ctrl+d and ctrl+u move.
func (m *model) pageSize() int {
	n := m.list.Height() / 2
	if n < 1 {
		n = 1
	}
	return n
}

// selectEdge moves the cursor to the first or last row. An empty list has no
// row to move to, and Select(-1) would leave the list with no cursor at all,
// so the bounds are checked here rather than at each call site.
func (m *model) selectEdge(index int) {
	if len(m.visible) == 0 {
		return
	}
	m.list.Select(min(max(index, 0), len(m.visible)-1))
}

func (m *model) moveDown(n int) {
	for range n {
		m.list.CursorDown()
	}
}

func (m *model) moveUp(n int) {
	for range n {
		m.list.CursorUp()
	}
}

// handleNormal processes a key press in normal mode.
func (m *model) handleNormal(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()

	// "gg" is a two-key sequence: the first g only arms the pending state and
	// the second jumps. Handling it before the switch keeps the rest of
	// keys.Top, which is home, unambiguous.
	if k == "g" {
		if m.pendingG {
			m.pendingG = false
			m.selectEdge(0)
			return nil
		}
		m.pendingG = true
		return nil
	}
	m.pendingG = false

	// Enter accepts an active search and then opens the card, which is what
	// someone who searched for a model almost always wants next.
	if key.Matches(msg, keys.Open) {
		m.exitSearch()
		return m.openModalForSelected()
	}

	switch {
	case key.Matches(msg, keys.Quit):
		return tea.Quit
	case key.Matches(msg, keys.Up):
		m.moveUp(1)
	case key.Matches(msg, keys.Down):
		m.moveDown(1)
	case key.Matches(msg, keys.HalfUp):
		m.moveUp(m.pageSize())
	case key.Matches(msg, keys.HalfDown):
		m.moveDown(m.pageSize())
	case key.Matches(msg, keys.Top):
		m.selectEdge(0)
	case key.Matches(msg, keys.Bottom):
		m.selectEdge(len(m.visible) - 1)
	case key.Matches(msg, keys.Search):
		m.enterSearch()
	case key.Matches(msg, keys.Clear):
		if m.query() != "" {
			m.search.SetValue("")
			return m.rebuild()
		}
	case key.Matches(msg, keys.Free):
		m.freeOnly = !m.freeOnly
		m.status = fmt.Sprintf("showing %s", scopeWord(m.freeOnly))
		return m.rebuild()
	case key.Matches(msg, keys.Copy):
		m.copySelected()
		return nil
	case key.Matches(msg, keys.Test):
		return m.probeSelected()
	case key.Matches(msg, keys.TestAll):
		return m.probeAll()
	case key.Matches(msg, keys.Refresh):
		return m.refresh(false)
	case key.Matches(msg, keys.Save):
		return m.refresh(true)
	}
	return nil
}

// handleSearch passes keys to the query line until the user commits or cancels.
func (m *model) handleSearch(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Here ctrl+c means "abandon what I was typing" rather than "quit", the
		// same as esc, so it clears the query and leaves search mode.
		m.search.SetValue("")
		m.exitSearch()
		return m.rebuild()
	case "enter":
		m.exitSearch()
		return nil
	}

	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	return tea.Batch(cmd, m.rebuild())
}

func (m *model) enterSearch() {
	m.mode = modeSearch
	m.search.CursorEnd()
	m.search.Focus()
}

func (m *model) exitSearch() {
	m.mode = modeNormal
	m.search.Blur()
}

// probeSelected runs one probe against the model under the cursor.
func (m *model) probeSelected() tea.Cmd {
	model, ok := m.selected()
	if !ok {
		m.status = "nothing selected"
		return nil
	}
	req, ok := m.request(model)
	if !ok {
		m.status = fmt.Sprintf("%s cannot be probed: no completions endpoint", model.Provider)
		return nil
	}
	m.probing[probeKey(req.Provider.Name, req.Model)] = true
	m.status = "probing " + req.Provider.Name + "/" + req.Model
	return tea.Batch(probeCmds(m.ctx, m.opts, []probe.Request{req})...)
}

// probeAll probes every model currently visible.
func (m *model) probeAll() tea.Cmd {
	reqs := m.requestsFor(m.visible)
	if len(reqs) == 0 {
		m.status = "nothing to probe"
		return nil
	}
	for _, r := range reqs {
		m.probing[probeKey(r.Provider.Name, r.Model)] = true
	}
	m.status = fmt.Sprintf("probing %d models", len(reqs))
	return tea.Batch(probeCmds(m.ctx, m.opts, reqs)...)
}

// refresh reloads every catalog. When persist is set the result is written to
// the store once the fetch lands, so what gets recorded is what was actually
// served rather than what was asked for.
func (m *model) refresh(persist bool) tea.Cmd {
	m.loading = true
	if persist {
		m.persistAfterFetch = true
		m.status = "refreshing and saving"
	} else {
		m.status = "refreshing catalogs"
	}
	return fetchCmd(m.ctx, m.opts)
}

// helpLine describes the bindings for the current mode, dropping the tail
// bindings until the line fits so nothing spills past the terminal edge.
func (m *model) helpLine() string {
	if m.mode == modeSearch {
		return trimLine(helpStyle.Render("type to filter · enter accept · esc clear and exit"), m.width)
	}
	parts := []string{
		"j/k move", "gg/G ends", "ctrl-d/ctrl-u page", "/ search",
		"enter card", "f free-only", "t test", "T test all", "c copy", "r refresh", "S save", "q quit",
	}
	sep := " · "
	for len(parts) > 1 {
		line := helpStyle.Render(strings.Join(parts, sep))
		if lipgloss.Width(line) <= m.width {
			return line
		}
		parts = parts[:len(parts)-1]
	}
	return trimLine(helpStyle.Render("q quit"), m.width)
}

func scopeWord(freeOnly bool) string {
	if freeOnly {
		return "free models"
	}
	return "all models"
}
