package doomsday

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Progress toward a counted criterion ("50 applications sent"). The target is read from the charter's own text by the
// host; the count is the judge's reading in its latest judgment (types.CriterionProgress.Count), a MAC-verified row the
// strategy store already holds. Neither is written anywhere new, and neither decides a finish: it is a reading for the
// operator, shown as the judge's opinion.

// CriterionCountView is one counted criterion: its capped text, the target in it and the judge's last reported count
// (nil: the judge has reported none, which is not zero).
type CriterionCountView struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Target int    `json:"target"`
	Count  *int   `json:"count,omitempty"`
}

// CriterionTarget reads a target count out of free text, or says there is none. It is deliberately strict: exactly one
// number in the text, written as digits (thousands commas allowed), standing alone as a count of things (followed by a
// space and a word), between 1 and types.MaxCount. A percentage, a decimal, a date or time, a range, a version, an
// ordinal, a currency amount or a second number all mean no target. It never guesses.
func CriterionTarget(spec string) (int, bool) {
	rs := []rune(spec)
	target, found := 0, false
	for i := 0; i < len(rs); {
		if !isASCIIDigit(rs[i]) {
			if unicode.IsDigit(rs[i]) { // a non-ASCII digit is not read
				return 0, false
			}
			i++
			continue
		}
		j := i
		for j < len(rs) {
			if isASCIIDigit(rs[j]) {
				j++
			} else if rs[j] == ',' && j+3 < len(rs) && isASCIIDigit(rs[j+1]) && isASCIIDigit(rs[j+2]) && isASCIIDigit(rs[j+3]) &&
				(j+4 == len(rs) || !isASCIIDigit(rs[j+4])) {
				j++ // a thousands comma: exactly three digits follow
			} else {
				break
			}
		}
		tok := strings.ReplaceAll(string(rs[i:j]), ",", "")
		if found || !countBoundaries(rs, i, j) {
			return 0, false
		}
		n, err := strconv.Atoi(tok)
		if err != nil || n < 1 || n > types.MaxCount {
			return 0, false
		}
		target, found = n, true
		i = j
	}
	return target, found
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// countBoundaries holds when rs[i:j] stands alone as a count: nothing glued to its front that makes it something else,
// and a space plus a word after it.
func countBoundaries(rs []rune, i, j int) bool {
	if i > 0 {
		switch p := rs[i-1]; {
		case unicode.IsLetter(p), unicode.IsDigit(p), strings.ContainsRune(".,$€£¥#-/:_+", p):
			return false
		}
	}
	if j+1 >= len(rs) || rs[j] != ' ' {
		return false
	}
	return unicode.IsLetter(rs[j+1])
}

// progressViews builds the views for the criteria that name a target, in charter order, reading each one's count from
// the judgment (nil: no judgment yet).
func progressViews(criteria []StrategyCriterion, j *types.Judgment) []CriterionCountView {
	var out []CriterionCountView
	for _, c := range criteria {
		target, ok := CriterionTarget(c.Text)
		if !ok {
			continue
		}
		v := CriterionCountView{ID: c.ID, Text: RedactText(c.Text, strategyTitleCap), Target: target}
		if j != nil {
			for _, p := range j.Criteria {
				if p.ID == c.ID && p.Count != nil {
					n := *p.Count
					v.Count = &n
				}
			}
		}
		out = append(out, v)
	}
	return out
}

// WriteProgress prints one line per counted criterion; nothing when there is none. An unreported count prints as "?".
func WriteProgress(w io.Writer, vs []CriterionCountView) {
	for _, v := range vs {
		n := "?"
		if v.Count != nil {
			n = strconv.Itoa(*v.Count)
		}
		fmt.Fprintf(w, "progress: %s / %d (judge-reported, not verified): %s\n", n, v.Target, v.Text)
	}
}
