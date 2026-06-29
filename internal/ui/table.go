package ui

import (
	"fmt"
	"strings"
)

// Table renders columns + rows as a MySQL-style grid. On a color TTY the
// header is brand-bold and NULL cells are dimmed; otherwise plain ASCII.
func Table(columns []string, rows [][]*string) string {
	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = len(c)
	}
	for _, row := range rows {
		for i, v := range row {
			n := 4 // len("NULL")
			if v != nil {
				n = len(*v)
			}
			if i < len(widths) && n > widths[i] {
				widths[i] = n
			}
		}
	}
	sep := func() string {
		var b strings.Builder
		b.WriteByte('+')
		for _, w := range widths {
			b.WriteString(strings.Repeat("-", w+2))
			b.WriteByte('+')
		}
		b.WriteByte('\n')
		return b.String()
	}()

	var b strings.Builder
	b.WriteString(sep)
	// header
	b.WriteByte('|')
	for i, c := range columns {
		cell := fmt.Sprintf(" %-*s ", widths[i], c)
		b.WriteString(Render(Brand.Bold(true), cell))
		b.WriteByte('|')
	}
	b.WriteByte('\n')
	b.WriteString(sep)
	// rows
	for _, row := range rows {
		b.WriteByte('|')
		for i, w := range widths {
			val, isNull := "NULL", true
			if i < len(row) && row[i] != nil {
				val, isNull = *row[i], false
			}
			cell := fmt.Sprintf(" %-*s ", w, val)
			if isNull {
				cell = Render(Dim, cell)
			}
			b.WriteString(cell)
			b.WriteByte('|')
		}
		b.WriteByte('\n')
	}
	b.WriteString(sep)
	return b.String()
}
