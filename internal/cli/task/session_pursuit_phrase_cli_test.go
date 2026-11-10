package task

import (
	"strconv"
	"strings"
	"testing"
)

// The app does not write the phrase: it fills the CLI's phrase_template with the values the CLI resolved, using the placeholder
// names below (wails-app/app_pursuit_facts.go: phrasePlaceholders). This is the real begin: the phrase so filled from a real
// preview is accepted, a phrase filled with any other value is not, and the template carries each placeholder exactly once.
// Not parallel: newBeginWorld switches the process-wide session token (a parallel run of the begin tests can race on it).
func TestPursuitBeginAcceptsThePhraseTheAppFillsFromThePreviewTemplate(t *testing.T) {
	w := newBeginWorld(t)
	p := w.preview(t, "--pct", "25", "--parked-max", "4", "--wakes-max", "7")
	fill := func(pct, parked, wakes int) string {
		return strings.NewReplacer("<ID>", strconv.FormatInt(w.s, 10), "<first 8 characters of the hash>", p.Goal.Hash[:8],
			"<percent>", strconv.Itoa(pct), "<parked>", strconv.Itoa(parked), "<wakes>", strconv.Itoa(wakes)).Replace(p.PhraseTpl)
	}
	for _, ph := range []string{"<ID>", "<first 8 characters of the hash>", "<percent>", "<parked>", "<wakes>"} {
		if strings.Count(p.PhraseTpl, ph) != 1 {
			t.Fatalf("the template does not carry %q exactly once: %q", ph, p.PhraseTpl)
		}
	}
	args := w.beginArgs(p, "--pct", "25", "--parked-max", "4", "--wakes-max", "7")
	for _, wrong := range []string{fill(26, 4, 7), fill(25, 5, 7), fill(25, 4, 8), p.PhraseTpl} {
		w.refused(t, 3, "invalid_input", append(append([]string{}, args...), "--gui-phrase="+wrong)...)
	}
	var out struct{ Via string }
	mustTaskJSON(t, w.db, "default", &out, "", append(append([]string{}, args...), "--gui-phrase="+fill(p.Limits["pct"], p.Limits["parked_max"], p.Limits["wakes_max"]))...)
	if out.Via != "gui" {
		t.Fatalf("via = %q", out.Via)
	}
}
