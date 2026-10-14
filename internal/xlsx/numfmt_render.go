package xlsx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// rendering

var (
	monthAbbr = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	monthFull = [...]string{"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"}
)

func render(toks []token, days int64, secs int, frac float64, year, month, day int, wd time.Weekday) string {
	hour, minute, second := secs/3600, (secs/60)%60, secs%60
	twelve := hasAMPM(toks)

	var b strings.Builder
	for _, t := range toks {
		switch t.kind {
		case tokLiteral:
			b.WriteString(t.text)

		case tokYear:
			if t.n <= 2 {
				fmt.Fprintf(&b, "%02d", ((year%100)+100)%100)
			} else {
				fmt.Fprintf(&b, "%04d", year)
			}

		case tokMonth:
			switch {
			case t.n >= 5:
				b.WriteString(monthName(month, monthAbbr[:])[:1])
			case t.n == 4:
				b.WriteString(monthName(month, monthFull[:]))
			case t.n == 3:
				b.WriteString(monthName(month, monthAbbr[:]))
			default:
				pad(&b, int64(month), t.n)
			}

		case tokDay:
			switch {
			case t.n >= 4:
				b.WriteString(wd.String())
			case t.n == 3:
				b.WriteString(wd.String()[:3])
			default:
				pad(&b, int64(day), t.n)
			}

		case tokHour:
			h := hour
			if twelve {
				h = hour % 12
				if h == 0 {
					h = 12
				}
			}
			pad(&b, int64(h), t.n)

		case tokMinute:
			pad(&b, int64(minute), t.n)

		case tokSecond:
			pad(&b, int64(second), t.n)

		case tokFrac:
			scale := math.Pow(10, float64(t.n))
			fmt.Fprintf(&b, ".%0*d", t.n, int(math.Round(frac*scale)))

		case tokAMPM:
			switch {
			case t.n == 1 && hour < 12:
				b.WriteString("A")
			case t.n == 1:
				b.WriteString("P")
			case hour < 12:
				b.WriteString("AM")
			default:
				b.WriteString("PM")
			}

		case tokElapsedH:
			pad(&b, days*24+int64(hour), t.n)

		case tokElapsedM:
			pad(&b, days*1440+int64(hour*60+minute), t.n)

		case tokElapsedS:
			pad(&b, days*86400+int64(secs), t.n)
		}
	}
	return b.String()
}

// monthName indexes a name table, tolerating a month outside 1..12 rather than
// panicking; calendarDate cannot produce one, but the table lookup is the kind
// of place a later change would introduce one.
func monthName(month int, names []string) string {
	if month < 1 || month > len(names) {
		return strconv.Itoa(month)
	}
	return names[month-1]
}

func pad(b *strings.Builder, v int64, width int) {
	if width <= 1 {
		fmt.Fprintf(b, "%d", v)
		return
	}
	fmt.Fprintf(b, "%0*d", width, v)
}
