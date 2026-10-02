package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/provider"
)

// tableGap is the number of spaces between columns.
const tableGap = 2

// column describes one column of the model table.
//
// drop is the order in which a column is sacrificed when the terminal is too
// narrow: higher goes first. A drop of 0 means the column is never dropped,
// which is reserved for MODEL since it identifies the row.
type column struct {
	title string
	// cap limits how wide the column may grow from its own content.
	cap int
	// drop is the removal priority; 0 is never removed.
	drop  int
	value func(provider.Model) string
	align align
	// width is filled in by computeLayout.
	width int
}

// align says which side of the cell text sits on.
type align int

const (
	alignLeft align = iota
	alignRight
)

// tableLayout is the set of columns and their widths for one rendering pass.
// The delegate holds a pointer to it, so recomputing the layout after a resize
// or a filter change is enough to update every row.
type tableLayout struct {
	cols []column
}

func (l *tableLayout) columns() []column {
	return []column{
		// Drop priorities, highest goes first when space runs out. MODEL is
		// never dropped because it identifies the row. PROVIDER follows
		// closely: knowing which gateway a model sits behind matters more here
		// than its context window or which rule flagged it as free.
		{title: "PROVIDER", cap: 14, drop: 1, align: alignLeft,
			value: func(m provider.Model) string { return m.Provider }},
		{title: "MODEL", cap: 1 << 20, drop: 0, align: alignLeft,
			value: func(m provider.Model) string { return m.ID }},
		{title: "CONTEXT", cap: 8, drop: 4, align: alignRight,
			value: func(m provider.Model) string { return formatContext(m.Context) }},
		{title: "$/1M IN", cap: 8, drop: 2, align: alignRight,
			value: func(m provider.Model) string { return priceCell(m.Priced, m.PromptPrice) }},
		{title: "$/1M OUT", cap: 8, drop: 3, align: alignRight,
			value: func(m provider.Model) string { return priceCell(m.Priced, m.CompletionPrice) }},
		{title: "FREE", cap: 10, drop: 5, align: alignLeft,
			value: func(m provider.Model) string { return freeReason(m) }},
	}
}

// contentWidth is the widest cell this column needs, capped.
func (c column) contentWidth(models []provider.Model) int {
	w := lipgloss.Width(c.title)
	for _, m := range models {
		if v := lipgloss.Width(c.value(m)); v > w {
			w = v
		}
	}
	return min(w, c.cap)
}

// computeLayout sizes the columns to the content and the terminal, dropping the
// least important ones until the row fits. Whatever slack remains goes to
// MODEL, which is the column a reader scans.
func computeLayout(models []provider.Model, width int) *tableLayout {
	l := &tableLayout{}
	cols := l.columns()

	for i := range cols {
		cols[i].width = cols[i].contentWidth(models)
	}

	for l.width(cols) > width {
		worst := -1
		for i := range cols {
			if cols[i].drop == 0 {
				continue
			}
			if worst == -1 || cols[i].drop > cols[worst].drop {
				worst = i
			}
		}
		if worst == -1 {
			break // only MODEL is left and it still overflows
		}
		cols = append(cols[:worst], cols[worst+1:]...)
	}

	// MODEL alone can still be wider than the terminal, so squeeze the last
	// column until the row fits rather than overflowing the right edge.
	if over := l.width(cols) - width; over > 0 && len(cols) > 0 {
		last := len(cols) - 1
		cols[last].width = max(1, cols[last].width-over)
	}

	// Give the leftover space to MODEL rather than leaving a ragged right edge.
	if slack := width - l.width(cols); slack > 0 {
		for i := range cols {
			if cols[i].drop == 0 {
				cols[i].width += slack
				break
			}
		}
	}

	l.cols = cols
	return l
}

// width is the total rendered width of a row including gaps.
func (l *tableLayout) width(cols []column) int {
	if len(cols) == 0 {
		return 0
	}
	total := tableGap * (len(cols) - 1)
	for _, c := range cols {
		total += c.width
	}
	return total
}

// header renders the column titles aligned with the rows below them.
func (l *tableLayout) header() string {
	var b strings.Builder
	for i, c := range l.cols {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", tableGap))
		}
		text := pad(c.title, c.width, c.align)
		if i == len(l.cols)-1 {
			text = strings.TrimRight(text, " ")
		}
		b.WriteString(text)
	}
	return labelStyle.Render(b.String())
}

// row renders one model.
func (l *tableLayout) row(m provider.Model) string {
	var b strings.Builder
	for i, c := range l.cols {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", tableGap))
		}
		text := pad(c.value(m), c.width, c.align)
		if i == len(l.cols)-1 {
			text = strings.TrimRight(text, " ")
		}
		b.WriteString(text)
	}
	return b.String()
}

// pad fits text into a column, truncating with an ellipsis when it is too long.
func pad(text string, width int, a align) string {
	w := lipgloss.Width(text)
	switch {
	case w > width:
		if width <= 1 {
			return ""
		}
		r := []rune(text)
		return string(r[:width-1]) + "…"
	case a == alignRight:
		return strings.Repeat(" ", width-w) + text
	default:
		return text + strings.Repeat(" ", width-w)
	}
}

// freeReason renders why a model qualified as free, or a dash when it did not.
func freeReason(m provider.Model) string {
	if m.Reason == "" {
		return "-"
	}
	return m.Reason
}

// priceCell renders a per-token price as a per-million figure. A model whose
// provider published no pricing shows a dash, because unknown and free are not
// the same thing.
func priceCell(priced bool, v float64) string {
	if !priced {
		return "-"
	}
	return fmt.Sprintf("$%.2f", v*1e6)
}

// tableDelegate renders each item as a single table row. The list keeps doing
// the scrolling and cursor work; this only controls how a row is painted.
type tableDelegate struct {
	layout *tableLayout
	styles list.DefaultItemStyles
}

func (d *tableDelegate) Height() int  { return 1 }
func (d *tableDelegate) Spacing() int { return 0 }

func (d *tableDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d *tableDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(modelItem)
	if !ok {
		return
	}

	// The gutter is drawn here rather than styled into the cells so the header
	// and every row share exactly the same indent.
	gutter := strings.Repeat(" ", listGutter)
	style := d.styles.NormalTitle
	// A newly discovered model is tinted, but the selection wins so the cursor
	// stays readable while moving through a block of them.
	switch {
	case index == m.Index():
		gutter = lipgloss.NewStyle().Foreground(colAccent).Render("▌ ")
		style = d.styles.SelectedTitle
	case it.isNew:
		style = newStyle
		gutter = lipgloss.NewStyle().Foreground(colWarn).Render("+ ")
	}
	fmt.Fprint(w, gutter+style.Render(d.layout.row(it.m)))
}

// newTableDelegate builds the delegate and its backing layout.
func newTableDelegate() *tableDelegate {
	return &tableDelegate{
		layout: &tableLayout{cols: (&tableLayout{}).columns()},
		styles: list.NewDefaultItemStyles(),
	}
}

// applyLayout points the delegate at a freshly computed layout.
func (d *tableDelegate) applyLayout(l *tableLayout) {
	d.layout = l
}
