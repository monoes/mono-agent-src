// Package sqltime orders stored timestamps by the instant they hold.
//
// TEXT timestamp columns hold several forms, depending on who wrote the row:
//
//	2026-10-19 09:04:56                       SQLite CURRENT_TIMESTAMP, datetime('now')
//	2026-10-19 09:04:56.123456789 +0000 UTC   a Go time.Time bound through modernc.org/sqlite (time.Time.String)
//	2026-10-19 12:34:56 +0330 +0330           the same, in another zone
//	2026-10-19T09:04:56Z                      a Go string in time.RFC3339 / RFC3339Nano
//	2026-10-19T09:04:56.000Z                  a fixed-width millisecond layout
//
// A plain ORDER BY compares them as text, so on one day every 'T' form sorts
// after every space form, and a non-UTC zone sorts by its local time.
// ORDER BY SortKey(col) compares instants instead.
package sqltime

import "fmt"

// SortKey returns an SQL expression for col (a column, or any expression such
// as COALESCE(a, b)) that orders it by time: the Julian day of the instant,
// NULL when col is NULL or unreadable. julianday reads every form above but
// time.Time.String's; that one ("date time[.frac] ±hhmm zone") is rewritten
// to "date time[.frac]±hh:mm" first.
func SortKey(col string) string {
	// The position, within what follows the 19 characters of date and time,
	// of the space before a time.Time.String zone offset; 0 for the other forms.
	sp := fmt.Sprintf("instr(substr(%s, 20), ' ')", col)
	return fmt.Sprintf("julianday(CASE WHEN %[2]s > 0 THEN "+
		"substr(%[1]s, 1, 18 + %[2]s) || substr(%[1]s, 20 + %[2]s, 3) || ':' || substr(%[1]s, 23 + %[2]s, 2) "+
		"ELSE %[1]s END)", col, sp)
}
