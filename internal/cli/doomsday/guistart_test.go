package doomsday

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// The app's start: no terminal, the charter on stdin, the phrase the person typed in the form. The CLI re-derives the
// phrase from the charter it rendered and compares; the caller is never trusted for it.

func TestGUIStartNeedsTheRederivedPhraseAndIsRecordedAsGUI(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	r.terminal = false
	for name, phrase := range map[string]string{
		"empty":       "",
		"wrong":       "yes",
		"short hash":  "I understand: lift 0 lines, charter 1234abcd",
		"wrong count": Phrase(3, h),
		"other hash":  Phrase(0, strings.Repeat("0", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := run(t, r.deps, "", "start", "--charter", file, "--gui-phrase", phrase)
			if err == nil || len(r.e.enters) != 0 {
				t.Fatalf("started on %q: %v", phrase, err)
			}
		})
	}
	out, err := run(t, r.deps, "", "start", "--charter", file, "--gui-phrase", "  "+Phrase(0, h)+"\n")
	if err != nil || len(r.e.enters) != 1 || r.e.enters[0].Kind != "gui" {
		t.Fatalf("gui start: %v %+v %s", err, r.e.enters, out)
	}
	if r.e.enterHash != h {
		t.Fatal("Enter was not handed the hash the person saw")
	}
}

func TestGUIPhraseDoesNotMixWithTheScriptFlags(t *testing.T) {
	file := writeCharter(t, goodCharter)
	r := newRig()
	h := fullHash(t, r, file)
	_, err := run(t, r.deps, "", "start", "--charter", file, "--gui-phrase", Phrase(0, h), "--yes-i-lift", "--hash", h)
	if err == nil || len(r.e.enters) != 0 {
		t.Fatalf("two acknowledgements were accepted: %v", err)
	}
}

func TestGUIStartIsOperatorOnly(t *testing.T) {
	file := writeCharter(t, goodCharter)
	for name, set := range map[string]func(*rig){
		"gate refuses":       func(r *rig) { r.operator = clicore.OperatorOnly("not you") },
		"human-only refusal": func(r *rig) { r.refusal = "a coder session's turn" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			set(r)
			_, err := run(t, r.deps, "", "start", "--charter", file, "--gui-phrase", Phrase(0, strings.Repeat("a", 64)))
			if exitOf(err) != 3 || r.e.canonCalls+len(r.e.enters) != 0 {
				t.Fatalf("the engine was reached by a refused caller: %v %+v", err, r.e)
			}
		})
	}
}

func TestCharterFromStdinRendersAndStartsLikeAFile(t *testing.T) {
	r := newRig()
	out, err := run(t, r.deps, goodCharter, "render", "--charter", "-")
	if err != nil || !strings.Contains(out, "Goal: ship it") {
		t.Fatalf("render from stdin: %v %s", err, out)
	}
	h := strings.Fields(strings.SplitN(out, "\n", 2)[0])[1]
	r.terminal = false
	if _, err := run(t, r.deps, goodCharter, "start", "--charter", "-", "--gui-phrase", Phrase(0, h)); err != nil || len(r.e.enters) != 1 {
		t.Fatalf("start from stdin: %v %+v", err, r.e.enters)
	}
}
