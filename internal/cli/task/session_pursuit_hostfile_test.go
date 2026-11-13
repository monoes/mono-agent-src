package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// A process holding the host lock can write a plausible small wake-up cap into host.json: the money text never shows a cap below
// the default, so the consent cannot be made to understate what a wake-up may cost.
func TestPursuitConsentNeverShowsAWakeCapBelowTheDefault(t *testing.T) {
	w := newBeginWorld(t)
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":0.02}}`)
	p := w.preview(t)
	want := coderhost.PursuitCostRows(coderhost.CostInput{WakeCapUSD: coderhost.DefaultWakeCapUSD, MissionBudgetUSD: 20, Pct: 20, Wakes: 6, HostReported: true})
	if p.Cost.Text != want.Text || strings.Contains(p.Cost.Text, "$0.02") {
		t.Fatalf("the table understates the cap:\n%s", p.Cost.Text)
	}
}

// Every status reader shows only validated host facts: a forged file is shown as unusable, never as its text.
func TestPursuitStatusReadersIgnoreAForgedHostFile(t *testing.T) {
	for name, forge := range map[string]func(dir string){
		"bidi in the reason": func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "host.json"), []byte(`{"pid":1,"pursuit":{"enabled":false,"reason":"all good ‮ FORGED-TEXT"}}`), 0o600)
		},
		"a symlink to /dev/zero": func(dir string) {
			_ = os.Remove(filepath.Join(dir, "host.json"))
			if err := os.Symlink("/dev/zero", filepath.Join(dir, "host.json")); err != nil {
				t.Skip("no symlinks here")
			}
		},
		"an oversize file": func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "host.json"), []byte(`{"pid":1,"pursuit":{"reason":"`+strings.Repeat("x", 70<<10)+`"}}`), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBeginWorld(t)
			forge(filepath.Join(filepath.Dir(w.db), "coder-host"))
			out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
			if err != nil || strings.Contains(out, "FORGED-TEXT") || strings.Contains(out, "the pursuit loop is ON") {
				t.Fatalf("preview: %v\n%s", err, out)
			}
			out, _, err = runTask(t, w.db, "default", false, "", "session", "pursuit", "readiness", id(w.s))
			if err != nil || strings.Contains(out, "FORGED-TEXT") || !strings.Contains(out, "not usable") {
				t.Fatalf("readiness: %v\n%s", err, out)
			}
		})
	}
}
