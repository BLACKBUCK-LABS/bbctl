package commands

import (
	"fmt"
	"strings"

	"github.com/blackbuck/bbctl/internal/ui"
)

func renderTable(columns []string, rows [][]*string, durationMs int64) string {
	if len(columns) == 0 {
		return fmt.Sprintf("Empty set (%s)\n\n", fmtDuration(durationMs))
	}
	rowWord := "rows"
	if len(rows) == 1 {
		rowWord = "row"
	}
	return ui.Table(columns, rows) +
		fmt.Sprintf("%d %s in set (%s)\n\n", len(rows), rowWord, fmtDuration(durationMs))
}

// renderVertical renders columns + rows in MySQL's \G style: one
// "*** N. row ***" header per row, followed by right-aligned "field: value"
// lines. Used when the query was terminated with \G instead of ;.
func renderVertical(columns []string, rows [][]*string, durationMs int64) string {
	if len(columns) == 0 {
		return fmt.Sprintf("Empty set (%s)\n\n", fmtDuration(durationMs))
	}
	width := 0
	for _, c := range columns {
		if len(c) > width {
			width = len(c)
		}
	}
	stars := strings.Repeat("*", 27)
	var b strings.Builder
	for r, row := range rows {
		fmt.Fprintf(&b, "%s %d. row %s\n", stars, r+1, stars)
		for i, c := range columns {
			val := "NULL"
			if i < len(row) && row[i] != nil {
				val = *row[i]
			}
			fmt.Fprintf(&b, "%*s: %s\n", width, c, val)
		}
	}
	rowWord := "rows"
	if len(rows) == 1 {
		rowWord = "row"
	}
	fmt.Fprintf(&b, "%d %s in set (%s)\n\n", len(rows), rowWord, fmtDuration(durationMs))
	return b.String()
}

func renderOK(rowsAffected, lastInsertID int64, durationMs int64) string {
	var msg string
	if lastInsertID > 0 {
		msg = fmt.Sprintf("Query OK, %d row(s) affected, last insert id: %d  (%s)",
			rowsAffected, lastInsertID, fmtDuration(durationMs))
	} else {
		msg = fmt.Sprintf("Query OK, %d row(s) affected  (%s)",
			rowsAffected, fmtDuration(durationMs))
	}
	return ui.Success(msg) + "\n\n"
}

func ticketCard(ticketKey, ticketURL, rerun string) string {
	fields := []ui.Field{{Key: "Ticket", Value: ticketKey}}
	if ticketURL != "" {
		fields = append(fields, ui.Field{Key: "URL", Value: ticketURL})
	}
	fields = append(fields,
		ui.Field{Key: "Status", Value: "awaiting manager approval"},
		ui.Field{Key: "Re-run", Value: rerun})
	return ui.Card("Approval required", fields)
}

func renderRestricted(ticketKey, ticketURL, sql string) string {
	return "\n" + ticketCard(ticketKey, ticketURL, sql) + "\n"
}

func renderError(code int, message string) string {
	var msg string
	if code != 0 {
		msg = fmt.Sprintf("ERROR %d: %s", code, message)
	} else {
		msg = fmt.Sprintf("ERROR: %s", message)
	}
	return ui.Err(msg) + "\n\n"
}

func fmtDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("0.%03d sec", ms)
	}
	return fmt.Sprintf("%.3f sec", float64(ms)/1000)
}
