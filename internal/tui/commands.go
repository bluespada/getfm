package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bluespada/getfm/pkg/probe"
	"github.com/bluespada/getfm/pkg/provider"
)

// rebuild recomputes the visible set from the catalogs, the free-only toggle
// and the search query, then hands it to the list.
func (m *model) rebuild() tea.Cmd {
	var items []list.Item
	visible := make([]provider.Model, 0, len(m.visible))

	for _, p := range m.opts.Catalog.Providers {
		source := m.freeModels[p.Name]
		if !m.freeOnly {
			source = m.allModels[p.Name]
		}
		for _, mm := range source {
			if !m.matches(mm) {
				continue
			}
			visible = append(visible, mm)
			items = append(items, modelItem{
				m:     mm,
				isNew: m.newIDs[p.Name][mm.ID],
			})
		}
	}

	m.visible = visible
	m.relayout()
	return m.list.SetItems(items)
}

// fetchMsg carries the outcome of a catalog fetch.
type fetchMsg struct{ results []provider.FetchResult }

// probeMsg carries the outcome of one probe.
type probeMsg struct {
	key  string
	item probeKeyed
}

// fetchCmd loads every catalog concurrently.
func fetchCmd(ctx context.Context, opts Options) tea.Cmd {
	return func() tea.Msg {
		client := newClient(opts.Timeout)
		return fetchMsg{results: provider.Fetch(ctx, client, opts.Catalog, provider.Options{
			Concurrency: len(opts.Catalog.Providers),
			MaxBytes:    16 << 20,
			Key:         opts.Key,
		})}
	}
}

// probeCmds issues one probe per request. Each returns its own message so the
// browser fills results in as they land rather than waiting for the slowest.
func probeCmds(ctx context.Context, opts Options, reqs []probe.Request) []tea.Cmd {
	client := newClient(opts.Timeout)
	cmds := make([]tea.Cmd, 0, len(reqs))
	for _, req := range reqs {
		req := req
		cmds = append(cmds, func() tea.Msg {
			res := probe.Send(ctx, client, req)
			return probeMsg{
				key: probeKey(res.Provider, res.Model),
				item: probeKeyed{
					Model:    provider.Model{Provider: res.Provider, ID: res.Model},
					OK:       res.OK,
					Status:   res.Status,
					Latency:  res.Latency,
					Tokens:   res.Tokens,
					Detail:   res.Detail,
					Request:  res.Request,
					Response: res.Response,
				},
			}
		})
	}
	return cmds
}

// request builds a probe request for a model.
func (m *model) request(model provider.Model) (probe.Request, bool) {
	p, ok := m.byName[model.Provider]
	if !ok {
		return probe.Request{}, false
	}
	if p.Completions == nil {
		return probe.Request{}, false
	}
	return probe.Request{
		Provider: p,
		Model:    model.ID,
		Ref:      model.Ref,
		Prompt:   m.opts.Prompt,
		Key:      m.opts.Key(p),
	}, true
}

// requestsFor turns models into probe requests, skipping providers that cannot
// be probed.
func (m *model) requestsFor(models []provider.Model) []probe.Request {
	out := make([]probe.Request, 0, len(models))
	for _, model := range models {
		if req, ok := m.request(model); ok {
			out = append(out, req)
		}
	}
	return out
}

// probeRunning reports whether anything is in flight.
func (m *model) probeRunning() bool { return len(m.probing) > 0 }
