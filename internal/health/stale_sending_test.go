package health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func staleEnv(info StaleSendingInfo, err error) *Env {
	return &Env{StaleSending: func(context.Context) (StaleSendingInfo, error) { return info, err }}
}

func TestStaleSendingRow(t *testing.T) {
	res := checkStaleSending(context.Background(), staleEnv(StaleSendingInfo{Count: 2, Oldest: 5 * time.Hour}, nil))
	if res.Status != StatusWarn || res.FixID != "" {
		t.Fatalf("stale rows: %+v; want a warning with no automatic fix", res)
	}
	for _, want := range []string{"2", "over 1h", "mark it failed or sent by hand"} {
		if !strings.Contains(res.Summary+res.Detail, want) {
			t.Fatalf("missing %q in %+v", want, res)
		}
	}
	if res := checkStaleSending(context.Background(), staleEnv(StaleSendingInfo{}, nil)); res.Status != StatusSkip || res.Summary != "" {
		t.Fatalf("no stale rows: %+v; want the row left out", res)
	}
	if res := checkStaleSending(context.Background(), staleEnv(StaleSendingInfo{}, errors.New("db busy"))); res.Status != StatusWarn {
		t.Fatalf("read error: %+v; want a warning", res)
	}
	if res := checkStaleSending(context.Background(), &Env{}); res.Status != StatusSkip {
		t.Fatalf("no hook: %+v; want skip", res)
	}
}

func TestStaleSendingIsRegistered(t *testing.T) {
	for _, c := range Default().Checks() {
		if c.ID == CheckStaleSending {
			return
		}
	}
	t.Fatal("check not registered")
}
