package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/config"
	"github.com/bluespada/getfm/pkg/probe"
	"github.com/bluespada/getfm/pkg/provider"
)

// Modal pages. Enter opens the modal on the card; Tab steps through the rest.
const (
	pageCard = iota
	pageRequest
	pageCount
)

var pageTitles = map[int]string{
	pageCard:    "model card",
	pageRequest: "request / response",
}

// modal is the overlay shown over the browser.
type modal struct {
	open  bool
	page  int
	model provider.Model
	// provider is the catalog entry for the model, used for endpoints and keys.
	provider config.Provider
	// result is the last probe for this model, if any.
	result *probeKeyed
	view   viewport.Model
	// width and height are the overlay's outer dimensions.
	width, height int
	// prepared is what getfm would send for this model, computed on open so the
	// request view is useful even for a model that has never been probed.
	prepared *probe.Prepared
}

// maxModalWidth keeps the card readable on wide terminals.
const maxModalWidth = 96

// openModal shows the overlay for a model. A zero or negative width leaves the
// overlay closed, which is the right behaviour before the first resize.
func (m *model) openModal(sel provider.Model, hasProvider bool) tea.Cmd {
	if m.width <= 0 || m.height <= 0 {
		return nil
	}
	if !hasProvider {
		return nil
	}

	d := &modal{
		open:     true,
		model:    sel,
		provider: m.byName[sel.Provider],
		width:    min(m.modalWidth(), maxModalWidth),
		height:   min(m.height-4, 30),
	}
	if r, ok := m.probes[probeKey(sel.Provider, sel.ID)]; ok {
		res := r
		d.result = &res
	}
	// request only reports ok for a provider that declares a completions
	// endpoint, so the credential slot can be described without checking again.
	if req, ok := m.request(sel); ok {
		if p, err := probe.Prepare(req); err == nil {
			safe := p.Safe(d.provider.Completions.AuthHeader, d.provider.Completions.AuthPrefix)
			d.prepared = &safe
		}
	}
	d.height = d.clampHeight()

	m.modal = *d
	m.mode = modeModal
	m.modal.syncContent()
	return nil
}

// modalWidth is the width available for the overlay.
func (m *model) modalWidth() int {
	w := m.width - 6
	if w < 32 {
		w = 32
	}
	return w
}

// clampHeight keeps the overlay inside the terminal with a line to spare.
func (d *modal) clampHeight() int {
	h := d.height
	if h < 8 {
		h = 8
	}
	return h
}

// contentWidth is the usable text width inside the overlay border.
func (d *modal) contentWidth() int {
	w := d.width - 4 // border and padding
	if w < 16 {
		return 16
	}
	return w
}

// innerHeight is how many text lines fit inside the overlay.
func (d *modal) innerHeight() int {
	h := d.height - 4 // border, padding, title row, hint row
	if h < 3 {
		return 3
	}
	return h
}

// closeModal dismisses the overlay and returns to normal mode.
func (m *model) closeModal() {
	m.modal = modal{}
	m.mode = modeNormal
}

// nextPage steps forward through the pages, wrapping.
func (m *model) nextPage() tea.Cmd {
	m.modal.page = (m.modal.page + 1) % pageCount
	m.modal.syncContent()
	m.modal.view.GotoTop()
	return nil
}

// syncContent rebuilds the viewport for the current page and resizes it.
func (d *modal) syncContent() {
	d.view = viewport.New(d.contentWidth(), d.innerHeight())
	d.view.YPosition = 0
	d.view.SetContent(d.render())
}

// render produces the text for the current page.
func (d *modal) render() string {
	switch d.page {
	case pageRequest:
		return d.renderRequest()
	default:
		return d.renderCard()
	}
}

