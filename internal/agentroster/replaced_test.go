package agentroster

import (
	"strings"
	"testing"
	"time"
)

// A superseded model leaves the roster only when its replacement answers, so a
// runtime validated before a release keeps its ready models.
func TestRosterHidesAModelOnlyOnceItsReplacementAnswers(t *testing.T) {
	now := time.Now()
	row := func(model, status string) Result {
		return Result{Runtime: "claude", Model: model, Status: status, ValidatedAt: now}
	}
	listed := func(rows ...Result) string {
		var out []string
		for _, rr := range Build(rows, nil, now, DefaultMaxAge) {
			for _, e := range rr.Models {
				out = append(out, e.Model)
			}
		}
		return strings.Join(out, " ")
	}
	old := row("claude-opus-4-8", StatusOK)
	if got := listed(old); got != "claude-opus-4-8" {
		t.Errorf("alone = %s", got)
	}
	if got := listed(old, row("claude-opus-5-5", StatusAuth)); got != "claude-opus-4-8 claude-opus-5-5" {
		t.Errorf("replacement failed = %s", got)
	}
	for _, newer := range []string{"claude-opus-5-5", "opus"} {
		if got := listed(old, row(newer, StatusOK)); got != newer {
			t.Errorf("replaced by %s = %s", newer, got)
		}
	}
}
