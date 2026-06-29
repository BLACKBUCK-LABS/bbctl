package ui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

func line(style lipgloss.Style, uni, ascii, msg string) string {
	glyph := Glyph(uni, ascii)
	return fmt.Sprintf("%s %s", Render(style, glyph), msg)
}

// Success renders an OK status line.
func Success(msg string) string { return line(Success_, "✔", "[OK]", msg) }

// Warn renders a warning status line.
func Warn(msg string) string { return line(Warning, "⚠", "[!]", msg) }

// Err renders an error status line.
func Err(msg string) string { return line(Danger, "✘", "[x]", msg) }

// Info renders an informational status line.
func Info(msg string) string { return line(Accent, "ℹ", "[i]", msg) }

// Arrow renders a "selected / proceeding" line.
func Arrow(msg string) string { return line(Brand, "→", "->", msg) }
