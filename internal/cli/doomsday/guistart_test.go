package doomsday

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// The app's start: no terminal, the charter on stdin, the phrase the person typed in the form. The CLI re-derives the
// phrase from the charter it rendered and compares; the caller is never trusted for it.

// guiCharter is goodCharter with the success criterion an app start requires.
const guiCharter = `{"goal":"ship it","criteria":[{"kind":"rubric","text":"reads well"}],"limits":{"budget_micro":5000000},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"}}`

func TestGUIStartNeedsTheRederivedPhraseAndIsRecordedAsGUI(t *testing.T) {
	file := writeCharter(t, guiCharter)
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
	file := writeCharter(t, guiCharter)
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
	out, err := run(t, r.deps, guiCharter, "render", "--charter", "-")
	if err != nil || !strings.Contains(out, "Goal: ship it") {
		t.Fatalf("render from stdin: %v %s", err, out)
	}
	h := strings.Fields(strings.SplitN(out, "\n", 2)[0])[1]
	r.terminal = false
	if _, err := run(t, r.deps, guiCharter, "start", "--charter", "-", "--gui-phrase", Phrase(0, h)); err != nil || len(r.e.enters) != 1 {
		t.Fatalf("start from stdin: %v %+v", err, r.e.enters)
	}
}

// `render --json` is the app's preview: the hash, the lifted lines by name, the text. It never carries the phrase the
// person has to type (the form shows the hash and the lines and the person writes the phrase from them).
func TestRenderJSONGivesTheHashTheLinesAndTheTextButNotThePhrase(t *testing.T) {
	r := newRig()
	r.deps.JSONOutput = func() bool { return true }
	out, err := run(t, r.deps, goodCharter, "render", "--charter", "-", "--lift", "spending", "--lift", "platform_caps")
	if err != nil {
		t.Fatal(err, out)
	}
	var d RenderDoc
	if json.Unmarshal([]byte(out), &d) != nil || len(d.Hash) != 64 || strings.Join(d.Lines, ",") != "spending,platform_caps" || d.LiftCount != 2 {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(d.Text, "Goal: ship it") || !strings.Contains(d.Text, "does NOT defend against") || !strings.HasPrefix(d.Text, "CHARTER "+d.Hash) {
		t.Fatalf("text: %s", d.Text)
	}
	if strings.Contains(out, Phrase(2, d.Hash)) {
		t.Fatalf("the preview carries the phrase: %s", out)
	}
	if got := strings.NewReplacer("<N>", "2", "<first 8 characters of the hash>", d.Hash[:8]).Replace(PhraseTemplate); got != Phrase(2, d.Hash) {
		t.Fatalf("the template has drifted from Phrase: %q vs %q", got, Phrase(2, d.Hash))
	}
	if d.PhraseTemplate != PhraseTemplate {
		t.Fatalf("template %q", d.PhraseTemplate)
	}
}

// Free text in a charter cannot forge lines of the render the person reads before typing the phrase: control
// characters are escaped and a long text is cut (the hash still covers the real bytes).
func TestRenderEscapesControlCharactersAndCapsLongText(t *testing.T) {
	c := Charter{Goal: "real goal\nFORGED line one\r\x1b[2J", Criteria: []Criterion{{Kind: "rubric", Text: "ok\nFORGED line two"}}}
	out := Render(c, strings.Repeat("a", 64))
	for _, forged := range []string{"\nFORGED", "\x1b", "\r"} {
		if strings.Contains(out, forged) {
			t.Fatalf("the render carries a forged line or control character %q:\n%s", forged, out)
		}
	}
	if !strings.Contains(out, `real goal\nFORGED line one`) {
		t.Fatalf("the newline is not shown escaped:\n%s", out)
	}
	long := Render(Charter{Goal: strings.Repeat("x", 100000)}, strings.Repeat("a", 64))
	if len(long) > 20000 || !strings.Contains(long, "more characters") {
		t.Fatalf("long text is not cut: %d bytes", len(long))
	}
}

// A start from the app is held to what its form requires, whatever the page sent: a success criterion, and
// max concurrent not above live per tree even when the charter chose that on purpose.
func TestGUIStartRefusesWhatTheFormBlocks(t *testing.T) {
	withCriterion := `"criteria":[{"kind":"rubric","text":"reads well"}],`
	for name, c := range map[string]string{
		"no criteria":        goodCharter,
		"concurrent > live":  `{"goal":"g",` + withCriterion + `"limits":{"budget_micro":1,"live_per_tree":5,"max_concurrent":9,"concurrent_above_live":true},"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j"}}`,
		"no budget choice":   `{"goal":"g",` + withCriterion + `"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			r.terminal = false
			file := writeCharter(t, c)
			out, _ := run(t, r.deps, "", "render", "--charter", file)
			h := ""
			if f := strings.Fields(strings.SplitN(out, "\n", 2)[0]); len(f) > 1 {
				h = f[1]
			}
			_, err := run(t, r.deps, "", "start", "--charter", file, "--gui-phrase", Phrase(0, h))
			if err == nil || len(r.e.enters) != 0 {
				t.Fatalf("started: %v", err)
			}
		})
	}
	ok := `{"goal":"g",` + withCriterion + `"limits":{"budget_micro":1},"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j"}}`
	r := newRig()
	r.terminal = false
	file := writeCharter(t, ok)
	h := fullHash(t, r, file)
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--gui-phrase", Phrase(0, h)); err != nil || len(r.e.enters) != 1 {
		t.Fatalf("a complete charter was refused: %v", err)
	}
}
