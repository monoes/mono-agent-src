package aitask

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The fake secrets are assembled here so that no key-shaped literal sits in
// the source.
var (
	fakeSK     = "s" + "k-live-abcdefghijklmnop0123"
	fakeGH     = "g" + "hp_abcdefghijklmnopqrstuvwxyz0123"
	fakeAWS    = "A" + "KIAABCDEFGHIJKLMNOP"
	fakeJWT    = "e" + "yJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc_def-ghi"
	fakeSlack  = "x" + "oxb-1234567890-abcdefghij"
	fakeBearer = "abcdefghijklmnop.qrstuv"
	fakeQuery  = "abcdefghijklmnopqrstuvwxyz"
)

func redactJSON(t *testing.T, in string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(RedactResult(v))
	return string(b)
}

// A secret inside prose is removed, wherever it sits in the string.
func TestRedactResultFindsSecretsInsideProse(t *testing.T) {
	for name, tc := range map[string]struct{ in, leaked string }{
		"api keys":        {`{"answer":"your key is ` + fakeSK + ` and also ` + fakeGH + `"}`, "abcdefghijklmnop"},
		"bearer header":   {`{"note":"Authorization: Bearer ` + fakeBearer + `"}`, fakeBearer},
		"access_token":    {`{"url":"https://x.test/cb?access_token=` + fakeQuery + `&a=1"}`, fakeQuery},
		"plain token":     {`{"url":"https://x.test/cb?a=1&token=abcdefghijkl"}`, "abcdefghijkl"},
		"aws key":         {`{"t":"use ` + fakeAWS + ` for s3"}`, fakeAWS},
		"jwt":             {`{"t":"cookie ` + fakeJWT + ` ok"}`, "abc_def-ghi"},
		"slack":           {`{"t":"` + fakeSlack + `"}`, "1234567890"},
		"nested in array": {`{"l":[{"m":"see ` + fakeSK + ` here"}]}`, "abcdefghijklmnop0123"},
	} {
		out := redactJSON(t, tc.in)
		if strings.Contains(out, tc.leaked) {
			t.Errorf("%s: leaked %q: %s", name, tc.leaked, out)
		}
		if !strings.Contains(out, RedactedValue) {
			t.Errorf("%s: nothing redacted: %s", name, out)
		}
	}
}

func TestRedactResultKeepsTheRestOfTheSentence(t *testing.T) {
	out := redactJSON(t, `{"a":"call me at 5 pm, key is `+fakeSK+` thanks"}`)
	if !strings.Contains(out, "call me at 5 pm, key is") || !strings.Contains(out, "thanks") {
		t.Errorf("the prose around the secret was lost: %s", out)
	}
}

// Labels for identity numbers keep no value.
func TestRedactResultLabelsForIdentityNumbers(t *testing.T) {
	for _, label := range []string{"Passport number", "Card no.", "Social security number", "IBAN", "Card number"} {
		out := redactJSON(t, `{"fields":[{"label":"`+label+`","value":"X1234567"}]}`)
		if strings.Contains(out, "X1234567") {
			t.Errorf("label %q kept its value: %s", label, out)
		}
	}
	// A key named like one.
	if out := redactJSON(t, `{"passport":"X1234567","iban":"DE89370400440532013000"}`); strings.Contains(out, "X1234567") || strings.Contains(out, "DE89") {
		t.Errorf("secret-named keys kept their value: %s", out)
	}
}

// A placeholder names a secret; it is what a valid workflow holds. It stays,
// under a secret-named key and inside a header value.
func TestRedactResultNeverRedactsPlaceholders(t *testing.T) {
	in := `{"config":{"api_key":"{{secret:k}}","Authorization":"Bearer {{secret:a_rather_long_name_api}}","token":"REPLACE_ME","url":"https://x.test/?access_token={{secret:tok_long_name_here}}","label":"Password","value":"{{secret:pw}}"}}`
	out := redactJSON(t, in)
	for _, want := range []string{`{{secret:k}}`, `Bearer {{secret:a_rather_long_name_api}}`, `REPLACE_ME`, `access_token={{secret:tok_long_name_here}}`, `{{secret:pw}}`} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was redacted: %s", want, out)
		}
	}
	if strings.Contains(out, RedactedValue) {
		t.Errorf("a placeholder was replaced: %s", out)
	}
}

