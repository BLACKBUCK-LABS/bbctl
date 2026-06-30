package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

func TestRender_ColorDisabled(t *testing.T) {
	Std = Caps{Color: false}
	got := Render(Brand, "hello")
	if got != "hello" {
		t.Errorf("Render with color off = %q, want plain %q", got, "hello")
	}
	if strings.Contains(got, "\033") {
		t.Errorf("Render leaked ANSI escapes with color off: %q", got)
	}
}

func TestRender_ColorEnabled(t *testing.T) {
	Std = Caps{Color: true}
	got := Render(Brand, "hello")
	if !strings.Contains(got, "hello") {
		t.Errorf("Render dropped content: %q", got)
	}
	if !strings.Contains(got, "\033") {
		t.Errorf("Render produced no ANSI with color on: %q", got)
	}
}
