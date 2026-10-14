package guardrails

import (
	"reflect"
	"strings"
	"testing"
)

func kinds(fs []finding) map[string][]string {
	out := map[string][]string{}
	for _, f := range fs {
		out[f.kind] = append(out[f.kind], f.text)
	}
	return out
}

func TestPIIPositives(t *testing.T) {
	cases := map[string]string{
		"EMAIL_ADDRESS":             "write to jane.doe+news@mail.example.co.uk today",
		"CREDIT_CARD":               "card 4111 1111 1111 1111 exp 12/29",
		"IBAN_CODE":                 "pay to DE89 3704 0044 0532 0130 00 please",
		"IP_ADDRESS":                "server at 192.168.10.254 is down",
		"US_SSN":                    "ssn 123-45-6789",
		"PHONE_NUMBER":              "call +1 415-555-2671 now",
		"UK_NHS":                    "NHS number 943 476 5919",
		"ES_NIF":                    "DNI 12345678Z",
		"ES_NIE":                    "NIE X1234567L",
		"FI_PERSONAL_IDENTITY_CODE": "hetu 131052-308T",
		"PL_PESEL":                  "pesel 44051401359",
		"AU_ABN":                    "ABN 51 824 753 556",
		"DATE_TIME":                 "born 1990-04-12",
		"CRYPTO":                    "send to 0x52908400098527886E0F7030069857D2E4169EE7",
		"LOCATION":                  "lives at 221B Baker Street",
	}
	for entity, text := range cases {
		got := kinds(detectPII(text, []string{entity}))
		if len(got[entity]) != 1 {
			t.Errorf("%s: %q found %v", entity, text, got)
		}
	}
	// IPv6 too.
	if got := kinds(detectPII("host 2001:db8::8a2e:370:7334 up", []string{"IP_ADDRESS"})); len(got["IP_ADDRESS"]) != 1 {
		t.Errorf("ipv6: %v", got)
	}
}

func TestPIINegatives(t *testing.T) {
	cases := map[string]string{
		"CREDIT_CARD":   "order 4111 1111 1111 1112 (fails Luhn)",
		"IBAN_CODE":     "DE89 3704 0044 0532 0130 01 (bad check digits)",
		"IP_ADDRESS":    "version 999.1.2.3 and time 12:30:45",
		"US_SSN":        "000-12-3456 and 666-12-3456",
		"EMAIL_ADDRESS": "no address @ here",
		"PHONE_NUMBER":  "room 12 34",
		"ES_NIF":        "12345678A",
		"UK_NHS":        "943 476 5918",
	}
	for entity, text := range cases {
		if got := detectPII(text, []string{entity}); len(got) != 0 {
			t.Errorf("%s: %q found %v", entity, text, got)
		}
	}
	if got := detectPII("The quick brown fox jumps over the lazy dog.", nil); len(got) != 0 {
		t.Errorf("plain sentence: %v", got)
	}
}

