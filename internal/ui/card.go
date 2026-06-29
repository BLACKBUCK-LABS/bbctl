package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Field is one key/value row in a Card.
type Field struct{ Key, Value string }

// Card renders a titled key/value block. Bordered on a color TTY,
// plain text otherwise.
func Card(title string, fields []Field) string {
	keyW := 0
	for _, f := range fields {
		if len(f.Key) > keyW {
			keyW = len(f.Key)
		}
	}
	var b strings.Builder
	if !Std.Color {
		fmt.Fprintf(&b, "── %s ──\n", title)
		for _, f := range fields {
			fmt.Fprintf(&b, "  %-*s  %s\n", keyW, f.Key, f.Value)
		}
		return b.String()
	}
	for _, f := range fields {
		fmt.Fprintf(&b, "%s  %s\n",
			Render(Muted, fmt.Sprintf("%-*s", keyW, f.Key)),
			Render(Text, f.Value))
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1)
	titled := Render(Brand.Bold(true), title) + "\n" +
		strings.TrimRight(b.String(), "\n")
	return box.Render(titled)
}
