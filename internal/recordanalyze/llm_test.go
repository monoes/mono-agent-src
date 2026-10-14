package recordanalyze

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
)

func formEnv(t *testing.T) *Env {
	return &Env{Analysis: analyzeFixture(t, "form-submit")}
}

func TestGenerateValidFirstTime(t *testing.T) {
	r := &stubRunner{answers: []string{answer(t, "form-submit")}}
	out, err := Generate(context.Background(), r, "P", formEnv(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 1 {
		t.Errorf("runs = %d", len(r.prompts))
	}
	// The AI's invalid id is slugged; names follow the fixed values.
	if out.Automation.ID != "acme-crm" || out.Action.Automation != "acme-crm" || out.Names["action"] != "create_contact" {
		t.Errorf("names = %+v / %s", out.Names, out.Automation.ID)
	}
}

func TestGenerateFencedJSON(t *testing.T) {
	r := &stubRunner{answers: []string{answer(t, "list-scrape")}}
	out, err := Generate(context.Background(), r, "P", &Env{Analysis: analyzeFixture(t, "list-scrape")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Action.ActionType != "list_top_stories" || out.Action.Steps[1].Fields["points"] != "span.points" {
		t.Errorf("out = %+v", out.Action)
	}
}

func TestGenerateMalformedThenRepaired(t *testing.T) {
	r := &stubRunner{answers: []string{"Sure! {\"automation\": {\"id\": }", answer(t, "form-submit")}}
	if _, err := Generate(context.Background(), r, "PROMPT", formEnv(t)); err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 2 {
		t.Fatalf("runs = %d", len(r.prompts))
	}
	rp := r.prompts[1]
	if !strings.HasPrefix(rp, "PROMPT") || !strings.Contains(rp, "failed validation") || !strings.Contains(rp, "invalid JSON") {
		t.Errorf("repair prompt = %s", rp)
	}
}

func TestGenerateInvalidTwiceFails(t *testing.T) {
	bad := strings.Replace(answer(t, "form-submit"), `"configKey": "contact.save_button", `, "", 1)
	r := &stubRunner{answers: []string{bad, bad}}
	_, err := Generate(context.Background(), r, "P", formEnv(t))
	if err == nil || !strings.Contains(err.Error(), `step "save" (click) must reference a selector through configKey`) {
		t.Fatalf("err = %v", err)
	}
	if len(r.prompts) != 2 || !strings.Contains(r.prompts[1], "must reference a selector") {
		t.Errorf("repair prompt missing validator errors")
	}
}

func TestCheckOutputProblems(t *testing.T) {
	env := formEnv(t)
	out, probs := parseAndCheck(answer(t, "form-submit"), env)
	if len(probs) != 0 {
		t.Fatalf("clean answer has problems: %v", probs)
	}
	out.Action.SideEffects = "sometimes"
	out.Action.Steps[1].Intent = ""
	out.Action.Steps[2].ConfigKey = "missing.key"
	// Break both selector checks: a candidate naming two kinds, and an
	// entry without candidates.
	save := out.Selectors["contact.save_button"]
	save.Candidates[0].Text = "Save"
	out.Selectors["contact.save_button"] = save
	out.Selectors["contact.empty"] = action.SelectorEntry{Intent: "nothing"}
	out.Action.Steps = append(out.Action.Steps, out.Action.Steps[0])
	out.Action.Steps[len(out.Action.Steps)-1].Type = "call_fragment"
	out.Action.Steps[len(out.Action.Steps)-1].Fragment = "nope"
	probs = CheckOutput(out, env)
	joined := strings.Join(probs, "\n")
	for _, want := range []string{"sideEffects \"sometimes\"", `step "email" (type) has no intent`,
		`configKey "missing.key" is not defined`, `fragment "nope" not found`,
		`selectors["contact.save_button"].candidates[0] must set exactly one of css, xpath, aria, text`,
		`selectors["contact.empty"] has no candidates`} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestFixNamesAgainstTarget(t *testing.T) {
	env := &Env{Analysis: analyzeFixture(t, "form-submit"), Target: &Existing{
		Actions:   map[string]bool{"create_contact": true},
		Fragments: map[string]*actionFragment{"dismiss_banner": {}},
	}}
	env.Target.Manifest.ID, env.Target.Manifest.Name = "acme", "Acme"
	out := &Output{Automation: DraftAutomation{ID: "whatever"}}
	out.Action.ActionType = "create_contact"
	out.Fragments = []actionFragmentDef{{Name: "Dismiss Banner"}}
	out.Action.Steps = []actionStep{{ID: "b", Type: "call_fragment", Fragment: "Dismiss Banner"}}
	FixNames(out, env)
	if out.Action.ActionType != "create_contact_2" || out.Automation.ID != "acme" {
		t.Errorf("action %q automation %q", out.Action.ActionType, out.Automation.ID)
	}
	if out.Fragments[0].Name != "dismiss_banner_2" || out.Action.Steps[0].Fragment != "dismiss_banner_2" {
		t.Errorf("fragment rename: %q / %q", out.Fragments[0].Name, out.Action.Steps[0].Fragment)
	}
}

func TestFixNamesNewAutomationCollision(t *testing.T) {
	env := &Env{Analysis: analyzeFixture(t, "form-submit"), AutomationTaken: func(id string) bool { return id == "acme-crm" }}
	out := &Output{Automation: DraftAutomation{Name: "Acme CRM"}}
	out.Action.ActionType = "Create Contact"
	FixNames(out, env)
	if out.Automation.ID != "acme-crm-2" || out.Action.ActionType != "create_contact" {
		t.Errorf("got %q / %q", out.Automation.ID, out.Action.ActionType)
	}
}

func TestExtractJSON(t *testing.T) {
	for in, want := range map[string]string{
		"```json\n{\"a\":1}\n```":    `{"a":1}`,
		"text {\"a\":{\"b\":2}} end": `{"a":{"b":2}}`,
		"no json":                    "",
	} {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q", in, got)
		}
	}
}

func TestRunnerHints(t *testing.T) {
	for msg, want := range map[string]string{
		"auth_error 401 Unauthorized":                          "no claude credentials? run `monomind doctor`",
		`exec: "monomind": executable file not found in $PATH`: "not installed",
		"the turn stopped early (timeout)":                     "timed out",
		"the runtime exited with code 3":                       "check the claude runtime with `monomind doctor`",
	} {
		err := withRunnerHint(errors.New(msg), "claude")
		if !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q → %v", msg, err)
		}
	}
}

func TestStderrTailRedacted(t *testing.T) {
	w := &tailWriter{max: 32}
	_, _ = w.Write([]byte(strings.Repeat("x", 40)))
	_, _ = w.Write([]byte("END"))
	if s := w.String(); len(s) != 32 || !strings.HasSuffix(s, "END") {
		t.Errorf("tail = %q", s)
	}
	stderr := "Error: 401 invalid x-api-key\nAuthorization: Bearer eyJhbGciOi.abc.def\n" +
		"ANTHROPIC_API_KEY=sk-ant-abcdefghijklmnopqrstuv\ncallback https://x.test/cb?code=zz123&ok=1\npassword: hunter2\n"
	err := withStderrTail(errors.New("the runtime exited with code 1"), stderr)
	msg := err.Error()
	for _, leak := range []string{"eyJhbGciOi", "sk-ant-abcdefghijklmnopqrstuv", "zz123", "hunter2"} {
		if strings.Contains(msg, leak) {
			t.Errorf("stderr tail leaks %q:\n%s", leak, msg)
		}
	}
	if !strings.Contains(msg, "runner stderr") || !strings.Contains(msg, "401 invalid") || !strings.Contains(msg, "exited with code 1") {
		t.Errorf("tail missing: %s", msg)
	}
	if withStderrTail(errors.New("e"), "  \n").Error() != "e" {
		t.Error("empty tail should add nothing")
	}
}
