package ui

import (
	"os"
	"runtime"

	"golang.org/x/term"
)

// Caps describes what the target terminal can render.
type Caps struct {
	Color   bool
	Unicode bool
	TTY     bool
}

// Std holds the detected capabilities for stdout. Set via Init().
var Std Caps

// Init detects stdout capabilities once. Safe to call multiple times.
func Init() { Std = DetectCaps(os.Stdout) }

// DetectCaps inspects a file handle and the environment.
func DetectCaps(w *os.File) Caps {
	tty := term.IsTerminal(int(w.Fd()))
	color := tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	// Heuristic: assume UTF-8 on non-Windows TTYs and when locale looks UTF-8.
	unicode := tty && (runtime.GOOS != "windows" ||
		os.Getenv("WT_SESSION") != "")
	return Caps{Color: color, Unicode: unicode, TTY: tty}
}

// Glyph returns the unicode form when the terminal supports it, else ascii.
func Glyph(unicode, ascii string) string {
	if Std.Unicode {
		return unicode
	}
	return ascii
}
