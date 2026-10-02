// Package tui provides getfm's interactive model browser.
//
// Navigation follows vim conventions: j and k move, / searches, gg and G jump,
// Ctrl-D and Ctrl-U page. Typing must not collide with movement, so the browser
// has two modes. Normal mode handles movement and commands; search mode passes
// every key to the query line, exactly like vim.
package tui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/free"
	"github.com/bluespada/getfm/pkg/probe"
	"github.com/bluespada/getfm/pkg/provider"
	"github.com/bluespada/getfm/pkg/store"
)

var errNoProviders = errors.New("no providers configured")

// Options is everything the browser needs. The command line resolves flags and
// credentials, then hands over plain data.
type Options struct {
	Catalog *config.File
	Rules   free.Rules
	// Key returns the API key for a provider, or "" when it needs none.
	Key     func(config.Provider) string
	Timeout time.Duration
	// Prompt is the text a probe sends.
	Prompt string
	// Store is the record of models seen in previous runs, and StorePath is
	// where an updated record is written. Both are optional: without a store
	// nothing is persisted and no model is ever flagged as new.
	Store     *store.Store
	StorePath string
	// Copy writes text to the clipboard, as the c key does. Optional; without
	// it the system clipboard is used.
	Copy func(string) error
}

type mode int

const (
	modeNormal mode = iota
	modeSearch
	// modeModal swallows keys for the overlay, so q closes the card instead of
	// quitting and j scrolls the card instead of moving the list.
	modeModal
)

// modelItem adapts a model to the list's item interface.
type modelItem struct {
	m provider.Model
	// isNew marks a model that was absent from the store at startup.
	isNew bool
}

func (i modelItem) Title() string       { return i.m.ID }
func (i modelItem) FilterValue() string { return i.m.ID }
func (i modelItem) Description() string {
	return strings.Join([]string{
		i.m.Provider,
		formatContext(i.m.Context),
		formatPrice(i.m),
	}, " · ")
}

// model holds all browser state.
type model struct {
	opts Options
	mode mode

	// ctx is cancelled when the program exits, which cancels in-flight requests.
	ctx    context.Context
	cancel context.CancelFunc

	width  int
	height int

	// pendingG tracks the first "g" of a "gg" sequence.
	pendingG bool
	// freeOnly hides everything that is not free.
	freeOnly bool
	// loading is true while a catalog fetch is in flight.
	loading bool
	// status is a short transient note shown under the header.
	status string

	// byName indexes the catalog for fast provider lookup.
	byName map[string]config.Provider
	// allModels and freeModels hold every model and the free subset per provider.
	allModels  map[string][]provider.Model
	freeModels map[string][]provider.Model
	// results is the outcome of the most recent fetch, shown in the header.
	results []provider.FetchResult

	// prior is an immutable snapshot of the store taken at startup. Newness is
	// judged against this rather than against the live store, so refreshing
	// mid-session does not erase the very models the refresh was meant to find.
	prior map[string]map[string]bool
	// newIDs accumulates, per provider, the ids absent from prior.
	newIDs map[string]map[string]bool
	// newCount is how many models have been flagged this session.
	newCount int
	// store accumulates what to persist; storePath is where it is written.
	store     *store.Store
	storePath string
	// hasStore distinguishes "a store that knows nothing yet", which is a first
	// run and makes every model new, from "no store at all", which leaves the
	// tool with nothing to compare against.
	hasStore bool
	// persistAfterFetch defers the write until the in-flight fetch lands, so
	// the store records what was actually seen rather than what was requested.
	persistAfterFetch bool

	// probes maps "provider/model" to the last probe result.
	probes  map[string]probeKeyed
	probing map[string]bool

	spinner spinner.Model
	list    list.Model
	search  textinput.Model

	// delegate paints each item as a table row; layout holds the column widths
	// it renders against, recomputed whenever the rows or the width change.
	delegate *tableDelegate
	layout   *tableLayout

	// modal is the overlay currently shown, if any.
	modal modal

	// visible mirrors the items currently in the list, so selection maps back
	// to the model without re-deriving it.
	visible []provider.Model
}

// probeKeyed is a probe result plus the model it belongs to.
type probeKeyed struct {
	Model   provider.Model
	OK      bool
	Status  int
	Latency time.Duration
	Tokens  int64
	Detail  string
	// Request and Response are what crossed the wire, already redacted.
	Request  probe.Prepared
	Response *probe.Exchange
}

// probeKey is the map key for a probe result.
func probeKey(providerName, modelID string) string { return providerName + "/" + modelID }

