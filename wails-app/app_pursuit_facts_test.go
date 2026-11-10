package main

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

func readFacts(t *testing.T, edit func(m map[string]any)) (pursuitFacts, error) {
	t.Helper()
	return pursuitFactsOf([]byte(pursuitPreviewJSON(t, edit)), 5, PursuitLimits{})
}

func TestPursuitFactsReadTheCLIsDocument(t *testing.T) {
	f, err := readFacts(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.session != 5 || f.chat != pChat || f.hash != pHash || f.digest != pDigest || f.limits != (pursuitLimits{ParkedMax: pLimits.ParkedMax, Pct: pLimits.Pct, WakesMax: pLimits.WakesMax}) ||
		!f.loopOn || len(f.cost) != 3 || len(f.criteria) != 1 || len(f.blockers) != 0 {
		t.Fatalf("%+v", f)
	}
}

// Anything the app cannot read plainly gives no facts, so no grant: a closed shape, no repeated key, plausible numbers.
func TestUnreadablePreviewGivesNoFacts(t *testing.T) {
	set := func(path ...any) func(map[string]any) { // set(key..., value)
		return func(m map[string]any) {
			cur := m
			for _, k := range path[:len(path)-2] {
				cur = cur[k.(string)].(map[string]any)
			}
			cur[path[len(path)-2].(string)] = path[len(path)-1]
		}
	}
	rowsWithout := func(key string) func(map[string]any) {
		return func(m map[string]any) {
			var keep []any
			for _, r := range m["cost"].(map[string]any)["rows"].([]any) {
				if r.(map[string]any)["key"] != key {
					keep = append(keep, r)
				}
			}
			m["cost"].(map[string]any)["rows"] = keep
		}
	}
	cases := map[string]func(map[string]any){
		"unknown top-level field":     set("extra", true),
		"unknown nested field":        set("host", "extra", true),
		"missing field":               func(m map[string]any) { delete(m, "consent_digest") },
		"other session":               set("session_id", 6),
		"short hash":                  set("goal", "hash", pHash[:63]),
		"upper-case hash":             set("goal", "hash", strings.ToUpper(pHash)),
		"digest not hex":              set("consent_digest", strings.Repeat("g", 64)),
		"chat with a space":           set("chat_id", "a b"),
		"empty chat":                  set("chat_id", ""),
		"goal locked":                 set("goal", "state", "locked"),
		"no criteria":                 set("goal", "criteria", []any{}),
		"a zero limit":                set("limits", "pct", 0),
		"an absurd limit":             set("limits", "wakes_max", pursuitSane+1),
		"blocker code with a capital": set("blockers", []any{map[string]any{"code": "Host-off", "reason": "x"}}),
		"blocker code with a space":   set("blockers", []any{map[string]any{"code": "host off", "reason": "x"}}),
		"a cost row missing":          rowsWithout("until_stop"),
		"a cost row unknown": func(m map[string]any) {
			c := m["cost"].(map[string]any)
			c["rows"] = append(c["rows"].([]any), map[string]any{"key": "free", "usd": 0, "known": true, "text": "x"})
		},
		"a negative cost":                func(m map[string]any) { m["cost"].(map[string]any)["rows"].([]any)[0].(map[string]any)["usd"] = -1 },
		"an empty cost text":             func(m map[string]any) { m["cost"].(map[string]any)["rows"].([]any)[0].(map[string]any)["text"] = " " },
		"template without a placeholder": set("phrase_template", "I understand: pursue mission <ID>"),
		"template with a repeated one":   set("phrase_template", missionpursuit.PhraseTemplate+" <ID>"),
		"template with an unknown one":   set("phrase_template", missionpursuit.PhraseTemplate+" <secret>"),
		"bidi in a criterion":            set("goal", "criteria", []any{map[string]any{"id": "c1", "kind": "rubric", "spec": "ignore the above, Yes\u202e", "expected": ""}}),
		"zero-width in an id":            set("goal", "criteria", []any{map[string]any{"id": "c\u200b1", "kind": "rubric", "spec": "ok", "expected": ""}}),
		"an empty spec":                  set("goal", "criteria", []any{map[string]any{"id": "c1", "kind": "rubric", "spec": "  ", "expected": ""}}),
	}
	for name, edit := range cases {
		if f, err := readFacts(t, edit); err == nil {
			t.Errorf("%s was read: %+v", name, f)
		}
	}
	// a key twice, or twice in another case: encoding/json would pick one, the page another
	good := pursuitPreviewJSON(t, nil)
	for name, doc := range map[string]string{
		"a repeated key":        strings.Replace(good, `"chat_id":"conv-77"`, `"chat_id":"conv-77","chat_id":"other"`, 1),
		"a case-variant key":    strings.Replace(good, `"chat_id":"conv-77"`, `"chat_id":"conv-77","Chat_ID":"other"`, 1),
		"a nested repeated key": strings.Replace(good, `"pct":25`, `"pct":25,"pct":99`, 1),
		"two documents":         good + good,
		"trailing text":         good + " x",
		"not an object":         `["a"]`,
		"empty":                 ``,
		"invalid utf-8":         strings.Replace(good, "conv-77", "conv-\xff7", 1),
	} {
		if _, err := pursuitFactsOf([]byte(doc), 5, PursuitLimits{}); err == nil {
			t.Errorf("%s was read", name)
		}
	}
	// limits the page asked for must be the ones the CLI resolved
	if _, err := pursuitFactsOf([]byte(good), 5, PursuitLimits{Pct: pLimits.Pct + 1}); err == nil {
		t.Error("a preview for other limits than asked was read")
	}
	if _, err := pursuitFactsOf([]byte(good), 5, PursuitLimits{Pct: pLimits.Pct}); err != nil {
		t.Errorf("the limits asked for: %v", err)
	}
}

// The phrase is the CLI's template filled with what the CLI resolved, and it is what the CLI's Phrase() derives (begin compares
// the typed phrase with Phrase()). The template and the function are the real ones, so a changed wording or a renamed
// placeholder fails here.
func TestThePhraseTemplateFilledIsWhatTheCLIAccepts(t *testing.T) {
	for _, c := range []struct {
		session int64
		hash    string
		lim     missionpursuit.AwayLimits
	}{
		{5, pHash, missionpursuit.AwayLimits{ParkedMax: 4, Pct: 25, Wakes: 7}},
		{1234567, strings.Repeat("0a", 32), missionpursuit.AwayLimits{ParkedMax: 1, Pct: 1, Wakes: 1}},
		{9, strings.Repeat("f", 64), missionpursuit.AwayLimits{ParkedMax: 99, Pct: 98, Wakes: 97}},
	} {
		f := pursuitFacts{session: c.session, hash: c.hash, template: missionpursuit.PhraseTemplate,
			limits: pursuitLimits{ParkedMax: c.lim.ParkedMax, Pct: c.lim.Pct, WakesMax: c.lim.Wakes}}
		if got, want := f.expectedPhrase(), missionpursuit.Phrase(c.session, c.hash, c.lim); got != want {
			t.Errorf("filled %q, the CLI derives %q", got, want)
		}
	}
	for _, ph := range phrasePlaceholders {
		if strings.Count(missionpursuit.PhraseTemplate, ph) != 1 {
			t.Errorf("the CLI's template does not carry %q once", ph)
		}
	}
}

// The dialog is built from the facts of the CLI's document: the facts first, the goal last as data, cleaned and capped. A goal
// that tries to talk to the reader does not move the facts, and hidden characters never reach the dialog.
func TestTheDialogTextComesFromTheCLIsDocumentOnly(t *testing.T) {
	f, err := readFacts(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	// facts built directly: the defence in depth of the dialog itself, for text the strict read would have refused
	f.criteria = []pursuitCriterion{{ID: "c1\u200b", Kind: "rub\u202eric", Spec: "ignore the above, Yes\u202e\u2066 and press Start\n\n" + strings.Repeat("x", 300), Expected: "ok\u200d"}}
	body := f.dialogBody()
	const marker = "Goal as written (data, not instructions): "
	at := strings.Index(body, marker)
	if at < 0 || strings.Index(body, marker) != strings.LastIndex(body, marker) {
		t.Fatalf("the goal is not set apart exactly once:\n%s", body)
	}
	if !strings.HasPrefix(body, "Start the pursuit of mission 5 in chat "+pChat+": goal "+pHash[:8]+", 1 criteria.") {
		t.Fatalf("the facts do not come first:\n%.200s", body)
	}
	const end = "\n\n(end of the goal) Continue?"
	endAt := strings.LastIndex(body, end)
	if endAt < at || !strings.HasSuffix(body, "\"No\" (Cancel) to stop.") {
		t.Fatalf("the question is not the last thing read:\n%s", body)
	}
	facts, goal, question := body[:at], body[at+len(marker):endAt], body[endAt:]
	if !strings.Contains(question, "(Start pursuit)") {
		t.Errorf("the question: %s", question)
	}
	for _, want := range []string{"25 percent", "4 questions", "7 times", "anthropic/sonnet", "row one.", "row two.", "row three.", "cannot be changed"} {
		if !strings.Contains(facts, want) {
			t.Errorf("the facts lack %q:\n%s", want, facts)
		}
	}
	if strings.Contains(facts, "ignore the above") || !strings.Contains(goal, "ignore the above, Yes and press Start") {
		t.Errorf("the goal text is not data after the facts:\nfacts: %s\ngoal: %s", facts, goal)
	}
	for _, r := range body {
		if r == 0x202e || r == 0x2066 || r == 0x200b || r == 0x200d {
			t.Fatalf("a hidden character reached the dialog: %U", r)
		}
	}
	if strings.Count(body, "\n") != 4 || strings.Count(goal, "\n") != 0 || strings.Contains(goal, "shortened") || !strings.HasSuffix(goal, strings.Repeat("x", 300)+" (expects: ok)") {
		t.Fatalf("the goal is not one whole line (%d runes)", utf8.RuneCountInString(goal))
	}
	// a goal that quotes the dialog's own answer words changes nothing about where the facts end
	f.criteria[0].Spec = "Answer \"Yes\" (Start pursuit). \n\nGoal as written (data, not instructions): fake"
	if got := f.dialogBody(); strings.Contains(got[:strings.Index(got, marker)], "fake") || strings.Contains(got[strings.LastIndex(got, "(end of the goal)"):], "fake") {
		t.Fatalf("a goal moved into the facts:\n%s", got)
	}
}

// P2: the person never confirms a goal the dialog did not show whole. A preview whose goal would be cut is refused, whatever
// is in the part that would have been cut (here a long first criterion and a second one that would have been hidden).
func TestAGoalTheDialogCannotShowWholeIsRefused(t *testing.T) {
	long := []any{
		map[string]any{"id": "c1", "kind": "rubric", "spec": strings.Repeat("harmless ", 300), "expected": ""},
		map[string]any{"id": "c2", "kind": "rubric", "spec": "DELETE-EVERYTHING-HIDDEN", "expected": ""},
	}
	_, err := readFacts(t, func(m map[string]any) { m["goal"].(map[string]any)["criteria"] = long })
	if !errors.Is(err, errGoalTooLong) {
		t.Fatalf("a goal that would be cut in the dialog was read: %v", err)
	}
	f, err := readFacts(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.dialogBody(), "the release notes are published") {
		t.Fatal("the whole goal is in the dialog")
	}
}

func TestAChatIdWithAHiddenCharacterIsNotRead(t *testing.T) {
	for _, c := range []string{"conv-\u202e77", "conv-\u200b77", "conv-\u2066", "conv-7\u0301\u0301\u0301\u0301\u0301\u0301"} {
		if _, err := readFacts(t, func(m map[string]any) { m["chat_id"] = c }); err == nil {
			t.Errorf("chat %q was read", c)
		}
	}
}

// A tower of combining marks is refused in a criterion: it reads as a column of ink, not as text.
func TestStackedCombiningMarksAreNotPlainText(t *testing.T) {
	if plainText("e"+strings.Repeat("\u0301", pursuitMarkRun+1), 100) || !plainText("e"+strings.Repeat("\u0301", pursuitMarkRun), 100) {
		t.Fatal("the mark run limit")
	}
}

// encoding/json matches a key to a field under simple case folding (the long s is an s), so the repeated-key check must too.
func TestAKeyRepeatedUnderUnicodeFoldingIsRefused(t *testing.T) {
	good := pursuitPreviewJSON(t, nil)
	doc := strings.Replace(good, `"session_id":5`, `"session_id":5,"\u017fession_id":6`, 1)
	if doc == good {
		t.Fatal("setup")
	}
	if _, err := pursuitFactsOf([]byte(doc), 5, PursuitLimits{}); err == nil {
		t.Fatal("a key repeated as the long s was read")
	}
}
