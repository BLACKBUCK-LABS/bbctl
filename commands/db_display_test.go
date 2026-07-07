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

func TestRenderVertical_SingleRow(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	id, name := "1", "admin"
	out := renderVertical([]string{"id", "created_by"}, [][]*string{{&id, &name}}, 10)

	wantHeader := "*************************** 1. row ***************************\n"
	if !strings.Contains(out, wantHeader) {
		t.Errorf("missing row header: %q", out)
	}
	if !strings.Contains(out, "        id: 1\n") {
		t.Errorf("expected right-aligned id field: %q", out)
	}
	if !strings.Contains(out, "created_by: admin\n") {
		t.Errorf("expected created_by field: %q", out)
	}
	if !strings.Contains(out, "1 row in set") {
		t.Errorf("missing footer: %q", out)
	}
}

func TestRenderVertical_MultipleRowsAndNull(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	a := "1"
	out := renderVertical([]string{"id", "name"}, [][]*string{{&a, nil}, {&a, nil}}, 5)

	if !strings.Contains(out, "*************************** 1. row ***************************\n") {
		t.Errorf("missing row 1 header: %q", out)
	}
	if !strings.Contains(out, "*************************** 2. row ***************************\n") {
		t.Errorf("missing row 2 header: %q", out)
	}
	if !strings.Contains(out, "name: NULL\n") {
		t.Errorf("expected NULL rendering: %q", out)
	}
	if !strings.Contains(out, "2 rows in set") {
		t.Errorf("missing plural footer: %q", out)
	}
}

func TestRenderVertical_EmptySet(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderVertical([]string{}, [][]*string{}, 50)
	if !strings.Contains(out, "Empty set") {
		t.Errorf("expected 'Empty set': %q", out)
	}
}

// TestRenderVertical_ZeroRowsWithColumns locks in parity with renderTable:
// when a SELECT matches no rows but still has columns (e.g. WHERE 1=0), both
// renderers print "0 rows in set" rather than "Empty set" — no row headers.
func TestRenderVertical_ZeroRowsWithColumns(t *testing.T) {
	ui.Std = ui.Caps{Color: false, Unicode: false, TTY: false}
	out := renderVertical([]string{"id"}, [][]*string{}, 5)
	if !strings.Contains(out, "0 rows in set") {
		t.Errorf("expected '0 rows in set': %q", out)
	}
	if strings.Contains(out, "row ***") {
		t.Errorf("did not expect a row header for zero rows: %q", out)
	}

	tableOut := renderTable([]string{"id"}, [][]*string{}, 5)
	if !strings.Contains(tableOut, "0 rows in set") {
		t.Errorf("renderTable diverges from renderVertical for zero-row parity: %q", tableOut)
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
