package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// A small fixed palette keeps provider colours stable between runs, so the same
// provider is always the same colour and the eye can learn the layout.
var (
	colAccent   = lipgloss.Color("#7dcfff")
	colOK       = lipgloss.Color("#7ee787")
	colWarn     = lipgloss.Color("#ffd479")
	colErr      = lipgloss.Color("#ff7b72")
	colMuted    = lipgloss.Color("#6e7681")
	colText     = lipgloss.Color("#c9d1d9")
	colHi       = lipgloss.Color("#f0f6fc")
	colBorder   = lipgloss.Color("#30363d")
	providerInk = []lipgloss.Color{
		lipgloss.Color("#7dcfff"),
		lipgloss.Color("#d2a8ff"),
		lipgloss.Color("#7ee787"),
		lipgloss.Color("#ffa657"),
		lipgloss.Color("#ff7b9c"),
		lipgloss.Color("#79c0ff"),
	}
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	labelStyle = lipgloss.NewStyle().Foreground(colMuted)
	valueStyle = lipgloss.NewStyle().Foreground(colText)
	hiStyle    = lipgloss.NewStyle().Foreground(colHi).Bold(true)
	// newStyle tints a model the store had not seen when the run started.
	newStyle = lipgloss.NewStyle().Foreground(colWarn)

	helpStyle = lipgloss.NewStyle().Foreground(colMuted)

	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBorder).
			Padding(0, 1)

	searchPromptStyle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
)

// providerColor assigns a stable colour by index so a provider keeps its colour
// regardless of catalog order or how many providers are configured.
func providerColor(index int) lipgloss.Color {
	if len(providerInk) == 0 {
		return colText
	}
	return providerInk[index%len(providerInk)]
}
