package coderhost

import "time"

// Timestamps in the coder tables are TEXT, written with timeLayout (time.RFC3339Nano) by every released version.
// RFC3339Nano drops a zero fraction and trailing zeros, so "...:00Z" sorts AFTER "...:00.000000001Z" and "...:00.5Z"
// after "...:00.51Z": comparing such a column lexically with a cutoff is wrong by up to a second whenever the two
// fall in the same second. The writers cannot change (released databases hold rows in that spelling, and a table
// that mixes two spellings is worse), so the comparisons are made independent of the spelling instead.
//
// The tsRaw* conditions are exact on a column that holds a UTC timestamp in ANY mix of the two spellings (no
// fraction, trimmed fraction, nine digits). They are exact to the nanosecond: SQLite's julianday()
// and unixepoch() resolve milliseconds only, so they cannot tell a row 1 ns before the cutoff from one 1 ns after.
// The cutoff is spliced in as a literal built from digits, '-', ':', 'T', '.', 'Z' only.
//
// How: the first 19 characters ("YYYY-MM-DDTHH:MM:SS") order whole seconds correctly in both spellings, and a
// plain string bound on them is index-friendly. Only a row in the cutoff's own second needs the fraction, which is
// compared after padding it to nine digits (tsNorm).
//
// UNPARSABLE VALUES. A stored value that is not "YYYY-MM-DDTHH:MM:SS[.f{1,9}]Z" (empty, NULL, a space instead of the
// T, trailing junk, an offset, an impossible month) has no order, and a bare comparison puts it on whichever side
// the characters happen to sort to. Callers therefore never use the tsRaw* conditions; they name what they want:
//
//	tsExpired(col, now)      a DEADLINE (token expires_at) has passed: col <= now. Unparsable = expired.
//	tsLive(col, now)         the deadline has not passed: col > now. Unparsable = not live. (not tsExpired)
//	tsLapsed(col, cut)       an age/lease LIMIT that REVOKES something (ends a spawn, fails a send): col < cut.
//	                         Unparsable = lapsed, so the thing is taken away rather than kept forever.
//	tsInWindow(col, since)   a rate WINDOW that is COUNTED against a cap: col >= since. Unparsable = inside, so
//	                         a bad row can only make the cap tighter, never supply the missing count.
//	tsOlderThan(col, cut)    an age that GRANTS or DELETES (retention prune, cleanup, reset, redelivery, applying a
//	                         default): col < cut AND parsable. Unparsable = never selected, so it is neither deleted
//	                         nor acted on; it stays for an operator.
//
// Rule of thumb: if matching a row takes power away from an agent (or only costs availability), an unparsable row
// matches; if matching it deletes evidence, frees a count or hands something out, it does not. Equality at the
// cutoff belongs to the deadline functions (expired at now), as VerifySessionToken has it (!now.Before(exp)).
// The cutoff is clamped to years 0001..9999 (the stored text has four year digits), so an extreme clock cannot
// mis-slice it.
const tsFixedLayout = "2006-01-02T15:04:05.000000000Z"

var (
	tsMinCut = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	tsMaxCut = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
)

// tsFixed is cut in tsFixedLayout, clamped to years 0001..9999.
func tsFixed(cut time.Time) string {
	cut = cut.UTC()
	if cut.Before(tsMinCut) {
		cut = tsMinCut
	} else if cut.After(tsMaxCut) {
		cut = tsMaxCut
	}
	return cut.Format(tsFixedLayout)
}

// tsOK is the condition "col holds a timestamp in the written spelling, on a real calendar date": year 0001..9999,
// month 1..12, a day that exists in that month (February by the Gregorian leap rule), hour < 24, minute and second
// < 60. It accepts exactly what time.Parse(time.RFC3339Nano) accepts for those years. NULL is not OK.
func tsOK(col string) string {
	const d19 = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]"
	y, m, d := "CAST(substr("+col+", 1, 4) AS INTEGER)", "CAST(substr("+col+", 6, 2) AS INTEGER)", "CAST(substr("+col+", 9, 2) AS INTEGER)"
	days := "CASE WHEN " + m + " IN (1, 3, 5, 7, 8, 10, 12) THEN 31 WHEN " + m + " IN (4, 6, 9, 11) THEN 30" +
		" WHEN " + y + " % 4 = 0 AND (" + y + " % 100 <> 0 OR " + y + " % 400 = 0) THEN 29 ELSE 28 END"
	return "COALESCE((" + col + " GLOB '" + d19 + "Z' OR (" + col + " GLOB '" + d19 + ".[0-9]*Z' AND length(" + col + ") <= 30 AND substr(" + col + ", 21, length(" + col + ") - 21) NOT GLOB '*[^0-9]*'))" +
		" AND " + y + " >= 1 AND " + m + " BETWEEN 1 AND 12 AND " + d + " BETWEEN 1 AND " + days +
		" AND substr(" + col + ", 12, 2) <= '23' AND substr(" + col + ", 15, 2) <= '59' AND substr(" + col + ", 18, 2) <= '59', 0)"
}

