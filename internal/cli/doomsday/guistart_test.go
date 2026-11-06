package doomsday

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
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
		"no criteria":       goodCharter,
		"concurrent > live": `{"goal":"g",` + withCriterion + `"limits":{"budget_micro":1,"live_per_tree":5,"max_concurrent":9,"concurrent_above_live":true},"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j"}}`,
		"no budget choice":  `{"goal":"g",` + withCriterion + `"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j"}}`,
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

// With --json stdout is the one JSON document and nothing else (the app parses it): the human render goes to stderr.
func TestStartJSONPrintsOnlyJSONOnStdout(t *testing.T) {
	r := newRig()
	r.terminal = false
	r.deps.JSONOutput = func() bool { return true }
	file := writeCharter(t, guiCharter)
	h := fullHashJSON(t, r, file)
	cmd := NewCommand(r.deps)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"start", "--charter", file, "--gui-phrase", Phrase(0, h)})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var e Entry
	if err := json.Unmarshal(stdout.Bytes(), &e); err != nil || e.RunID == "" {
		t.Fatalf("stdout is not exactly one JSON document: %q (%v)", stdout.String(), err)
	}
	if !strings.Contains(stderr.String(), "Goal: ship it") {
		t.Fatalf("the render went missing: %q", stderr.String())
	}
}

func fullHashJSON(t *testing.T, r *rig, file string) string {
	t.Helper()
	d := r.deps
	d.JSONOutput = func() bool { return true }
	out, err := run(t, d, "", "render", "--charter", file)
	if err != nil {
		t.Fatal(err, out)
	}
	var doc RenderDoc
	if json.Unmarshal([]byte(out), &doc) != nil {
		t.Fatal(out)
	}
	return doc.Hash
}

