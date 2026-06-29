package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// Brand palette — 256-color codes mirror the originals in welcome.go.
var (
	Brand    = lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // soft red
	Accent   = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))  // cyan
	Success_ = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
	Warning  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber
	Danger   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // red (brand)
	Muted    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // gray
	Dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // dimmer gray
	Text     = lipgloss.NewStyle().Foreground(lipgloss.Color("231")) // near-white
)

// Render applies a style, but only when the terminal supports color.
// Returns the raw string otherwise so piped output stays clean.
func Render(style lipgloss.Style, s string) string {
	if !Std.Color {
		return s
	}
	return style.Render(s)
}