// renderCard shows the curated summary followed by the provider's own record.
func (d *modal) renderCard() string {
	var b strings.Builder
	b.WriteString(trimLine(hiStyle.Render(d.provider.Label+" · "+d.provider.Name), d.contentWidth()) + "\n")
	b.WriteString(trimLine(hiStyle.Render(d.model.Provider+"/"+d.model.ID), d.contentWidth()) + "\n\n")

	b.WriteString(d.cardField("name", d.model.Name))
	b.WriteString(d.cardField("context", contextText(d.model.Context)))
	b.WriteString(d.cardField("price", plain(priceText(d.model))))
	b.WriteString(d.cardField("free", plain(freeText(d.model))))
	if modality := modalityText(d.model); modality != "" {
		b.WriteString(d.cardField("modality", modality))
	}
	if desc := descriptionText(d.model); desc != "" {
		b.WriteString("\n  " + labelStyle.Render("description") + "\n")
		for _, line := range wrap(desc, d.contentWidth()-4) {
			b.WriteString("  " + valueStyle.Render(line) + "\n")
		}
	}
	if params := paramsText(d.model); params != "" {
		b.WriteString("\n")
		b.WriteString(d.cardField("parameters", params))
	}

	if len(d.model.Raw) > 0 {
		b.WriteString("\n\n" + labelStyle.Render("provider record") + "\n")
		b.WriteString(prettyJSON(d.model.Raw, d.contentWidth()))
	}
	return b.String()
}

// renderRequest shows what getfm sent and what came back.
func (d *modal) renderRequest() string {
	var b strings.Builder

	if d.result != nil {
		b.WriteString(hiStyle.Render("last probe") + "\n")
		state := labelStyle.Render("no response captured")
		if d.result.Status > 0 {
			state = lipgloss.NewStyle().Foreground(colOK).Render(fmt.Sprintf("%d", d.result.Status))
			if !d.result.OK {
				state = lipgloss.NewStyle().Foreground(colErr).Render(fmt.Sprintf("%d", d.result.Status))
			}
		}
		b.WriteString(d.cardField("status", state+"  "+fmt.Sprintf("%.0fms", float64(d.result.Latency.Microseconds())/1000)))
		if !d.result.OK {
			b.WriteString(d.cardField("reason", classLabel(d.result.Reason)))
		}
		if d.result.Tokens > 0 {
			b.WriteString(d.cardField("tokens", fmt.Sprintf("%d", d.result.Tokens)))
		}
		if d.result.Detail != "" {
			b.WriteString(d.cardField("detail", d.result.Detail))
		}
		b.WriteString("\n")
	}

	prep := d.prepared
	if prep == nil && d.result != nil {
		prep = &d.result.Request
	}
	if prep != nil {
		b.WriteString(hiStyle.Render("request") + "\n")
		b.WriteString(d.cardField("method", prep.Method))
		b.WriteString(d.cardField("url", prep.URL))
		for _, k := range sortedKeys(prep.Header) {
			b.WriteString(d.cardField(strings.ToLower(k), strings.Join(prep.Header.Values(k), ", ")))
		}
		if prep.Body != "" {
			b.WriteString("\n" + labelStyle.Render("body") + "\n")
			b.WriteString(indentBlock(prettyBody(prep.Body, d.contentWidth()-2), "  "))
		}
	} else {
		b.WriteString(labelStyle.Render("no request could be prepared for this model") + "\n")
	}

	if d.result != nil && d.result.Response != nil {
		b.WriteString("\n\n" + hiStyle.Render("response") + "\n")
		if d.result.Response.Truncated {
			b.WriteString(labelStyle.Render("(body truncated)") + "\n")
		}
		for _, k := range sortedKeys(d.result.Response.Header) {
			b.WriteString(d.cardField(strings.ToLower(k), strings.Join(d.result.Response.Header.Values(k), ", ")))
		}
		body := d.result.Response.Body
		if body == "" {
			b.WriteString("\n" + labelStyle.Render("(empty body)") + "\n")
		} else {
			b.WriteString("\n" + labelStyle.Render("body") + "\n")
			b.WriteString(indentBlock(prettyBody(body, d.contentWidth()-2), "  "))
		}
	}
	return b.String()
}

