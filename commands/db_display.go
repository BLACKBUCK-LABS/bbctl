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
	widths := columnWidths(columns, rows)
	sep := buildSeparator(widths)
	var sb strings.Builder
	sb.WriteString(sep)
	sb.WriteString(buildRow(strPtrs(columns), widths))
	sb.WriteString(sep)
	for _, row := range rows {
		sb.WriteString(buildRow(row, widths))
	}
	sb.WriteString(sep)
	rowWord := "rows"
	if len(rows) == 1 {
		rowWord = "row"
	}
	fmt.Fprintf(&sb, "%d %s in set (%s)\n\n", len(rows), rowWord, fmtDuration(durationMs))
	return sb.String()
}

func renderOK(rowsAffected, lastInsertID int64, durationMs int64) string {
	if lastInsertID > 0 {
		return fmt.Sprintf("Query OK, %d row(s) affected, last insert id: %d  (%s)\n\n",
			rowsAffected, lastInsertID, fmtDuration(durationMs))
	}
	return fmt.Sprintf("Query OK, %d row(s) affected  (%s)\n\n",
		rowsAffected, fmtDuration(durationMs))
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
	if code != 0 {
		return fmt.Sprintf("ERROR %d: %s\n\n", code, message)
	}
	return fmt.Sprintf("ERROR: %s\n\n", message)
}

func columnWidths(columns []string, rows [][]*string) []int {
	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = len(col)
	}
	for _, row := range rows {
		for i, val := range row {
			if i < len(widths) && val != nil && len(*val) > widths[i] {
				widths[i] = len(*val)
			}
		}
	}
	return widths
}

func buildSeparator(widths []int) string {
	var sb strings.Builder
	sb.WriteByte('+')
	for _, w := range widths {
		sb.WriteString(strings.Repeat("-", w+2))
		sb.WriteByte('+')
	}
	sb.WriteByte('\n')
	return sb.String()
}

func buildRow(values []*string, widths []int) string {
	var sb strings.Builder
	sb.WriteByte('|')
	for i, w := range widths {
		val := "NULL"
		if i < len(values) && values[i] != nil {
			val = *values[i]
		}
		sb.WriteByte(' ')
		sb.WriteString(val)
		sb.WriteString(strings.Repeat(" ", w-len(val)+1))
		sb.WriteByte('|')
	}
	sb.WriteByte('\n')
	return sb.String()
}

func strPtrs(ss []string) []*string {
	out := make([]*string, len(ss))
	for i := range ss {
		out[i] = &ss[i]
	}
	return out
}

func fmtDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("0.%03d sec", ms)
	}
	return fmt.Sprintf("%.3f sec", float64(ms)/1000)
}
