package coderhost

import "time"

// Timestamps in the coder tables are TEXT, written with timeLayout (time.RFC3339Nano) by every released version.
// RFC3339Nano drops a zero fraction and trailing zeros, so "...:00Z" sorts AFTER "...:00.000000001Z" and "...:00.5Z"
// after "...:00.51Z": comparing such a column lexically with a cutoff is wrong by up to a second whenever the two
// fall in the same second. The writers cannot change (released databases hold rows in that spelling, and a table
// that mixes two spellings is worse), so the comparisons are made independent of the spelling instead.
//
// tsBefore and tsNotBefore return a SQL condition on a column that holds a UTC timestamp in ANY mix of the two
// spellings (no fraction, trimmed fraction, nine digits). They are exact to the nanosecond: SQLite's julianday()
// and unixepoch() resolve milliseconds only, so they cannot tell a row 1 ns before the cutoff from one 1 ns after.
// The cutoff is spliced in as a literal built from digits, '-', ':', 'T', '.', 'Z' only.
//
// How: the first 19 characters ("YYYY-MM-DDTHH:MM:SS") order whole seconds correctly in both spellings, and a
// plain string bound on them is index-friendly. Only a row in the cutoff's own second needs the fraction, which is
// compared after padding it to nine digits (tsNorm).
const tsFixedLayout = "2006-01-02T15:04:05.000000000Z"

// tsBefore is the condition "col < cut".
func tsBefore(col string, cut time.Time) string {
	fixed := cut.UTC().Format(tsFixedLayout)
	sec, frac := fixed[:19], fixed[:29]
	return "(" + col + " < '" + sec + "~' AND (" + col + " < '" + sec + "' OR " + tsNorm(col) + " < '" + frac + "'))"
}

// tsNotBefore is the condition "col >= cut".
func tsNotBefore(col string, cut time.Time) string {
	fixed := cut.UTC().Format(tsFixedLayout)
	sec, frac := fixed[:19], fixed[:29]
	return "(" + col + " >= '" + sec + "' AND (" + col + " >= '" + sec + "~' OR " + tsNorm(col) + " >= '" + frac + "'))"
}

// tsNorm is col as "YYYY-MM-DDTHH:MM:SS.nnnnnnnnn" (no zone: only meaningful within one second, where it is compared).
func tsNorm(col string) string {
	return "(substr(" + col + ", 1, 19) || '.' || substr(CASE WHEN substr(" + col + ", 20, 1) = '.' THEN substr(" + col + ", 21, length(" + col + ") - 21) ELSE '' END || '000000000', 1, 9))"
}