// newModel builds the initial state. Fetching starts in Init.
func newModel(opts Options) (*model, error) {
	if opts.Catalog == nil || len(opts.Catalog.Providers) == 0 {
		return nil, errNoProviders
	}
	if opts.Prompt == "" {
		opts.Prompt = "hi"
	}
	if opts.Key == nil {
		opts.Key = func(config.Provider) string { return "" }
	}
	if opts.Copy == nil {
		opts.Copy = newClipboard()
	}

	m := &model{
		opts:       opts,
		byName:     make(map[string]config.Provider, len(opts.Catalog.Providers)),
		allModels:  make(map[string][]provider.Model, len(opts.Catalog.Providers)),
		freeModels: make(map[string][]provider.Model, len(opts.Catalog.Providers)),
		probes:     map[string]probeKeyed{},
		probing:    map[string]bool{},
		freeOnly:   true,
		loading:    true,
		status:     "loading catalogs",
		width:      80,
		height:     24,
		spinner:    spinner.New(),
	}
	for _, p := range opts.Catalog.Providers {
		m.byName[p.Name] = p
	}
	// Snapshot what the store already knows, so a model can be called new
	// relative to the start of this run rather than relative to the last fetch.
	m.store = opts.Store
	m.storePath = opts.StorePath
	m.hasStore = opts.Store != nil
	m.prior = make(map[string]map[string]bool, len(opts.Catalog.Providers))
	m.newIDs = make(map[string]map[string]bool, len(opts.Catalog.Providers))
	for _, p := range opts.Catalog.Providers {
		known := map[string]bool{}
		for id := range opts.Store.Known(p.Name) {
			known[id] = true
		}
		m.prior[p.Name] = known
		m.newIDs[p.Name] = map[string]bool{}
	}
	m.initComponents()
	return m, nil
}

// initComponents builds the widgets and gives the list room to render.
func (m *model) initComponents() {
	m.spinner = spinner.New()
	m.spinner.Spinner = spinner.Dot
	m.spinner.Style = lipgloss.NewStyle().Foreground(colAccent)

	ti := textinput.New()
	ti.Placeholder = "filter"
	ti.Prompt = "/"
	ti.PromptStyle = searchPromptStyle
	ti.CharLimit = 120
	ti.Width = 40
	ti.Focus()
	m.search = ti

	m.delegate = newTableDelegate()
	// The delegate and the title are styled without padding or borders so the
	// table owns its own gutter; otherwise the list's chrome shifts the columns
	// and the header stops lining up with the rows.
	m.delegate.styles.NormalTitle = lipgloss.NewStyle().Foreground(colText)
	m.delegate.styles.SelectedTitle = lipgloss.NewStyle().Foreground(colHi).Bold(true)
	m.delegate.styles.DimmedTitle = lipgloss.NewStyle().Foreground(colMuted)
	m.delegate.styles.NormalDesc = lipgloss.NewStyle().Foreground(colMuted)

	m.layout = &tableLayout{cols: m.layout.columns()}

	m.list = list.New(nil, m.delegate, 80, 10)
	m.list.SetShowTitle(true)
	m.list.SetShowStatusBar(false)
	m.list.SetShowHelp(false)
	m.list.SetFilteringEnabled(false)
	m.list.DisableQuitKeybindings()
	m.list.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
	m.list.Styles.Title = labelStyle
}

// listGutter is the indent the table draws itself, leaving the selected row a
// cursor mark in the same two columns.
const listGutter = 2

// relayout recomputes column widths for the current rows and terminal size.
func (m *model) relayout() {
	width := m.width - listGutter
	if width <= 20 {
		width = 20
	}
	m.layout = computeLayout(m.visible, width)
	m.delegate.applyLayout(m.layout)
	m.list.Title = strings.Repeat(" ", listGutter) + m.layout.header()
}

// newClient builds an HTTP client with a capped redirect chain.
func newClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// selected returns the model under the cursor, if any.
func (m *model) selected() (provider.Model, bool) {
	i := m.list.Index()
	if i < 0 || i >= len(m.visible) {
		return provider.Model{}, false
	}
	return m.visible[i], true
}

// applyFree decides which models are free, reusing the same rules the command
// line uses so the browser and any scripts never disagree.
func (m *model) applyFree(res provider.FetchResult) {
	p, ok := m.byName[res.Provider]
	if !ok {
		return
	}
	facts := free.Facts{FreeAlways: p.FreeAlways, FreeIDs: p.DeclaredFree()}
	m.allModels[res.Provider] = res.Models
	m.freeModels[res.Provider] = m.opts.Rules.Filter(res.Models, facts)
}

// query is the active search text, lowercased for matching.
func (m *model) query() string {
	return strings.ToLower(strings.TrimSpace(m.search.Value()))
}

// matches reports whether a model survives the active search.
func (m *model) matches(model provider.Model) bool {
	q := m.query()
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(model.ID), q) ||
		strings.Contains(strings.ToLower(model.Provider), q) ||
		strings.Contains(strings.ToLower(model.Name), q)
}

// Run starts the browser and blocks until the user quits.
func Run(ctx context.Context, opts Options) error {
	m, err := newModel(opts)
	if err != nil {
		return err
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	defer m.cancel()

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
