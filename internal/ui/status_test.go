package ui

import (
	"strings"
	"testing"
)

func TestStatus_PlainWhenNoColorNoUnicode(t *testing.T) {
	Std = Caps{Color: false, Unicode: false}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"success", Success("done"), "[OK] done"},
		{"warn", Warn("careful"), "[!] careful"},
		{"err", Err("broke"), "[x] broke"},
		{"info", Info("note"), "[i] note"},
		{"arrow", Arrow("go"), "-> go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
			}
			if strings.Contains(c.got, "\033") {
				t.Errorf("%s leaked ANSI: %q", c.name, c.got)
			}
		})
	}
}

func TestStatus_UnicodeGlyphs(t *testing.T) {
	Std = Caps{Color: false, Unicode: true}
	if got := Success("done"); got != "✔ done" {
		t.Errorf("Success = %q, want %q", got, "✔ done")
	}
}
