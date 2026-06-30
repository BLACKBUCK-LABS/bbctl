package ui

import (
	"os"
	"testing"
)

func TestDetectCaps_NoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// A pipe is not a TTY.
	r, _, _ := os.Pipe()
	defer r.Close()
	c := DetectCaps(r)
	if c.Color {
		t.Errorf("Color = true, want false when NO_COLOR set")
	}
}

func TestDetectCaps_NonTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	r, _, _ := os.Pipe()
	defer r.Close()
	c := DetectCaps(r)
	if c.TTY {
		t.Errorf("TTY = true, want false for a pipe")
	}
	if c.Color {
		t.Errorf("Color = true, want false for a non-TTY")
	}
}

func TestGlyph_UnicodeFallback(t *testing.T) {
	Std = Caps{Unicode: false}
	if got := Glyph("✔", "[OK]"); got != "[OK]" {
		t.Errorf("Glyph = %q, want [OK]", got)
	}
	Std = Caps{Unicode: true}
	if got := Glyph("✔", "[OK]"); got != "✔" {
		t.Errorf("Glyph = %q, want ✔", got)
	}
}
