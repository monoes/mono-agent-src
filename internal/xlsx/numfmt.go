package xlsx

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// builtInNumFmt holds the built-in number formats this package renders.
//
// Only the date and time ids are here. A stored date is a bare serial number,
// so without the format code it comes back as "45000" instead of a date, which
// is useless to a workflow — that is the problem worth solving. The numeric
// built-ins (currency, percent, fraction, scientific) are display polish on a
// value that is already readable, so a cell using one comes back as its plain
// number. That is a deliberate difference from excelize, which rendered them.
var builtInNumFmt = map[int]string{
	14: "mm-dd-yy",
	15: "d-mmm-yy",
	16: "d-mmm",
	17: "mmm-yy",
	18: "h:mm AM/PM",
	19: "h:mm:ss AM/PM",
	20: "hh:mm",
	21: "hh:mm:ss",
	22: "m/d/yy hh:mm",
	45: "mm:ss",
	46: "[h]:mm:ss",
	47: "mm:ss.0",
}

// Serial bounds. Excel's own grid stops at 9999-12-31, which is serial 2958465
// in the 1900 system and 2957003 in the 1904 one. A serial outside the range
// has no date to show, so the cell keeps its raw number.
const (
	maxSerial1900 = 2958465
	maxSerial1904 = 2957003

	// maxElapsedSerial bounds formats with no calendar field — a clock or an
	// elapsed duration — which the calendar range has no business limiting.
	// Chosen so that the serial and every second derived from it stay exact
	// in a float64 and well inside an int64.
	maxElapsedSerial = 1 << 33
)

