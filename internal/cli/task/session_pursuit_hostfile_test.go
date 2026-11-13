package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// A process holding the host lock can write any plausible wake-up cap into host.json, and the cap the host enforces (its own
// environment) is not readable from here: the money text shows the top of the range a host accepts, whatever the file says, so
// the consent cannot be made to understate what a wake-up may cost.
func TestPursuitConsentShowsTheMostAHostCanEnforceWhateverTheFileSays(t *testing.T) {
	want := coderhost.PursuitCostRows(coderhost.CostInput{WakeCapUSD: coderhost.MaxWakeCapUSD, MissionBudgetUSD: 20, Pct: 20, Wakes: 6, HostReported: true, CapIsBound: true})
	for _, cap := range []string{"0.02", "2", "10"} {
		w := newBeginWorld(t)
		w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":` + cap + `}}`)
		p := w.preview(t)
		if p.Cost.Text != want.Text || !strings.Contains(p.Cost.Text, "The wake-up cap is $10 ") || strings.Contains(p.Cost.Text, "$0.02") {
			t.Fatalf("cap %s: the table understates the cap:\n%s", cap, p.Cost.Text)
		}
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

// The cap a file reports is not an input of the consent: changing it does not move the digest (it is not what the host enforces).
func TestPursuitConsentDigestDoesNotFollowTheReportedWakeCap(t *testing.T) {
	w := newBeginWorld(t)
	p := w.preview(t)
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":5}}`)
	if again := w.preview(t); again.Digest != p.Digest {
		t.Fatal("the digest follows a number host.json can be made to say")
	}
}