// A charter with a repeated key is refused (the last value would win silently, so a reader and the CLI could disagree),
// and an empty stdin says so instead of EOF.
func TestCharterRefusesDuplicateKeysAndSaysWhenEmpty(t *testing.T) {
	for name, c := range map[string]string{
		"top level": `{"goal":"a","goal":"b"}`,
		"nested":    `{"goal":"a","limits":{"tree_depth":2,"tree_depth":3}}`,
		"in a list": `{"goal":"a","criteria":[{"kind":"rubric","text":"x","text":"y"}]}`,
	} {
		if _, err := LoadCharter(strings.NewReader(c)); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := LoadCharter(strings.NewReader(goodCharter)); err != nil {
		t.Fatal(err)
	}
	r := newRig()
	_, err := run(t, r.deps, "", "render", "--charter", "-")
	if err == nil || !strings.Contains(err.Error(), "empty") || strings.Contains(err.Error(), "EOF") {
		t.Fatalf("empty stdin: %v", err)
	}
}

// The dialog of the app's start is built from this summary (the CLI's own words about the charter): enough that a
// swapped charter does not look the same. Escaped, and the goal cut to its first 300 runes.
func TestRenderDocCarriesTheSummaryTheDialogIsBuiltFrom(t *testing.T) {
	c := Charter{Goal: "real goal\u202e" + strings.Repeat("x", 400), Criteria: []Criterion{{Kind: "rubric", Text: "a"}, {Kind: "rubric", Text: "b"}},
		Limits: Limits{TreeDepth: 4, LivePerTree: 30, MaxConcurrent: 20, MaxTotal: 100},
		Pins:   Pins{Boss: Pin{Provider: "anth\u200bropic", Model: "opus", Version: "4"}, Verifier: Pin{Provider: "p", Model: "son\nnet", Version: "4"}}}
	d := NewRenderDoc(c, strings.Repeat("a", 64))
	s := d.Summary
	if !strings.HasPrefix(s.Goal, `real goal\u202e`) || len([]rune(s.Goal)) > 340 || s.Criteria != 2 ||
		s.TreeDepth != 4 || s.LivePerTree != 30 || s.MaxConcurrent != 20 || s.MaxTotal != 100 {
		t.Fatalf("%+v", s)
	}
	if s.BossModel != `anth\u200bropic/opus 4` || s.VerifierModel != `p/son\nnet 4` {
		t.Fatalf("models: %q %q", s.BossModel, s.VerifierModel)
	}
}

// Hidden characters of every kind that a model reads and a person does not are shown escaped in the render, pins
// included; and an app start refuses them (and over-long text) outright, since the hash and the boss see all of it.
func TestRenderEscapesHiddenCharactersEverywhere(t *testing.T) {
	hidden := "a\u200bb\u202ec\U000E0041d\ufe0fe\u2028f\x00g\ue000h"
	c := Charter{Goal: hidden, Criteria: []Criterion{{Kind: "command", Command: hidden, Expected: hidden}}, Pins: Pins{Boss: Pin{Provider: hidden, Model: hidden, Version: hidden}, Judging: hidden}}
	out := Render(c, strings.Repeat("a", 64))
	for _, r := range []rune{'\u200b', '\u202e', 0xE0041, '\ufe0f', '\u2028', 0, 0xE000} {
		if strings.ContainsRune(out, r) {
			t.Fatalf("the render carries U+%04X raw:\n%s", r, out)
		}
	}
}

func TestAnAppStartRefusesHiddenCharactersAndTextBeyondTheDisplayCap(t *testing.T) {
	base := func() Charter {
		return Charter{Goal: "g", Criteria: []Criterion{{Kind: "rubric", Text: "r"}}, Limits: Limits{LivePerTree: 30, MaxConcurrent: 30},
			Pins: Pins{Boss: Pin{Provider: "p", Model: "m", Version: "1"}, Verifier: Pin{Provider: "p", Model: "v", Version: "1"}, Judging: "j"}}
	}
	if err := ValidateGUIStart(base()); err != nil {
		t.Fatalf("a plain charter was refused: %v", err)
	}
	for name, mut := range map[string]func(*Charter){
		"goal 2001 runes":   func(c *Charter) { c.Goal = strings.Repeat("é", 2001) },
		"rubric 2001 runes": func(c *Charter) { c.Criteria[0].Text = strings.Repeat("x", 2001) },
		"zero width":        func(c *Charter) { c.Goal = "a\u200bb" },
		"bidi override":     func(c *Charter) { c.Goal = "a\u202eb" },
		"tag character":     func(c *Charter) { c.Criteria[0].Text = "a\U000E0041" },
		"variation select":  func(c *Charter) { c.Goal = "a\ufe0f" },
		"line separator":    func(c *Charter) { c.Goal = "a\u2028b" },
		"nul":               func(c *Charter) { c.Goal = "a\x00b" },
		"private use":       func(c *Charter) { c.Goal = "a\ue000" },
		"pin":               func(c *Charter) { c.Pins.Boss.Model = "m\u200b" },
		"judging":           func(c *Charter) { c.Pins.Judging = "j\U000E0041" },
		"command":           func(c *Charter) { c.Criteria = []Criterion{{Kind: "command", Command: "ls\u200b", Expected: "x"}} },
	} {
		c := base()
		mut(&c)
		if err := ValidateGUIStart(c); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	c := base()
	c.Goal = strings.Repeat("é", 2000) + "\nsecond line\twith a tab"
	c.Goal = strings.Repeat("é", 1990) + "\nx\ty"
	if err := ValidateGUIStart(c); err != nil {
		t.Fatalf("2000 runes with a newline and a tab were refused: %v", err)
	}
}

func TestDuplicateKeysAreRefusedWhateverTheirCase(t *testing.T) {
	for _, c := range []string{`{"goal":"a","Goal":"b"}`, `{"limits":{"tree_depth":2,"TREE_DEPTH":3}}`, `{"criteria":[{"kind":"rubric","Text":"x","text":"y"}]}`} {
		if _, err := LoadCharter(strings.NewReader(c)); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Errorf("%s: %v", c, err)
		}
	}
}

// EVERY string a charter can carry is held to the same controls in an app start: the table is built by reflection over
// the Charter type, so a string field added later is checked too (or the test names it).
func TestEveryCharterStringIsHeldToTheAppStartControls(t *testing.T) {
	enum := map[string]bool{"Criteria.Kind": true, "Lines": true} // validated by Validate (a closed set), not free text
	base := func() Charter {
		return Charter{Goal: "g", Criteria: []Criterion{{Kind: "command", Text: "t", Command: "c", Expected: "e"}}, Limits: Limits{LivePerTree: 30, MaxConcurrent: 30},
			Lines:  []Line{LineSpending},
			Pins:   Pins{Boss: Pin{Provider: "p", Model: "m", Version: "1"}, Verifier: Pin{Provider: "p", Model: "v", Version: "1"}, Judging: "j"},
			PerAsk: PerAsk{Access: "coding", AnswerClasses: []string{"spend", "scope"}}}
	}
	if err := ValidateGUIStart(base()); err != nil {
		t.Fatalf("the base charter was refused: %v", err)
	}
	var paths []string
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.String:
			paths = append(paths, path)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				p := v.Type().Field(i).Name
				if path != "" {
					p = path + "." + p
				}
				walk(v.Field(i), p)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), path)
			}
		}
	}
	walk(reflect.ValueOf(base()), "")
	if len(paths) < 12 {
		t.Fatalf("the walk found only %d strings: %v", len(paths), paths)
	}
	set := func(c *Charter, path, val string) {
		v := reflect.ValueOf(c).Elem()
		for _, name := range strings.Split(path, ".") {
			if v.Kind() == reflect.Slice {
				v = v.Index(0)
			}
			v = v.FieldByName(name)
		}
		if v.Kind() == reflect.Slice {
			v = v.Index(0)
		}
		v.SetString(val)
	}
	for _, path := range paths {
		if enum[path] || enum[strings.SplitN(path, ".", 2)[0]] {
			continue
		}
		for name, bad := range map[string]string{"hidden": "a\u200bb", "tag": "a\U000E0041", "too long": strings.Repeat("x", MaxDisplayRunes+1), "nbsp": "a\u00a0b", "ideographic space": "a\u3000b", "braille blank": "a\u2800b"} {
			c := base()
			set(&c, path, bad)
			if err := ValidateGUIStart(c); err == nil {
				t.Errorf("%s with a %s value was accepted", path, name)
			}
		}
	}
}