// Epochs. Serial 1 is 1900-01-01 in the 1900 system, and serial 0 is
// 1904-01-01 in the 1904 one.
var (
	epoch1900     = time.Date(1899, time.December, 31, 0, 0, 0, 0, time.UTC)
	epoch1900Post = time.Date(1899, time.December, 30, 0, 0, 0, 0, time.UTC)
	epoch1904     = time.Date(1904, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// formatDateTime renders value under the number format code, reporting false
// if the pair is not a date or time to render — a code that is not a date
// format, or a value that is not a serial number in range. Callers fall back
// to the raw value then, which is what Excel shows for a text cell under a
// date format too.
//
// Differences from excelize, all of them cases where excelize contradicted
// Excel and this package follows Excel:
//
//   - excelize converted serials at or below 61 through a Julian calendar and
//     everything above it through a Gregorian one, so the two ranges disagreed
//     with each other. In the 1904 system that made serial 1 render as
//     1900-01-03. Here one model covers the whole range.
//   - excelize offset elapsed-time formats ("[h]:mm:ss") by the epoch, so zero
//     elapsed time in a 1904 workbook showed as 35088 hours. Elapsed time here
//     is measured from zero, as Excel measures it.
//   - excelize let a year past 9999 overflow a two-digit year field, printing
//     "004". Out-of-range serials here keep their raw number.
//   - sub-second digits ("mm:ss.0") landed on whichever side of excelize's
//     two conversion paths the serial fell, so the same fraction rounded
//     differently either side of serial 61. Here the value is rounded once, to
//     the precision the format asks for.
func formatDateTime(value, code string, date1904 bool) (string, bool) {
	toks, kind, ok := parseNumFmt(code)
	if !ok || kind == fmtNotDate {
		return "", false
	}
	serial, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(serial) || math.IsInf(serial, 0) || serial < 0 {
		return "", false
	}

	limit := float64(maxElapsedSerial)
	if kind == fmtDate {
		limit = maxSerial1900 + 1
		if date1904 {
			limit = maxSerial1904 + 1
		}
	}
	if serial >= limit {
		return "", false
	}

	days, secs, frac := splitSerial(serial, fracDigits(toks))
	var y, mo, d int
	var wd time.Weekday
	if kind == fmtDate {
		y, mo, d, wd = calendarDate(days, date1904)
	}
	return render(toks, days, secs, frac, y, mo, d, wd), true
}

// splitSerial breaks a serial into whole days and the time of day, rounding
// once at the precision the format will show so that a value a hair under
// midnight carries into the next day rather than showing 24:00.
func splitSerial(serial float64, digits int) (days int64, secs int, frac float64) {
	d := math.Floor(serial)
	sec := (serial - d) * 86400

	scale := math.Pow(10, float64(digits))
	sec = math.Round(sec*scale) / scale
	if sec >= 86400 {
		d++
		sec -= 86400
	}
	whole := math.Floor(sec)
	return int64(d), int(whole), sec - whole
}

// calendarDate maps a whole-day serial to a calendar date and a weekday.
//
// The 1900 system carries Excel's leap-year bug: serial 60 is 1900-02-29, a
// date that never existed, kept because Lotus 1-2-3 had it and every
// spreadsheet since has matched for compatibility. So serials below 60 name a
// day one later than a straight count from the epoch, serial 60 is the phantom
// day itself, and serials above it line up again. Serial 0 is "1900-01-00",
// the day zero Excel shows for an unset date.
//
// The weekday does not get the same correction. Excel runs one unbroken
// seven-day cycle over the serials, so the phantom day is part of the cycle
// and every date before 1900-03-01 is named a day earlier than the real
// calendar has it — Excel calls 1900-01-01 a Sunday when it was a Monday.
// Matching that is the point: the weekday a workflow reads should be the one
// the spreadsheet shows.
func calendarDate(days int64, date1904 bool) (year, month, day int, wd time.Weekday) {
	n := int(days) // in range: the caller only reaches here inside the calendar bounds
	if date1904 {
		t := epoch1904.AddDate(0, 0, n)
		return t.Year(), int(t.Month()), t.Day(), t.Weekday()
	}
	wd = epoch1900Post.AddDate(0, 0, n).Weekday()
	switch {
	case days == 0:
		return 1900, 1, 0, wd
	case days == 60:
		return 1900, 2, 29, wd
	case days < 60:
		t := epoch1900.AddDate(0, 0, n)
		return t.Year(), int(t.Month()), t.Day(), wd
	default:
		t := epoch1900Post.AddDate(0, 0, n)
		return t.Year(), int(t.Month()), t.Day(), wd
	}
}

// ---------------------------------------------------------------------------
// format code parsing

type tokKind int

const (
	tokLiteral tokKind = iota
	tokYear
	tokMonth
	tokDay
	tokHour
	tokMinute
	tokSecond
	tokFrac
	tokAMPM
	tokElapsedH
	tokElapsedM
	tokElapsedS
)

type token struct {
	kind tokKind
	n    int    // repeat count: "mm" is 2, ".00" is 2
	text string // literal text
}

type fmtKind int

const (
	fmtNotDate fmtKind = iota // not a date or time format at all
	fmtDate                   // has at least one calendar field
	fmtTime                   // clock or elapsed fields only, no calendar
)

// parseNumFmt tokenises a number format code, reporting fmtNotDate for
// anything outside the date and time subset. Rejecting rather than
// half-rendering is the point: a currency or percent code that reached the
// date renderer would produce nonsense, where falling back to the raw number
// produces something a workflow can still use.
func parseNumFmt(code string) ([]token, fmtKind, bool) {
	if code == "" {
		return nil, fmtNotDate, false
	}
	var toks []token
	hasCalendar, hasClock, hasElapsed := false, false, false

	emit := func(k tokKind, n int) {
		toks = append(toks, token{kind: k, n: n})
	}
	lit := func(s string) {
		toks = append(toks, token{kind: tokLiteral, text: s})
	}

	for i := 0; i < len(code); {
		ch := code[i]
		switch {
		case ch == '"':
			// Quoted literal.
			j := strings.IndexByte(code[i+1:], '"')
			if j < 0 {
				return nil, fmtNotDate, false
			}
			lit(code[i+1 : i+1+j])
			i += j + 2

		case ch == '\\':
			// Escaped single character.
			if i+1 >= len(code) {
				return nil, fmtNotDate, false
			}
			lit(string(code[i+1]))
			i += 2

		case ch == '_' || ch == '*':
			// Padding and fill directives take a character and render nothing.
			if i+1 >= len(code) {
				return nil, fmtNotDate, false
			}
			i += 2

		case ch == '[':
			j := strings.IndexByte(code[i:], ']')
			if j < 0 {
				return nil, fmtNotDate, false
			}
			inner := code[i+1 : i+j]
			i += j + 1
			switch {
			case isRun(inner, 'h'):
				emit(tokElapsedH, len(inner))
				hasElapsed = true
			case isRun(inner, 'm'):
				emit(tokElapsedM, len(inner))
				hasElapsed = true
			case isRun(inner, 's'):
				emit(tokElapsedS, len(inner))
				hasElapsed = true
			case strings.HasPrefix(inner, "$"):
				// A locale tag such as [$-409]. Ignored: this package renders
				// en-US month and weekday names only.
			case isColorName(inner):
				// Colours do not affect the text.
			default:
				// A condition such as [>100] means the code has sections that
				// this package does not evaluate.
				return nil, fmtNotDate, false
			}

		case ch == 'y' || ch == 'Y':
			n := runLen(code[i:], 'y')
			emit(tokYear, n)
			hasCalendar = true
			i += n

		case ch == 'm' || ch == 'M':
			n := runLen(code[i:], 'm')
			// Month or minute is decided after the whole code is known.
			emit(tokMonth, n)
			i += n

		case ch == 'd' || ch == 'D':
			n := runLen(code[i:], 'd')
			emit(tokDay, n)
			hasCalendar = true
			i += n

		case ch == 'h' || ch == 'H':
			n := runLen(code[i:], 'h')
			emit(tokHour, n)
			hasClock = true
			i += n

		case ch == 's' || ch == 'S':
			n := runLen(code[i:], 's')
			emit(tokSecond, n)
			hasClock = true
			i += n

		case ch == 'a' || ch == 'A':
			switch {
			case strings.EqualFold(peek(code, i, 5), "AM/PM"):
				emit(tokAMPM, 2)
				i += 5
			case strings.EqualFold(peek(code, i, 3), "A/P"):
				emit(tokAMPM, 1)
				i += 3
			default:
				return nil, fmtNotDate, false
			}

		case ch == '.':
			// ".0" is fractional seconds; a bare "." belongs to a numeric
			// format, which is not ours to render.
			n := runLen(code[i+1:], '0')
			if n == 0 {
				return nil, fmtNotDate, false
			}
			emit(tokFrac, n)
			hasClock = true
			i += n + 1

		case ch == '0' || ch == '#' || ch == '?' || ch == '%' || ch == '@' ||
			ch == ';' || ch == '$' || ch == 'e' || ch == 'E' ||
			ch == 'g' || ch == 'G':
			// Numeric, text, section, era and General syntax: not a date code.
			return nil, fmtNotDate, false

		default:
			lit(string(ch))
			i++
		}
	}

	resolveMinutes(toks)
	for _, t := range toks {
		if t.kind == tokMinute {
			hasClock = true
		} else if t.kind == tokMonth {
			hasCalendar = true
		}
	}

	switch {
	case hasCalendar:
		return toks, fmtDate, true
	case hasClock || hasElapsed:
		return toks, fmtTime, true
	default:
		return nil, fmtNotDate, false
	}
}

// resolveMinutes decides which "m" runs are minutes rather than months. Excel's
// rule: an m directly after an hour field, or directly before a seconds field,
// is minutes. Separators in between do not break the adjacency, so "hh:mm" and
// "mm:ss" both mean minutes while "mm-dd-yy" means months.
func resolveMinutes(toks []token) {
	prev := func(i int) tokKind {
		for j := i - 1; j >= 0; j-- {
			if toks[j].kind != tokLiteral {
				return toks[j].kind
			}
		}
		return tokLiteral
	}
	next := func(i int) tokKind {
		for j := i + 1; j < len(toks); j++ {
			if toks[j].kind != tokLiteral {
				return toks[j].kind
			}
		}
		return tokLiteral
	}
	for i := range toks {
		if toks[i].kind != tokMonth {
			continue
		}
		p, n := prev(i), next(i)
		if p == tokHour || p == tokElapsedH || n == tokSecond || n == tokElapsedS {
			toks[i].kind = tokMinute
		}
	}
}

func fracDigits(toks []token) int {
	for _, t := range toks {
		if t.kind == tokFrac {
			return t.n
		}
	}
	return 0
}

func hasAMPM(toks []token) bool {
	for _, t := range toks {
		if t.kind == tokAMPM {
			return true
		}
	}
	return false
}

func runLen(s string, ch byte) int {
	n := 0
	for n < len(s) && (s[n]|0x20) == ch {
		n++
	}
	return n
}

func isRun(s string, ch byte) bool {
	if s == "" {
		return false
	}
	return runLen(s, ch) == len(s)
}

func peek(s string, i, n int) string {
	if i+n > len(s) {
		return ""
	}
	return s[i : i+n]
}

func isColorName(s string) bool {
	switch strings.ToLower(s) {
	case "black", "blue", "cyan", "green", "magenta", "red", "white", "yellow":
		return true
	}
	return strings.HasPrefix(strings.ToLower(s), "color")
}
