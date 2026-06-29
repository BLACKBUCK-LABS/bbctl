package commands

import (
	"fmt"

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
