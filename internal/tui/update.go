package tui

import (
	"fmt"
	"regexp"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/provider"
)

// detailLines is how many content lines the detail pane holds. The pane adds a
// border, so the reserved height is this plus two.
const detailLines = 6

// Init starts the first fetch.
func (m *model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, fetchCmd(m.ctx, m.opts))
}

// Update routes messages.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 && msg.Height > 0 {
			m.width, m.height = msg.Width, msg.Height
		}
		m.resize()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case fetchMsg:
		return m, m.onFetch(msg)

	case probeMsg:
		delete(m.probing, msg.key)
		m.probes[msg.key] = msg.item
		m.status = ""
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case modeSearch:
			return m, m.handleSearch(msg)
		case modeModal:
			return m, m.handleModal(msg)
		default:
			return m, m.handleNormal(msg)
		}
	}
	return m, nil
}

// onFetch applies a completed fetch and rebuilds the list.
func (m *model) onFetch(msg fetchMsg) tea.Cmd {
	m.loading = false
	m.results = msg.results
	now := time.Now().UTC()
	for _, res := range msg.results {
		m.applyFree(res)
		m.recordFetched(res, now)
	}

	failed := 0
	for _, r := range msg.results {
		if r.Err != nil {
			failed++
		}
	}
	switch {
	case failed == 0:
		m.status = ""
	case failed == len(msg.results):
		m.status = "no providers could be reached"
	default:
		m.status = fmt.Sprintf("%d of %d providers unavailable", failed, len(msg.results))
	}

	// Persisting is deliberately last, so a store that records a failed fetch
	// does not lose the models an earlier successful fetch already knew about.
	if m.persistAfterFetch {
		m.persistAfterFetch = false
		if err := m.saveStore(); err != nil {
			m.status = "could not save store: " + shortError(err.Error())
		} else {
			m.status = fmt.Sprintf("saved to %s", m.storePath)
		}
	}
	return m.rebuild()
}

// recordFetched flags models the store had not seen and folds the fetched
// catalogue into the in-memory store.
//
// A failed provider is skipped entirely. Recording its empty result would
// forget every model that provider ever offered, and the next run would
// rediscover all of them as new.
func (m *model) recordFetched(res provider.FetchResult, now time.Time) {
	if res.Err != nil {
		return
	}
	ids := make([]string, 0, len(res.Models))
	for _, mm := range res.Models {
		ids = append(ids, mm.ID)
	}
	// With no store there is nothing to compare against, so nothing is new.
	if m.hasStore {
		known := m.prior[res.Provider]
		fresh := m.newIDs[res.Provider]
		if fresh == nil {
			fresh = map[string]bool{}
			m.newIDs[res.Provider] = fresh
		}
		for _, id := range ids {
			// Counting only ids not already flagged keeps newCount a count of
			// models rather than a count of refreshes that rediscovered them.
			if !known[id] && !fresh[id] {
				fresh[id] = true
				m.newCount++
			}
		}
	}
	m.store.Record(res.Provider, ids, now)
}

// saveStore writes the accumulated record to disk.
func (m *model) saveStore() error {
	if m.store == nil || m.storePath == "" {
		return nil
	}
	return m.store.Save(m.storePath)
}

// resize gives the list whatever height the other sections do not need.
func (m *model) resize() {
	// A zero width or height arrives on some terminals during startup; keeping
	// the previous size avoids collapsing the layout to nothing.
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// header, provider row, search row, bordered detail pane, help row
	const chrome = 1 + 1 + 1 + (detailLines + 2) + 1
	h := m.height - chrome
	if h < 3 {
		h = 3
	}
	m.list.SetSize(m.width, h)
	m.relayout()
}

// scopeLabel names what the list is currently showing.
func (m *model) scopeLabel() string {
	return scopeWord(m.freeOnly)
}

// countLabel is the "showing N free of M" summary.
func (m *model) countLabel() string {
	all, free := 0, 0
	for _, p := range m.opts.Catalog.Providers {
		all += len(m.allModels[p.Name])
		free += len(m.freeModels[p.Name])
	}
	shown := len(m.visible)
	return fmt.Sprintf("%d shown · %d free of %d", shown, free, all)
}

// providerErrors returns the failures keyed by provider for the header.
func (m *model) providerErrors() map[string]string {
	out := map[string]string{}
	for _, r := range m.results {
		if r.Err != nil {
			out[r.Provider] = shortError(r.Err.Error())
		}
	}
	return out
}

// trimLine shortens a single line to a visible width, marking where it was
// cut. Measuring with lipgloss and truncating through it keeps escape
// sequences and multi-byte characters intact, which counting runes would not:
// a rune count sees the escape bytes as characters and cuts far too early,
// and a byte count can split a character in half.
func trimLine(s string, width int) string {
	if width <= 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(s) + "…"
}

func shortError(s string) string {
	return trimLine(s, 60)
}

// ansiPattern matches the escape sequences styling introduces.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*(\x07|\x1b\\)|\x1b[()][B0]`)

// plain removes escape sequences, leaving only printable characters.
func plain(s string) string { return ansiPattern.ReplaceAllString(s, "") }
