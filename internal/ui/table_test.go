package ui

import (
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestTable_ShapeAndNull(t *testing.T) {
	Std = Caps{Color: false, Unicode: false, TTY: false}
	out := Table([]string{"id", "name"}, [][]*string{
		{ptr("1"), ptr("alice")},
		{ptr("2"), nil},
	})
	if !strings.Contains(out, "| id") || !strings.Contains(out, "name") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "NULL") {
		t.Errorf("nil cell should render NULL: %q", out)
	}
	if strings.Count(out, "+----") < 1 {
		t.Errorf("expected separator rows: %q", out)
	}
}
