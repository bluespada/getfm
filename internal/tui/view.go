package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/provider"
)

// View renders the whole screen.
func (m *model) View() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(m.statusView())
	b.WriteString("\n")
	b.WriteString(m.searchView())
	b.WriteString("\n")
	b.WriteString(m.list.View())
	b.WriteString("\n")
	b.WriteString(m.detailView())
	b.WriteString("\n")
	b.WriteString(m.helpLine())
	frame := b.String()

	if m.modal.open {
		frame = overlay(frame, m.modal.renderBox(), m.width, m.height)
	}
	return frame
}

// headerView is the title plus the provider row. Each provider shows how many
// models it returned and how many are free; a provider that failed is marked
// instead of being hidden, so a gap is never mistaken for "no free models".
func (m *model) headerView() string {
	sub := "  " + m.scopeLabel() + " · " + m.countLabel()
	if m.newCount > 0 {
		sub += " · " + newCountLabel(m.newCount)
	}
	if lipgloss.Width("getfm"+sub) <= m.width {
		return titleStyle.Render("getfm") + labelStyle.Render(sub)
	}
	return trimLine("getfm"+sub, m.width)
}

// newCountLabel reports how many models the store had not seen at startup.
func newCountLabel(n int) string {
	if n == 1 {
		return "1 new"
	}
	return fmt.Sprintf("%d new", n)
}

// providerRow renders one chip per provider. It is appended to the header by
// statusView so the layout stays easy to reason about.
func (m *model) providerRow() string {
	errs := m.providerErrors()
	var chips []string
	for i, p := range m.opts.Catalog.Providers {
		label := fmt.Sprintf("%s %d/%d", p.Name, len(m.allModels[p.Name]), len(m.freeModels[p.Name]))
		style := lipgloss.NewStyle().Foreground(providerColor(i))
		if _, failed := errs[p.Name]; failed {
			label = fmt.Sprintf("%s unavailable", p.Name)
			style = lipgloss.NewStyle().Foreground(colErr)
		}
		chips = append(chips, style.Render(label))
	}
	row := strings.Join(chips, labelStyle.Render("  ·  "))

	extra := m.status
	if m.loading || m.probeRunning() {
		extra = m.spinner.View() + " " + m.status
	}
	if extra != "" {
		row += "   " + labelStyle.Render("|") + "   " + lipgloss.NewStyle().Foreground(colWarn).Render(extra)
	}
	// The provider row carries the most important information on screen, so it
	// keeps its styling and the status note is what gets dropped.
	return trimLine(row, m.width)
}

// statusView shows the provider row, plus any transient status or spinner.
func (m *model) statusView() string { return m.providerRow() }

// searchView renders the query line. In normal mode it still shows the active
// filter, dimmed, so the current scope is always visible.
func (m *model) searchView() string {
	prompt := searchPromptStyle.Render("/")
	field := m.search.View()

	if m.mode != modeSearch {
		prompt = labelStyle.Render("/")
		if m.search.Value() == "" {
			return prompt + labelStyle.Render("search models") + labelStyle.Render("   (press / )")
		}
		return prompt + labelStyle.Render(m.search.Value())
	}
	return prompt + field
}

// detailView shows everything known about the model under the cursor.
func (m *model) detailView() string {
	lines := make([]string, 0, detailLines)

	sel, ok := m.selected()
	if !ok {
		for i := range detailLines {
			if i == 0 {
				lines = append(lines, labelStyle.Render("no models match"))
			} else {
				lines = append(lines, "")
			}
		}
		return paneStyle.Width(m.paneWidth()).Render(strings.Join(lines, "\n"))
	}

	p, hasProvider := m.byName[sel.Provider]
	lines = append(lines,
		field("model", sel.Provider+"/"+sel.ID, m.paneWidth()),
		field("name", sel.Name, m.paneWidth()),
		field("context", contextText(sel.Context), m.paneWidth()),
		field("price", priceText(sel), m.paneWidth()),
		field("free", freeText(sel), m.paneWidth()),
		field("probe", m.probeText(sel, hasProvider, p.Completions != nil), m.paneWidth()),
	)
	return paneStyle.Width(m.paneWidth()).Render(strings.Join(lines, "\n"))
}

// probeText renders the outcome of the last probe for a model.
func (m *model) probeText(sel provider.Model, hasProvider, probeable bool) string {
	if m.probing[probeKey(sel.Provider, sel.ID)] {
		return m.spinner.View() + " probing…"
	}
	res, ok := m.probes[probeKey(sel.Provider, sel.ID)]
	if !ok {
		if !hasProvider {
			return "unknown provider"
		}
		if !probeable {
			return "no completions endpoint configured"
		}
		return labelStyle.Render("not tested — press t")
	}

	status := fmt.Sprintf("%d", res.Status)
	if res.Status == 0 {
		status = "ERR"
	}
	if !res.OK {
		detail := res.Detail
		if detail == "" {
			detail = "failed"
		}
		return lipgloss.NewStyle().Foreground(colErr).Render(status) +
			"  " + fmt.Sprintf("%.0fms", float64(res.Latency.Microseconds())/1000) +
			"  " + labelStyle.Render(detail)
	}
	mark := lipgloss.NewStyle().Foreground(colOK).Render(status)
	out := mark + "  " + fmt.Sprintf("%.0fms", float64(res.Latency.Microseconds())/1000)
	if res.Tokens > 0 {
		out += fmt.Sprintf("  %d tokens", res.Tokens)
	}
	return out
}

// paneWidth is the content width available inside the bordered detail pane.
func (m *model) paneWidth() int {
	w := m.width - 4 // border plus padding
	if w < 20 {
		return 20
	}
	return w
}

// field renders a label/value pair with the label right-aligned to a fixed
// column, so the values line up down the pane.
func field(label, value string, width int) string {
	const labelWidth = 8
	left := labelStyle.Render(fmt.Sprintf("%*s", labelWidth, label))
	return left + " " + trimLine(value, width-labelWidth-2)
}

func contextText(n int64) string {
	if n <= 0 {
		return "unknown"
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM tokens", float64(n)/1e6)
	}
	if n >= 1000 {
		return fmt.Sprintf("%dk tokens", n/1000)
	}
	return fmt.Sprintf("%d tokens", n)
}

func priceText(m provider.Model) string {
	if !m.Priced {
		return labelStyle.Render("not published")
	}
	return fmt.Sprintf("$%.2f / 1M in · $%.2f / 1M out", m.PromptPrice*1e6, m.CompletionPrice*1e6)
}

func freeText(m provider.Model) string {
	if m.Reason == "" {
		return labelStyle.Render("no")
	}
	return lipgloss.NewStyle().Foreground(colOK).Render("yes") + " " + labelStyle.Render("("+m.Reason+")")
}

func formatContext(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatPrice(m provider.Model) string {
	if !m.Priced {
		return "price n/a"
	}
	return fmt.Sprintf("$%.2f", m.PromptPrice*1e6)
}
