package ui

import (
	"strings"
	"testing"
)

func TestCard_PlainNonTTY(t *testing.T) {
	Std = Caps{Color: false, Unicode: false, TTY: false}
	got := Card("Approval required", []Field{
		{"Ticket", "REQ-4821"},
		{"Status", "awaiting approval"},
	})
	if !strings.Contains(got, "Approval required") {
		t.Errorf("missing title: %q", got)
	}
	if !strings.Contains(got, "Ticket") || !strings.Contains(got, "REQ-4821") {
		t.Errorf("missing field: %q", got)
	}
	if strings.Contains(got, "\033") {
		t.Errorf("leaked ANSI on non-TTY: %q", got)
	}
}