// The tsRaw* conditions compare a PARSABLE col (see tsOK) with cut; never use them alone. A cut outside 0001..9999
// cannot be spelled, so it decides every parsable col the same way and is answered by a constant.

// tsRawBefore is "col < cut".
func tsRawBefore(col string, cut time.Time) string {
	if cut.After(tsMaxCut) {
		return "1"
	}
	fixed := tsFixed(cut)
	sec, frac := fixed[:19], fixed[:29]
	return "(" + col + " < '" + sec + "~' AND (" + col + " < '" + sec + "' OR " + tsNorm(col) + " < '" + frac + "'))"
}

// tsRawNotBefore is "col >= cut".
func tsRawNotBefore(col string, cut time.Time) string {
	if cut.After(tsMaxCut) {
		return "0"
	} else if cut.Before(tsMinCut) {
		return "1"
	}
	fixed := tsFixed(cut)
	sec, frac := fixed[:19], fixed[:29]
	return "(" + col + " >= '" + sec + "' AND (" + col + " >= '" + sec + "~' OR " + tsNorm(col) + " >= '" + frac + "'))"
}

// tsRawAtMost is "col <= cut".
func tsRawAtMost(col string, cut time.Time) string {
	if cut.After(tsMaxCut) {
		return "1"
	} else if cut.Before(tsMinCut) {
		return "0"
	}
	fixed := tsFixed(cut)
	sec, frac := fixed[:19], fixed[:29]
	return "(" + col + " < '" + sec + "~' AND (" + col + " < '" + sec + "' OR " + tsNorm(col) + " <= '" + frac + "'))"
}

// tsRawAfter is "col > cut".
func tsRawAfter(col string, cut time.Time) string {
	if cut.After(tsMaxCut) {
		return "0"
	} else if cut.Before(tsMinCut) {
		return "1"
	}
	fixed := tsFixed(cut)
	sec, frac := fixed[:19], fixed[:29]
	return "(" + col + " >= '" + sec + "' AND (" + col + " >= '" + sec + "~' OR " + tsNorm(col) + " > '" + frac + "'))"
}

// tsExpired: the deadline col has passed (col <= now); an unparsable col counts as passed.
func tsExpired(col string, now time.Time) string {
	return "(NOT " + tsOK(col) + " OR " + tsRawAtMost(col, now) + ")"
}

// tsLive: the deadline col has not passed (col > now); an unparsable col is not live.
func tsLive(col string, now time.Time) string {
	return "(" + tsOK(col) + " AND " + tsRawAfter(col, now) + ")"
}

// tsLapsed: col < cut, for limits that revoke; an unparsable col counts as lapsed.
func tsLapsed(col string, cut time.Time) string {
	return "(NOT " + tsOK(col) + " OR " + tsRawBefore(col, cut) + ")"
}

// tsInWindow: col >= since, for windows that are counted against a cap; an unparsable col counts as inside.
func tsInWindow(col string, since time.Time) string {
	return "(NOT " + tsOK(col) + " OR " + tsRawNotBefore(col, since) + ")"
}

// tsOlderThan: col < cut, for deletes and grants; an unparsable col is never older.
func tsOlderThan(col string, cut time.Time) string {
	return "(" + tsOK(col) + " AND " + tsRawBefore(col, cut) + ")"
}

// tsNorm is col as "YYYY-MM-DDTHH:MM:SS.nnnnnnnnn" (no zone: only meaningful within one second, where it is compared).
func tsNorm(col string) string {
	return "(substr(" + col + ", 1, 19) || '.' || substr(CASE WHEN substr(" + col + ", 20, 1) = '.' THEN substr(" + col + ", 21, length(" + col + ") - 21) ELSE '' END || '000000000', 1, 9))"
}