// With every entity, a span is reported once, by the most specific type.
func TestPIIAllEntitiesNoDoubleCount(t *testing.T) {
	got := kinds(detectPII("card 4111111111111111, mail a@b.io, iban DE89370400440532013000", nil))
	want := map[string][]string{
		"CREDIT_CARD":   {"4111111111111111"},
		"EMAIL_ADDRESS": {"a@b.io"},
		"IBAN_CODE":     {"DE89370400440532013000"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestKeywords(t *testing.T) {
	got := matchKeywords("The Password is here; passwords too. C++ rocks! Ünïcode wörd", []string{"password.", "c++", "wörd", "ünï", ""})
	want := []string{"Password", "C++", "wörd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := matchKeywords("nothing to see", []string{"secret"}); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	// Repeats are reported once, case-insensitively.
	if got := matchKeywords("Bomb bomb BOMB", []string{"bomb"}); !reflect.DeepEqual(got, []string{"Bomb"}) {
		t.Errorf("got %v", got)
	}
}

func TestSecretsVendorAndEntropy(t *testing.T) {
	aws := "AKIA" + strings.Repeat("ABCDEFGHIJ", 1) + "KLMNOP"
	gh := "ghp_" + "aB3dE5gH7jK9mN1pQ3sT5vW7yZ9bC1dE3fG5"
	text := "use " + aws + " and " + gh + " but not report.pdf or https://example.com/docs or 123e4567-e89b-12d3-a456-426614174000"
	got := detectSecrets(text, "balanced")
	if !reflect.DeepEqual(got, []string{aws, gh}) {
		t.Errorf("balanced: %v", got)
	}
	random := "Zx9!qL2@vB7#nM4$kP8%"
	if got := detectSecrets("the value "+random+" is it", "balanced"); !reflect.DeepEqual(got, []string{random}) {
		t.Errorf("entropy balanced: %v", got)
	}
	if got := detectSecrets("the value "+random+" is it", "permissive"); len(got) != 0 {
		t.Errorf("entropy permissive (too short): %v", got)
	}
	if got := detectSecrets("hello world, this is a normal sentence with words.", "strict"); len(got) != 0 {
		t.Errorf("plain text strict: %v", got)
	}
	if got := detectSecrets("db password=hunter2pass ok", "balanced"); !reflect.DeepEqual(got, []string{"hunter2pass"}) {
		t.Errorf("assignment: %v", got)
	}
	// strict flags a hex digest balanced lets through (two character classes).
	digest := "9f86d081884c7d659a2feaa0c55ad015"
	if got := detectSecrets(digest, "strict"); len(got) != 1 {
		t.Errorf("strict digest: %v", got)
	}
	if got := detectSecrets(digest, "balanced"); len(got) != 0 {
		t.Errorf("balanced digest: %v", got)
	}
}

func TestURLs(t *testing.T) {
	rules := urlRules{allowed: []string{"example.com", "https://docs.acme.io/public", "10.0.0.0/8"},
		schemes: []string{"https", "mailto"}, blockUserinfo: true, allowSubdomains: true}
	text := "see https://api.example.com/x, https://docs.acme.io/public/a and https://docs.acme.io/private " +
		"http://example.com plus evil.org/path, https://user:pw@example.com, https://10.1.2.3/ok, mailto:a@b.c and report.pdf, mail me at bob@corp.com."
	detected := detectURLs(text)
	blocked := map[string]string{}
	for _, u := range detected {
		if why := rules.checkURL(u); why != "" {
			blocked[u] = why
		}
	}
	want := map[string]string{
		"https://docs.acme.io/private": "Not in allow list",
		"http://example.com":           "Blocked scheme: http",
		"evil.org/path":                "Blocked scheme: http",
		"https://user:pw@example.com":  "Contains userinfo (potential credential injection)",
	}
	if !reflect.DeepEqual(blocked, want) {
		t.Errorf("blocked %v\nwant %v\ndetected %v", blocked, want, detected)
	}
	for _, u := range detected {
		if strings.Contains(u, "report.pdf") || strings.Contains(u, "corp.com") {
			t.Errorf("detected %q", u)
		}
	}
	noSub := urlRules{allowed: []string{"example.com"}, schemes: []string{"https"}}
	if why := noSub.checkURL("https://api.example.com"); why != "Not in allow list" {
		t.Errorf("subdomain without allow_subdomains: %q", why)
	}
	if why := (urlRules{schemes: []string{"https"}}).checkURL("https://example.com"); why == "" {
		t.Error("empty allow list allowed a URL")
	}
}

func TestCustomRegexAndMasks(t *testing.T) {
	re, err := compileRegex("/ord-\\d{3}/gi")
	if err != nil {
		t.Fatal(err)
	}
	fs := detectRegexes("orders ORD-123 and ord-456", []namedRegex{{"ORDER", re}})
	if got := kinds(fs)["ORDER"]; !reflect.DeepEqual(got, []string{"ORD-123", "ord-456"}) {
		t.Errorf("got %v", got)
	}
	if _, err := compileRegex("/(?<=x)y/"); err == nil {
		t.Error("lookbehind compiled")
	}
	got := applyMasks("a@b.io wrote to a@b.io.uk and a@b.io", map[string][]string{
		"EMAIL_ADDRESS": {"a@b.io", "a@b.io.uk"}})
	if got != "<EMAIL_ADDRESS> wrote to <EMAIL_ADDRESS> and <EMAIL_ADDRESS>" {
		t.Errorf("masked %q", got)
	}
}
