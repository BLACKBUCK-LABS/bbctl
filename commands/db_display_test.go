package commands

import (
	"strings"
	"testing"

	"github.com/blackbuck/bbctl/internal/ui"
)

func TestRenderTable_FooterAndPlain(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	s := "x"
	out := renderTable([]string{"a"}, [][]*string{{&s}}, 12)
	if !strings.Contains(out, "1 row in set") {
		t.Errorf("missing footer: %q", out)
	}
	if strings.Contains(out, "\033") {
		t.Errorf("leaked ANSI: %q", out)
	}
}

func TestRenderTable_EmptySet(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderTable([]string{}, [][]*string{}, 50)
	if !strings.Contains(out, "Empty set") {
		t.Errorf("expected 'Empty set': %q", out)
	}
	if !strings.Contains(out, "0.050 sec") {
		t.Errorf("expected duration: %q", out)
	}
}

func TestRenderTable_PluralRows(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	a, b := "foo", "bar"
	out := renderTable([]string{"col"}, [][]*string{{&a}, {&b}}, 100)
	if !strings.Contains(out, "2 rows in set") {
		t.Errorf("expected plural rows footer: %q", out)
	}
}

func TestRenderOK_NoInsertID(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderOK(3, 0, 42)
	if !strings.Contains(out, "3 row(s) affected") {
		t.Errorf("missing rows affected: %q", out)
	}
	if !strings.Contains(out, "0.042 sec") {
		t.Errorf("missing duration: %q", out)
	}
	if strings.Contains(out, "\033") {
		t.Errorf("leaked ANSI: %q", out)
	}
}

func TestRenderOK_WithInsertID(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderOK(1, 99, 10)
	if !strings.Contains(out, "last insert id: 99") {
		t.Errorf("missing last insert id: %q", out)
	}
	if !strings.Contains(out, "1 row(s) affected") {
		t.Errorf("missing rows affected: %q", out)
	}
	if !strings.Contains(out, "0.010 sec") {
		t.Errorf("missing duration: %q", out)
	}
}

func TestRenderError_WithCode(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderError(1064, "syntax error")
	if !strings.Contains(out, "1064") {
		t.Errorf("missing error code: %q", out)
	}
	if !strings.Contains(out, "syntax error") {
		t.Errorf("missing message: %q", out)
	}
	if strings.Contains(out, "\033") {
		t.Errorf("leaked ANSI: %q", out)
	}
}

func TestRenderError_NoCode(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderError(0, "connection failed")
	if !strings.Contains(out, "connection failed") {
		t.Errorf("missing message: %q", out)
	}
}