// Ordinary prose, and numbers that merely sit under a *tokens key, are not secrets.
func TestRedactResultLeavesOrdinaryTextAlone(t *testing.T) {
	in := `{"summary":"The token bucket limits requests; a password policy applies. Keys and passports are discussed in the tokenizer notes. See sk-learn and the bearer of this letter.","config":{"max_tokens":512,"stream":true,"secret_enabled":false},"url":"https://x.test/a?page=2&token=true"}`
	var want any
	_ = json.Unmarshal([]byte(in), &want)
	wantB, _ := json.Marshal(want)
	if got := redactJSON(t, in); got != string(wantB) {
		t.Errorf("ordinary content changed:\n got %s\nwant %s", got, wantB)
	}
}

// RE2 is linear: a near megabyte of hostile near-matches finishes quickly and
// the secret at the end still goes.
func TestRedactResultWorkIsBounded(t *testing.T) {
	big := strings.Repeat("Bearer s"+"k-aaaaaaaaaaaaaaaa?token=ab g"+"hp_ e"+"yJ.", 20000)
	big += " " + fakeSK
	start := time.Now()
	out, _ := RedactResult(map[string]any{"a": big}).(map[string]any)["a"].(string)
	// A quadratic version takes about half an hour on this input; the limit only has to
	// stay clear of a loaded -race machine.
	if d := time.Since(start); d > 60*time.Second {
		t.Fatalf("redacting %d bytes took %v", len(big), d)
	}
	if strings.Contains(out, "abcdefghijklmnop0123") {
		t.Error("the secret at the end survived")
	}
}

// ---- round 2: whole-value placeholders, more shapes, no changed types ----

func TestRedactResultPlaceholdersMustBeWholeAndKnown(t *testing.T) {
	for name, tc := range map[string]struct{ in, leaked string }{
		"braces around a key":   {`{"v":"{{` + "g" + `hp_REALREALREALREAL0123456789}}"}`, "REALREAL"},
		"secret-named key":      {`{"password":"{{Hunter2RealPass!}}"}`, "Hunter2RealPass"},
		"prose label":           {`{"v":"password: {{Hunter2RealPass!}}"}`, "Hunter2RealPass"},
		"query after braces":    {`{"u":"https://x.test/?api_key={{a}}RealSecretValue12345"}`, "RealSecretValue12345"},
		"bearer after braces":   {`{"h":"Bearer {{a}}RealSecretToken1234567890"}`, "RealSecretToken1234567890"},
		"placeholder + suffix":  {`{"v":"x","api_key":"{{secret:k}}RealTrailingSecret99"}`, "RealTrailingSecret99"},
		"unknown brace content": {`{"api_key":"{{whatever}}"}`, "whatever"},
	} {
		if out := redactJSON(t, tc.in); strings.Contains(out, tc.leaked) {
			t.Errorf("%s: kept %q: %s", name, tc.leaked, out)
		}
	}
	for _, ok := range []string{`{{secret:k}}`, `{{ secret:my-key }}`, `@secret:outlook`, `{{vault:a}}`, `{{$json.x}}`, `{{ $node.Foo }}`, `REPLACE_ME`} {
		if out := redactJSON(t, `{"api_key":"`+ok+`"}`); !strings.Contains(out, ok) {
			t.Errorf("placeholder %q was redacted: %s", ok, out)
		}
	}
}

