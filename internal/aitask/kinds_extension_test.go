package aitask

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func fillResult(mut func(m map[string]any)) string {
	m := map[string]any{
		"summary":               "Filled 2 fields on step 1 of 3.",
		"stopped_before_submit": true,
		"fields": []any{
			map[string]any{"label": "Full name", "value": "Ada Lovelace", "source": "profile: name", "generated": false},
			map[string]any{"label": "Why us", "value": "I like compilers.", "source": "generated", "generated": true},
		},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func askResult(mut func(m map[string]any)) string {
	m := map[string]any{
		"summary":           "The page is a checkout form.",
		"answer":            "The Pay button is disabled until the terms box is ticked.",
		"suggested_actions": []any{"Tick the terms box."},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func decode(t *testing.T, doc string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExtensionKindsAreRegisteredWithTheLowestAccessThatWorks(t *testing.T) {
	fill, err := Lookup(KindExtensionFill)
	if err != nil || fill.DefaultAccess != AccessWorkspace || fill.UnattendedOK {
		t.Fatalf("extension-fill = %+v %v", fill, err)
	}
	ask, err := Lookup(KindExtensionAsk)
	if err != nil || ask.DefaultAccess != AccessQA || ask.UnattendedOK {
		t.Fatalf("extension-ask = %+v %v", ask, err)
	}
}

func TestExtensionFillSchema(t *testing.T) {
	over := strings.Repeat("x", extMaxValue+1)
	many := make([]any, extMaxFields+1)
	for i := range many {
		many[i] = map[string]any{"label": "l", "value": "v", "source": "s", "generated": false}
	}
	cases := map[string]struct {
		doc  string
		want string // "" accepted
	}{
		"complete":                     {fillResult(nil), ""},
		"no fields filled":             {fillResult(func(m map[string]any) { m["fields"] = []any{} }), ""},
		"stopped_before_submit false":  {fillResult(func(m map[string]any) { m["stopped_before_submit"] = false }), "stopped_before_submit: must be true"},
		"stopped_before_submit absent": {fillResult(func(m map[string]any) { delete(m, "stopped_before_submit") }), `missing required property "stopped_before_submit"`},
		"stopped_before_submit string": {fillResult(func(m map[string]any) { m["stopped_before_submit"] = "true" }), "must be true"},
		"a submitted claim":            {fillResult(func(m map[string]any) { m["submitted"] = true }), `unexpected property "submitted"`},
		"oversized value": {fillResult(func(m map[string]any) {
			m["fields"] = []any{map[string]any{"label": "Bio", "value": over, "source": "s", "generated": true}}
		}), "must be at most"},
		"too many fields":   {fillResult(func(m map[string]any) { m["fields"] = many }), "must have at most"},
		"oversized summary": {fillResult(func(m map[string]any) { m["summary"] = strings.Repeat("s", extMaxSummary+1) }), "must be at most"},
		"empty label": {fillResult(func(m map[string]any) {
			m["fields"] = []any{map[string]any{"label": "", "value": "v", "source": "s", "generated": false}}
		}), "must be at least 1"},
		"generated missing": {fillResult(func(m map[string]any) {
			m["fields"] = []any{map[string]any{"label": "l", "value": "v", "source": "s"}}
		}), `missing required property "generated"`},
	}
	for name, c := range cases {
		err := Validate(ExtensionFillSchema, decode(t, c.doc))
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestExtensionAskSchema(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want string
	}{
		"complete":        {askResult(nil), ""},
		"no actions":      {askResult(func(m map[string]any) { m["suggested_actions"] = []any{} }), ""},
		"no answer":       {askResult(func(m map[string]any) { delete(m, "answer") }), `missing required property "answer"`},
		"empty answer":    {askResult(func(m map[string]any) { m["answer"] = "" }), "must be at least 1"},
		"oversized":       {askResult(func(m map[string]any) { m["answer"] = strings.Repeat("a", extMaxAnswer+1) }), "must be at most"},
		"long action":     {askResult(func(m map[string]any) { m["suggested_actions"] = []any{strings.Repeat("a", extMaxAction+1)} }), "must be at most"},
		"action not text": {askResult(func(m map[string]any) { m["suggested_actions"] = []any{map[string]any{"do": "x"}} }), "want string"},
		"extra property":  {askResult(func(m map[string]any) { m["fields"] = []any{} }), `unexpected property "fields"`},
	}
	for name, c := range cases {
		err := Validate(ExtensionAskSchema, decode(t, c.doc))
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

// Through Service.Complete: a fill that says it did not stop before submit is
// refused and the task stays open; a good one is stored with secrets removed.
func TestCompletingAnExtensionFillTask(t *testing.T) {
	s, l := newService(t)
	task, err := s.Submit(context.Background(), SubmitInput{Kind: KindExtensionFill, Goal: "Fill in the form on the web page open in tab 3."})
	if err != nil {
		t.Fatal(err)
	}
	token := l.tokens[0]

	_, err = s.Complete(task.ID, token, []byte(fillResult(func(m map[string]any) { m["stopped_before_submit"] = false })))
	if code(err) != CodeSchemaInvalid || !strings.Contains(err.Error(), "stopped_before_submit") {
		t.Fatalf("a fill that did not stop was accepted: %v", err)
	}
	if cur, _ := s.Store.Get(task.ID); cur.Status != StatusRunning {
		t.Fatalf("status = %s after a refused result", cur.Status)
	}

	secret := fillResult(func(m map[string]any) {
		m["fields"] = []any{
			map[string]any{"label": "Password", "value": "hunter2", "source": "page", "generated": false},
			map[string]any{"label": "Notes", "value": "ghp_0123456789abcdef0123456789abcdef0123", "source": "page", "generated": false},
			map[string]any{"label": "Full name", "value": "Ada Lovelace", "source": "profile: name", "generated": false},
		}
	})
	done, err := s.Complete(task.ID, token, []byte(secret))
	if err != nil || done.Status != StatusComplete {
		t.Fatalf("complete = %+v %v", done, err)
	}
	got := string(done.Result)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "ghp_") || !strings.Contains(got, "Ada Lovelace") {
		t.Fatalf("stored result = %s", got)
	}
}

func TestCompletingAnExtensionAskTaskRedactsSecrets(t *testing.T) {
	s, l := newService(t)
	task, err := s.Submit(context.Background(), SubmitInput{Kind: KindExtensionAsk, Goal: "Question about the web page open in tab 3: why is Pay disabled?"})
	if err != nil {
		t.Fatal(err)
	}
	doc := askResult(func(m map[string]any) { m["suggested_actions"] = []any{"sk-abcdefghijklmnopqrstuvwxyz0123456789ABCD"} })
	done, err := s.Complete(task.ID, l.tokens[0], []byte(doc))
	if err != nil || strings.Contains(string(done.Result), "sk-abcdef") {
		t.Fatalf("complete = %+v %v", done, err)
	}
}

// These kinds read a page strangers wrote: they never run unattended.
func TestExtensionTasksRefuseToRunUnattended(t *testing.T) {
	s, _ := newService(t)
	for _, kind := range []string{KindExtensionFill, KindExtensionAsk} {
		_, err := s.Submit(context.Background(), SubmitInput{Kind: kind, Goal: "g", Access: AccessQA, Unattended: true})
		if err == nil {
			t.Errorf("%s ran unattended", kind)
		}
	}
}

// Personal form values are the point of a fill: redaction keeps them, still
// redacts what is obviously a secret, and the redacted result is still valid.
func TestRedactionKeepsAFillsPersonalValuesAndRemovesSecrets(t *testing.T) {
	doc := fillResult(func(m map[string]any) {
		m["fields"] = []any{
			map[string]any{"label": "Full name", "value": "Ada Lovelace", "source": "profile: name", "generated": false},
			map[string]any{"label": "Email", "value": "ada@example.com", "source": "profile: email", "generated": false},
			map[string]any{"label": "Phone", "value": "+44 20 7946 0958", "source": "profile: phone", "generated": false},
			map[string]any{"label": "Address", "value": "12 St James's Square, London", "source": "profile: address", "generated": false},
			map[string]any{"label": "Password", "value": "hunter2hunter2", "source": "page", "generated": false},
			map[string]any{"label": "Card number", "value": "4111 1111 1111 1111", "source": "page", "generated": false},
			map[string]any{"label": "One-time code", "value": "493021", "source": "page", "generated": false},
			map[string]any{"label": "Notes", "value": "ghp_0123456789abcdef0123456789abcdef0123", "source": "page", "generated": false},
		}
	})
	red := RedactResult(decode(t, doc))
	b, _ := json.Marshal(red)
	var back any
	_ = json.Unmarshal(b, &back)
	if err := Validate(ExtensionFillSchema, back); err != nil {
		t.Fatalf("redaction made a valid result invalid: %v\n%s", err, b)
	}
	for _, keep := range []string{"Ada Lovelace", "ada@example.com", "+44 20 7946 0958", "12 St James's Square, London"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("redaction removed %q: %s", keep, b)
		}
	}
	for _, gone := range []string{"hunter2hunter2", "4111 1111 1111 1111", "493021", "ghp_0123"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("a secret survived: %q in %s", gone, b)
		}
	}
}

func redactedField(t *testing.T, label, value string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"fields": []any{map[string]any{"label": label, "value": value, "source": "page", "generated": false}}})
	out, _ := json.Marshal(RedactResult(decode(t, string(b))))
	var back struct {
		Fields []struct{ Value string }
	}
	if err := json.Unmarshal(out, &back); err != nil || len(back.Fields) != 1 {
		t.Fatalf("%v %s", err, out)
	}
	return back.Fields[0].Value
}

// A form names its secrets in its own words; the label alone decides.
func TestRedactionOfFormFieldLabels(t *testing.T) {
	for _, label := range []string{
		"One-time code", "One-time code (name)", "Authenticator code", "Authenticator app", "MFA code", "TOTP", "Backup code", "Backup codes",
		"Recovery code", "Confirmation code", "Code from SMS", "6-digit code", "Security code", "Verification code", "SMS code",
		"PIN", "Your PIN", "PIN code", "Pass", "Token", "Access token", "Passcode", "Password (name)", "Card number (id)",
	} {
		if got := redactedField(t, label, "493021"); got != RedactedValue {
			t.Errorf("label %q kept its value: %q", label, got)
		}
	}
	// Ordinary labels that share a word with a secret keep theirs.
	for label, value := range map[string]string{
		"Token Street": "5 Token Street", "Boarding pass": "QR 7788", "Postal code": "SW1Y 4JH", "Zip code": "94105", "Country code": "+44",
		"Area code": "020", "Pin number of the map": "12", "Street": "Tokenizer Lane 5", "Passenger name": "Ada", "Backup contact": "Sam",
		"Full name": "Ada Lovelace", "Time of arrival": "10:30",
	} {
		if got := redactedField(t, label, value); got != value {
			t.Errorf("label %q lost its value: %q -> %q", label, value, got)
		}
	}
	// A secret-shaped value is redacted whatever the label says.
	if got := redactedField(t, "Notes", "ghp_0123456789abcdef0123456789abcdef0123"); got != RedactedValue {
		t.Errorf("a token under an ordinary label survived: %q", got)
	}
}

// JSON keys keep their metadata veto (a count or a name under a token key is no
// secret); form-field labels do not.
func TestRedactionKeepsTheMetadataVetoForKeysOnly(t *testing.T) {
	out, _ := json.Marshal(RedactResult(decode(t, `{"max_tokens":512,"token_name":"deploy","secret_enabled":"yes","otp_type":"sms"}`)))
	if string(out) != `{"max_tokens":512,"otp_type":"sms","secret_enabled":"yes","token_name":"deploy"}` {
		t.Errorf("a metadata key was redacted: %s", out)
	}
}
