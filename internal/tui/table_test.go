package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/bluespada/getfm/pkg/provider"
)

func sampleModels() []provider.Model {
	return []provider.Model{
		{ID: "vendor/a-very-long-model-identifier:free", Provider: "openrouter", Priced: true, Context: 1048576, Reason: "suffix"},
		{ID: "short", Provider: "kilo", Context: 8000, Reason: "declared"},
		{ID: "mid/model", Provider: "cline", Priced: true, PromptPrice: 1.5, CompletionPrice: 7},
	}
}

func titles(l *tableLayout) []string {
	out := make([]string, 0, len(l.cols))
	for _, c := range l.cols {
		out = append(out, c.title)
	}
	return out
}

func TestWideLayoutKeepsEveryColumn(t *testing.T) {
	l := computeLayout(sampleModels(), 120)
	got := strings.Join(titles(l), ",")
	want := "PROVIDER,MODEL,CONTEXT,$/1M IN,$/1M OUT,FREE"
	if got != want {
		t.Errorf("columns = %s, want %s", got, want)
	}
}

func TestNarrowLayoutDropsLeastImportantFirst(t *testing.T) {
	models := sampleModels()
	// Step down in width and confirm the columns fall away in priority order,
	// with MODEL surviving to the end.
	var sequence []string
	for _, w := range []int{120, 90, 80, 70, 60, 50, 40, 30, 20} {
		sequence = append(sequence, strings.Join(titles(computeLayout(models, w)), ","))
	}

	if !contains(sequence, "PROVIDER,MODEL,CONTEXT,$/1M IN,$/1M OUT,FREE") {
		t.Errorf("widest layout should keep everything: %v", sequence)
	}
	last := sequence[len(sequence)-1]
	if !strings.Contains(last, "MODEL") {
		t.Errorf("MODEL must survive to the narrowest layout, got %q", last)
	}
	if !contains(sequence, "PROVIDER,MODEL,CONTEXT,$/1M IN,$/1M OUT") {
		t.Errorf("expected an intermediate layout that keeps the provider: %v", sequence)
	}
	if indexOf(sequence, "PROVIDER,MODEL,CONTEXT,$/1M IN,$/1M OUT,FREE") >
		indexOf(sequence, "PROVIDER,MODEL,CONTEXT,$/1M IN,$/1M OUT") {
		t.Errorf("FREE must be dropped before PROVIDER: %v", sequence)
	}
}

func TestLayoutAlwaysFitsWidth(t *testing.T) {
	models := sampleModels()
	for w := 10; w <= 200; w++ {
		l := computeLayout(models, w)
		if got := l.width(l.cols); got > w {
			t.Errorf("width %d: layout is %d wide", w, got)
		}
	}
}

func TestHeaderLinesUpWithRows(t *testing.T) {
	l := computeLayout(sampleModels(), 120)
	header := plain(l.header())
	row := plain(l.row(sampleModels()[0]))

	// Each header title must start at the same column as its column's values.
	if lipgloss.Width(header) == 0 {
		t.Fatal("header is empty")
	}
	for _, c := range l.cols {
		if !strings.Contains(header, c.title) {
			t.Errorf("header is missing %q", c.title)
		}
	}
	// The model id must begin where the MODEL header begins.
	hi := strings.Index(header, "MODEL")
	ri := strings.Index(row, "vendor/a-very-long")
	if hi != ri {
		t.Errorf("MODEL header at %d but values at %d; they must align", hi, ri)
	}
}

func TestPriceCellDistinguishesUnknownFromFree(t *testing.T) {
	if got := priceCell(false, 0); got != "-" {
		t.Errorf("unpriced model = %q, want a dash", got)
	}
	if got := priceCell(true, 0); got != "$0.00" {
		t.Errorf("free model = %q, want $0.00", got)
	}
}

func TestPadTruncatesAndAligns(t *testing.T) {
	if got := pad("hello", 10, alignLeft); got != "hello     " {
		t.Errorf("left pad = %q", got)
	}
	if got := pad("hello", 10, alignRight); got != "     hello" {
		t.Errorf("right pad = %q", got)
	}
	got := pad("a-very-long-value", 8, alignLeft)
	if lipgloss.Width(got) != 8 {
		t.Errorf("truncated width = %d, want 8 (%q)", lipgloss.Width(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation should mark with an ellipsis, got %q", got)
	}
}

func TestFreeReasonRendersDashWhenNotFree(t *testing.T) {
	if got := freeReason(provider.Model{}); got != "-" {
		t.Errorf("no reason = %q, want a dash", got)
	}
	if got := freeReason(provider.Model{Reason: "suffix"}); got != "suffix" {
		t.Errorf("reason = %q", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}