func TestRedactResultMoreShapes(t *testing.T) {
	for name, tc := range map[string]struct{ in, leaked string }{
		"stripe live":    {`{"t":"key ` + "s" + `k_live_abcdefghijklmnop1234 ok"}`, "abcdefghijklmnop1234"},
		"stripe test":    {`{"t":"` + "s" + `k_test_abcdefghijklmnop1234"}`, "abcdefghijklmnop1234"},
		"google key":     {`{"t":"` + "A" + `Iza` + strings.Repeat("a1B2", 9) + `"}`, "a1B2a1B2a1B2"},
		"url password":   {`{"u":"postgres://admin:Sup3rS3cret@db.example.com:5432/x"}`, "Sup3rS3cret"},
		"basic header":   {`{"h":"Authorization: Basic dXNlcjpwYXNzd29yZA=="}`, "dXNlcjpwYXNzd29yZA"},
		"basic as value": {`{"Authorization":"Basic dXNlcjpwYXNzd29yZA=="}`, "dXNlcjpwYXNzd29yZA"},
		"secret as key":  {`{"` + "s" + `k-live-abcdefghijklmnop0123":"x"}`, "abcdefghijklmnop0123"},
		"private key":    {`{"k":"-----BEGIN RSA PRIVATE KEY-----\nMIIE"}`, "MIIE"},
	} {
		out := redactJSON(t, tc.in)
		if strings.Contains(out, tc.leaked) {
			t.Errorf("%s: leaked %q: %s", name, tc.leaked, out)
		}
	}
	// The user and host of a URL stay.
	if out := redactJSON(t, `{"u":"postgres://admin:Sup3rS3cret@db.example.com/x"}`); !strings.Contains(out, "postgres://admin:[redacted]@db.example.com/x") {
		t.Errorf("url = %s", out)
	}
}

// What is not a secret keeps its value and its type.
func TestRedactResultKeepsNumbersBooleansHashesAndLookalikes(t *testing.T) {
	in := `{"token_limit":100,"max_tokens":512,"token_count":"42","tokens_used":7,"secret_name":"db","token_type":"bearer","password_required":true,` +
		`"footprint":"abc12345","otp_enabled":false,"cvv":null,"tokenizer":"gpt","passport_count":3,` +
		`"commit":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","id":"0123456789abcdef0123456789abcdef0123456789abcdef",` +
		`"note":"password: required, and the token bucket is full"}`
	var want any
	_ = json.Unmarshal([]byte(in), &want)
	wantB, _ := json.Marshal(want)
	if got := redactJSON(t, in); got != string(wantB) {
		t.Errorf("valid content changed:\n got %s\nwant %s", got, wantB)
	}
	// ...but a word-boundary secret key with a string value still goes.
	for _, k := range []string{"password", "apiKey", "api_key", "access-key", "otp", "privateKey", "passportNumber", "social_security_number", "card_no", "IBAN"} {
		if out := redactJSON(t, `{"`+k+`":"Zx9-realvalue"}`); strings.Contains(out, "realvalue") {
			t.Errorf("key %q kept its value: %s", k, out)
		}
	}
}

// Redaction runs after the schema check: it must never make a valid result of
// any registered kind invalid.
func TestRedactResultKeepsEveryKindsValidResultValid(t *testing.T) {
	secretish := "key " + fakeSK + " and Bearer " + fakeBearer + " at https://u:pw12345678@h.test/?token=abcdefghijkl"
	samples := map[string]string{
		"generic":     `{"summary":"` + secretish + `","output":{"api_key":"{{secret:k}}","token_limit":5,"password":"Zx9-realvalue"}}`,
		"draft-reply": `{"summary":"s","draft":{"to":"sam","subject":"Re","body":"` + secretish + `"},"assumptions":["` + secretish + `"]}`,
		// The kinds of internal/aitaskkinds register in this test binary through
		// redact_kinds_test.go (an external test package: aitaskkinds imports this one).
		"workflow-generate": `{"summary":"` + secretish + `","workflow":{"name":"Digest","description":"d","is_active":false,` +
			`"nodes":[{"id":"t","type":"trigger.manual","name":"Start","config":{}},` +
			`{"id":"h","type":"http.request","name":"Fetch","config":{"url":"https://example.test/feed","method":"GET","credential_id":"REPLACE_ME","auth_token":"{{secret:feed_token}}"}}],` +
			`"connections":[{"source":"t","target":"h","source_handle":"main","target_handle":"main"}]},` +
			`"validation":{"valid":true,"errors":[],"warnings":["` + secretish + `"]}}`,
		"recording-analyze": `{"recordingId":"rec-20261013-101010-abcd","summary":"` + secretish + `","suggestions":["automate the export","` + secretish + `"],"risks":["a step changes the site"]}`,
		"failed-run-fix": `{"summary":"s","diagnosis":"` + secretish + `","workflowId":"wf-1","executionId":"ex-1",` +
			`"fix":{"kind":"workflow-edit","description":"` + secretish + `","edits":[{"node":"h","config":{"url":"https://example.test/feed","headers":{"Accept":"application/json"}}}]}}`,
		"extension-fill": `{"summary":"Filled 4 fields; left the VAT number empty.","stopped_before_submit":true,"fields":[` +
			`{"label":"Full name","value":"Ada Lovelace","source":"profile: name","generated":false},` +
			`{"label":"Email","value":"ada@example.com","source":"profile: email","generated":false},` +
			`{"label":"Phone","value":"+44 20 7946 0958","source":"profile: phone","generated":false},` +
			`{"label":"Street address","value":"12 St James's Square, London SW1Y 4JH","source":"profile: address","generated":false},` +
			`{"label":"Date of birth","value":"1815-12-10","source":"profile: dob","generated":false},` +
			`{"label":"Why us","value":"I enjoy compilers and your team ships them.","source":"generated","generated":true}]}`,
		"extension-ask": `{"summary":"The Pay button is disabled.","answer":"Tick the terms box first; contact ada@example.com or call +44 20 7946 0958 if it stays disabled.","suggested_actions":["Tick the terms box.","Reload the page."]}`,
	}
	for _, name := range Names() {
		raw, ok := samples[name]
		if !ok {
			if name == "application-apply" {
				continue // registered by internal/applyfill, which imports this package
			}
			t.Errorf("kind %s has no sample result here: add one", name)
			continue
		}
		k, _ := Lookup(name)
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		schema := k.Schema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		if err := Validate(schema, v); err != nil {
			t.Fatalf("%s: the sample is not valid: %v", name, err)
		}
		red := RedactResult(v)
		b, _ := json.Marshal(red)
		var back any
		_ = json.Unmarshal(b, &back)
		if err := Validate(schema, back); err != nil {
			t.Errorf("%s: redaction made a valid result invalid: %v\n%s", name, err, b)
		}
		if strings.Contains(string(b), "abcdefghijklmnop0123") || strings.Contains(string(b), "pw12345678") {
			t.Errorf("%s: a secret survived: %s", name, b)
		}
	}
}