func TestRenderEscapesAnswerClassesAndAccess(t *testing.T) {
	c := Charter{Goal: "g", PerAsk: PerAsk{SpendMicro: 1, Tasks: 1, Access: "co\u200bding", Depth: 1, RunSpendMicro: 1, RunTasks: 1, AnswerClasses: []string{"a\nFORGED", "b\u202e"}}}
	out := Render(c, strings.Repeat("a", 64))
	for _, bad := range []string{"\nFORGED", "\u202e", "\u200b"} {
		if strings.Contains(out, bad) {
			t.Fatalf("the render carries %q:\n%s", bad, out)
		}
	}
	s := NewRenderDoc(c, strings.Repeat("a", 64)).Summary
	if !strings.Contains(s.AutoApprove, "spend") || len(s.AnswerClasses) != 2 || strings.ContainsAny(strings.Join(s.AnswerClasses, ""), "\n\u202e") {
		t.Fatalf("summary: %+v", s)
	}
	if NewRenderDoc(Charter{Goal: "g"}, strings.Repeat("a", 64)).Summary.AutoApprove != "nothing" {
		t.Fatal("a charter with no per-ask limits must say it auto-approves nothing")
	}
}

// Other kinds of invisible or look-alike-blank characters are refused in an app start and escaped in the render.
func TestMoreInvisibleCharactersAreHidden(t *testing.T) {
	for _, r := range []rune{0x00a0, 0x2003, 0x3000, 0x2800, 0x3164, 0x115f, 0x1160, 0xffa0, 0x034f, 0x180e, 0x00ad, 0x2060, 0xfeff} {
		if !hiddenRune(r) {
			t.Errorf("U+%04X is not treated as hidden", r)
		}
	}
	for _, r := range []rune{'a', ' ', 'é', '\u5b57', '\U0001f600', '\u0301'} {
		if hiddenRune(r) && r != '\u0301' {
			t.Errorf("U+%04X is treated as hidden", r)
		}
	}
}

func TestDuplicateKeysAreRefusedAfterNormalisation(t *testing.T) {
	for _, c := range []string{"{\"goal\":\"a\",\"\uFF47oal\":\"b\"}"} { // a fullwidth g is a goal after NFKC
		if _, err := LoadCharter(strings.NewReader(c)); err == nil {
			t.Errorf("%q was accepted", c)
		}
	}
}

// charterStrings finds strings behind pointers, in maps (keys and values), arrays and interfaces too.
func TestCharterStringsWalksEveryContainerKind(t *testing.T) {
	s := "ptr"
	type inner struct{ V string }
	v := struct {
		P  *string
		M  map[string]string
		A  [2]string
		I  interface{}
		PS *inner
		N  *string
	}{P: &s, M: map[string]string{"key": "val"}, A: [2]string{"a0", "a1"}, I: "iface", PS: &inner{V: "deep"}}
	got := strings.Join(charterStrings(reflect.ValueOf(v)), ",")
	for _, want := range []string{"ptr", "key", "val", "a0", "a1", "iface", "deep"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not found in %q", want, got)
		}
	}
}

func TestMongolianAndKhmerInvisiblesAreHidden(t *testing.T) {
	for _, r := range []rune{0x180b, 0x180c, 0x180d, 0x180f, 0x17b4, 0x17b5} {
		if !hiddenRune(r) {
			t.Errorf("U+%04X is not treated as hidden", r)
		}
	}
}