// cardField renders an aligned label and value, wrapping long values to the
// overlay's content width. Wrapping here rather than leaving it to the
// viewport matters because the viewport cuts overflow off with no mark that
// anything was hidden.
func (d *modal) cardField(label, value string) string {
	const labelWidth = 12
	indent := strings.Repeat(" ", labelWidth)
	avail := d.contentWidth() - labelWidth - 1
	if avail < 8 {
		avail = 8
	}
	lines := wrap(value, avail)
	if len(lines) == 0 {
		return labelStyle.Render(fmt.Sprintf("%*s", labelWidth, label)) + "\n"
	}
	var b strings.Builder
	b.WriteString(labelStyle.Render(fmt.Sprintf("%*s", labelWidth, label)) + " " + valueStyle.Render(lines[0]) + "\n")
	for _, line := range lines[1:] {
		b.WriteString(indent + valueStyle.Render(line) + "\n")
	}
	return b.String()
}

// classInk picks the colour a failure class is drawn in, by how much the reader
// has to do about it. A rate limit, a timeout, a network blip and a
// provider-side 5xx are worth retrying later rather than fixing: none of
// them are the reader's fault. A rejected credential, an exhausted
// allowance, a malformed request and a bad catalog entry are things to go
// and fix. A withdrawn model is history.
func classInk(c probe.Class) lipgloss.Color {
	switch c {
	case probe.ClassRate, probe.ClassTimeout, probe.ClassNetwork, probe.ClassServer:
		return colWarn
	case probe.ClassGone, probe.ClassUnknown:
		return colMuted
	default:
		return colErr
	}
}

// classLabel renders a failure class in that colour. An unset class still reads
// as "unknown" rather than as an empty field.
func classLabel(c probe.Class) string {
	if c == "" {
		c = probe.ClassUnknown
	}
	return lipgloss.NewStyle().Foreground(classInk(c)).Render(string(c))
}

// modalityText reports input/output modalities when the provider publishes them.
func modalityText(m provider.Model) string {
	arch, ok := m.Raw["architecture"].(map[string]any)
	if !ok {
		return ""
	}
	in := joinAny(arch["input_modalities"])
	out := joinAny(arch["output_modalities"])
	if in == "" && out == "" {
		return ""
	}
	return in + " → " + out
}

// descriptionText returns the provider's own one-line description.
func descriptionText(m provider.Model) string {
	s, _ := m.Raw["description"].(string)
	return strings.TrimSpace(s)
}

// paramsText summarises supported parameters without dumping the whole list.
func paramsText(m provider.Model) string {
	list := toStrings(m.Raw["supported_parameters"])
	if len(list) == 0 {
		return ""
	}
	if len(list) <= 8 {
		return strings.Join(list, ", ")
	}
	return fmt.Sprintf("%d supported: %s, …", len(list), strings.Join(list[:8], ", "))
}

func joinAny(v any) string {
	return strings.Join(toStrings(v), ", ")
}

func toStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys(h map[string][]string) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// prettyJSON re-indents a decoded value, falling back to its Go form when the
// value cannot be re-encoded.
func prettyJSON(v any, width int) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return valueStyle.Render(fmt.Sprintf("%v", v))
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		lines[i] = valueStyle.Render(trimLine(line, width))
	}
	return strings.Join(lines, "\n")
}

// prettyBody indents a raw body when it is JSON, and shows it verbatim when it
// is not.
func prettyBody(body string, width int) string {
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err == nil {
		return prettyJSON(decoded, width)
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = valueStyle.Render(trimLine(line, width))
	}
	return strings.Join(lines, "\n")
}

func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

// wrap breaks text into lines no wider than width, on word boundaries.
//
// A value that already fits is returned untouched, which matters for styled
// values: measuring them by rune count would count escape sequences and wrap
// far too early. A value that genuinely needs wrapping is wrapped as plain
// text, so an escape sequence is never split in half.
func wrap(s string, width int) []string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return nil
	}
	if width < 8 {
		width = 8
	}
	if lipgloss.Width(s) <= width {
		return []string{s}
	}
	s = plain(s)
	var out []string
	for len(s) > width {
		cut := strings.LastIndex(s[:width+1], " ")
		if cut <= 0 {
			cut = safeCut(s, width)
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	return append(out, s)
}

// safeCut returns the largest index at or below limit that does not split a
// multi-byte character.
func safeCut(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return limit
}