// ---- round 3 ----

// A secret placed where a placeholder's NAME goes is not a placeholder.
func TestRedactResultPlaceholderNameMustBeAName(t *testing.T) {
	for name, in := range map[string]string{
		"token as name":    `{"v":"{{secret:` + fakeGH + `}}"}`,
		"mixed alnum name": `{"password":"{{secret:Hunter2Hunter2xx}}"}`,
		"digit run name":   `{"api_key":"{{secret:abc1234567890}}"}`,
		"at-secret secret": `{"api_key":"@secret:` + fakeSK + `"}`,
		"vault token name": `{"api_key":"{{vault:` + fakeGH + `}}"}`,
	} {
		out := redactJSON(t, in)
		for _, leaked := range []string{"abcdefghijklmnop", "Hunter2Hunter2xx", "1234567890"} {
			if strings.Contains(out, leaked) {
				t.Errorf("%s: kept %q: %s", name, leaked, out)
			}
		}
	}
	for _, ok := range []string{`{{secret:openai_key}}`, `{{ vault:Gmail-app }}`, `@secret:slack-token`, `{{secret:SLACK_TOKEN_2}}`, `@secret:outlook`} {
		if out := redactJSON(t, `{"api_key":"`+ok+`"}`); !strings.Contains(out, ok) {
			t.Errorf("placeholder %q was redacted: %s", ok, out)
		}
	}
	// a long @secret: name inside a header value stays too
	if out := redactJSON(t, `{"h":"Bearer @secret:a-rather-long-secret-name"}`); !strings.Contains(out, "@secret:a-rather-long-secret-name") {
		t.Errorf("header placeholder redacted: %s", out)
	}
}

func TestRedactResultRound3Shapes(t *testing.T) {
	zw := "​"
	for name, tc := range map[string]struct{ in, leaked string }{
		"zero-width in a key": {`{"t":"` + fakeSK[:6] + zw + fakeSK[6:] + `"}`, "abcdefghijklmnop0123"},
		"zero-width in label": {`{"t":"pass` + zw + `word: Hunter2!xyz99"}`, "Hunter2!xyz99"},
		"quoted json label":   {`{"t":"{\"password\":\"Hunter2!xyz99\"}"}`, "Hunter2!xyz99"},
		"quoted api_key":      {`{"t":"\"api_key\": \"abcdef1234567890\""}`, "abcdef1234567890"},
		"cookie header":       {`{"t":"Cookie: sid=abc123def456; theme=dark"}`, "abc123def456"},
		"set-cookie header":   {`{"t":"Set-Cookie: session=abc123def456; HttpOnly"}`, "abc123def456"},
		"x-api-key header":    {`{"t":"X-Api-Key: abcdef1234567890"}`, "abcdef1234567890"},
		"authorization token": {`{"t":"Authorization: Token abcdef1234567890"}`, "abcdef1234567890"},
		"gitlab":              {`{"t":"` + "g" + `lpat-abcdefghijklmnopqrstu"}`, "abcdefghijklmnopqrstu"},
		"npm":                 {`{"t":"` + "n" + `pm_` + strings.Repeat("aB3", 12) + `"}`, "aB3aB3aB3aB3"},
		"restricted stripe":   {`{"t":"` + "r" + `k_live_abcdefghijklmnop1234"}`, "abcdefghijklmnop1234"},
		"pwd key":             {`{"pwd":"Zx9-realvalue"}`, "realvalue"},
		"pw key":              {`{"pw":"Zx9-realvalue"}`, "realvalue"},
	} {
		if out := redactJSON(t, tc.in); strings.Contains(out, tc.leaked) {
			t.Errorf("%s: leaked %q: %s", name, tc.leaked, out)
		}
	}
	// lookalikes stay
	in := `{"footprint":"abc12345","pwned_count":"12","note":"Cookie: chocolate chip is the flavour","pw_len":"8"}`
	var want any
	_ = json.Unmarshal([]byte(in), &want)
	wantB, _ := json.Marshal(want)
	if got := redactJSON(t, in); got != string(wantB) {
		t.Errorf("lookalikes changed:\n got %s\nwant %s", got, wantB)
	}
}

// Linear work: a few MiB of hostile input finishes in a moment.
func TestRedactResultWorkIsLinear(t *testing.T) {
	limit := 2 * time.Second
	if raceEnabled {
		limit = 60 * time.Second
	}
	cases := map[string]any{
		"4 MiB of labels": strings.Repeat("password=Abcdefg1 ", (4<<20)/18),
		"4 MiB of braces": strings.Repeat("{{ x }} token=Abcdefg12345 ", (4<<20)/27),
	}
	for name, in := range cases {
		start := time.Now()
		out := RedactResult(map[string]any{"a": in})
		if d := time.Since(start); d > limit {
			t.Errorf("%s: took %v", name, d)
		}
		if s, _ := out.(map[string]any)["a"].(string); strings.Contains(s, "Abcdefg1") {
			t.Errorf("%s: a secret survived", name)
		}
	}
	// Many keys that all redact to the same name.
	keys := map[string]any{}
	for i := 0; i < 100000; i++ {
		keys[fmt.Sprintf("%s%07d", "s"+"k-live-abcdefghijkl", i)] = i
	}
	start := time.Now()
	out := RedactResult(keys).(map[string]any)
	if d := time.Since(start); d > limit {
		t.Errorf("100k colliding keys took %v", d)
	}
	if len(out) != len(keys) {
		t.Errorf("keys lost: %d of %d", len(out), len(keys))
	}
}

func TestScrubJSONCatchesEscapedSecretsInValuesAndKeys(t *testing.T) {
	tok := "mat-" + strings.Repeat("Ab1_-", 8)
	esc := `\u006d` + tok[1:] // "m" written as an escape: invisible to a replacement on the text
	raw := []byte(`{"` + esc + `":1,"n":12345678901234567890,"a":["x ` + esc + `",{"deep":"` + tok + `"}],"s":"` + esc + `"}`)
	out := string(ScrubJSON(raw, RedactAgentTokens))
	if strings.Contains(out, tok) || strings.Contains(out, "Ab1_-Ab1_-") || strings.Contains(out, `\u006d`) {
		t.Fatalf("not scrubbed: %s", out)
	}
	if !strings.Contains(out, "12345678901234567890") || !json.Valid([]byte(out)) {
		t.Fatalf("document changed: %s", out)
	}
	if got := string(ScrubJSON([]byte("not json "+tok), RedactAgentTokens)); strings.Contains(got, tok) {
		t.Fatalf("text: %s", got)
	}
}
