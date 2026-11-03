package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCanonHostTable(t *testing.T) { // T19
	for in, want := range map[string]string{
		"Example.COM":           "example.com",
		"example.com.":          "example.com",
		"BÜCHER.example":        "xn--bcher-kva.example",
		"xn--bcher-kva.example": "xn--bcher-kva.example",
		"ｅｘａｍｐｌｅ.com":           "example.com", // compatibility forms
	} {
		if got, err := CanonHost(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "a@b.com", "user:pw@example.com", "example.com:443", "example.com/x", "a b.com", "a\x00.com", "127.0.0.1", "::1", "[::1]", "a..com", "..", "-bad-.com", "exa%6dple.com"} {
		if got, err := CanonHost(in); err == nil {
			t.Fatalf("%q was accepted as %q", in, got)
		}
	}
}

func TestHostAllowedExactSuffixAndSharedHosts(t *testing.T) {
	cases := []struct {
		allowed []string
		host    string
		want    bool
	}{
		{[]string{"example.com"}, "example.com", true},
		{[]string{"example.com"}, "EXAMPLE.com.", true},
		{[]string{"example.com"}, "api.example.com", true},
		{[]string{"example.com"}, "evil-example.com", false}, // not on a label boundary
		{[]string{"example.com"}, "example.com.evil.com", false},
		{[]string{"example.com"}, "com", false},
		{[]string{"api.example.com"}, "example.com", false},
		{[]string{"github.io"}, "evil.github.io", false}, // a public suffix on the private list: exact only
		{[]string{"github.io"}, "github.io", true},
		{[]string{"alice.github.io"}, "x.alice.github.io", true},
		{[]string{"alice.github.io"}, "bob.github.io", false},
		{[]string{"co.uk"}, "evil.co.uk", false},
		{[]string{"host.invalidtld"}, "x.host.invalidtld", false}, // a name the list does not know: exact only
		{[]string{"host.invalidtld"}, "host.invalidtld", true},
		{[]string{Any}, "anything.example", true},
		{nil, "example.com", false},
		{[]string{"example.com"}, "127.0.0.1", false},
	}
	for _, c := range cases {
		if got := HostAllowed(c.allowed, c.host); got != c.want {
			t.Fatalf("%v / %q: %v", c.allowed, c.host, got)
		}
	}
}

func TestParseInt64IsPlainIntegersOnly(t *testing.T) { // T4, T19
	for in, want := range map[string]int64{"0": 0, "5": 5, "-5": -5, "9223372036854775807": 1<<63 - 1} {
		if got, err := ParseInt64(in); err != nil || got != want {
			t.Fatalf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "1.5", "1e3", "NaN", "+Inf", "-Inf", "+5", "05", " 5", "5 ", "0x10", "9223372036854775808", "1_000", "٣"} {
		if _, err := ParseInt64(in); !errors.Is(err, ErrBadInteger) {
			t.Fatalf("%q: %v", in, err)
		}
	}
}

func TestCanonRelPath(t *testing.T) {
	if got, err := CanonRelPath("a//b/./c/"); err != nil || got != "a/b/c" {
		t.Fatalf("%q %v", got, err)
	}
	// NFC: e + combining acute equals the precomposed form
	a, _ := CanonRelPath("café/x")
	b, _ := CanonRelPath("café/x")
	if a != b || a == "" {
		t.Fatalf("%q != %q", a, b)
	}
	for _, in := range []string{"", ".", "..", "../x", "a/../../x", "/abs", "a\x00", `a\b`} {
		if got, err := CanonRelPath(in); err == nil {
			t.Fatalf("%q accepted as %q", in, got)
		}
	}
}

func TestCanonHandlePerPlatformAndNoRuleStaysOff(t *testing.T) {
	for _, c := range [][3]string{
		{"x", "@Alice_9", "alice_9"}, {"x", "alice_9", "alice_9"},
		{"instagram", "@A.b_c", "a.b_c"},
		{"email", "Bob@EXAMPLE.com", "bob@example.com"}, {"email", "bob@Bücher.example", "bob@xn--bcher-kva.example"},
	} {
		if got, err := CanonHandle(c[0], c[1]); err != nil || got != c[2] {
			t.Fatalf("%v: %q %v", c, got, err)
		}
	}
	for _, c := range [][2]string{{"x", "toolonghandle_123456"}, {"x", "a b"}, {"x", ""}, {"instagram", ".a"}, {"email", "a@b@c"}, {"email", "nodomain"}, {"email", "a@127.0.0.1"}} {
		if got, err := CanonHandle(c[0], c[1]); err == nil {
			t.Fatalf("%v accepted as %q", c, got)
		}
	}
	if _, err := CanonHandle("linkedin", "someone"); !errors.Is(err, ErrNoPlatformRule) || HasPlatformRule("linkedin") {
		t.Fatalf("a platform with no rule: %v", err)
	}
	c := Constraints{SendThirdParty: &SendThirdParty{Platform: "linkedin", Account: "a", Destinations: []string{Any}, MaxPerRun: 1, ContentClass: "text"}}
	if err := c.Validate(bounds); !errors.Is(err, ErrInvalidLimits) || !strings.Contains(err.Error(), "no handle rule") {
		t.Fatalf("a charter for a platform without a rule: %v", err)
	}
}

func vaultJSON(over map[string]string) []byte {
	f := map[string]string{"bucket_size": "1024", "bucket_count": "4", "max_call_ms": "5000", "slot_ms": "500", "max_attempts": "10"}
	for k, v := range over {
		f[k] = v
	}
	var parts []string
	for k, v := range f {
		parts = append(parts, fmt.Sprintf("%q:%s", k, v))
	}
	parts = append(parts, `"secrets":["S1"]`, `"hosts":["example.com"]`)
	return []byte("{" + strings.Join(parts, ",") + "}")
}

func TestVaultUseEntryValidationNamesTheFieldAndStoresNothing(t *testing.T) { // T5f8, T14
	if v, err := ParseVaultUse(vaultJSON(nil)); err != nil || v.Slots() != 10 || v.Cap() != 4096 {
		t.Fatalf("%+v %v", v, err)
	}
	bad := map[string][]string{
		"bucket_size":  {"0", "-1", "1099511627776", "1.5", "NaN", "+Inf", `"5"`, "1e3", "99999999999999999999"},
		"bucket_count": {"0", "-1", "17", "2.5", "NaN", "+Inf"},
		"max_call_ms":  {"0", "999", "60001", "-1", "1.5", "NaN", "+Inf"},
		"slot_ms":      {"0", "99", "-1", "1.5", "NaN", "+Inf", "6000"}, // 6000 > max_call_ms 5000
		"max_attempts": {"0", "-1", "1001", "1.5", "NaN", "+Inf"},
	}
	for field, vals := range bad {
		for _, v := range vals {
			_, err := ParseVaultUse(vaultJSON(map[string]string{field: v}))
			if !errors.Is(err, ErrInvalidLimits) || (!strings.Contains(fmt.Sprint(err), field) && !strings.Contains(fmt.Sprint(err), "does not parse")) {
				t.Fatalf("%s=%s: %v", field, v, err)
			}
		}
	}
	// numeric overflow before any conversion: the product would wrap, the fields are refused one by one
	if _, err := ParseVaultUse(vaultJSON(map[string]string{"bucket_size": "1099511627776", "bucket_count": "1099511627776"})); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("overflow: %v", err)
	}
	// 60000 ms at 100 ms is 600 slots: over 64
	if _, err := ParseVaultUse(vaultJSON(map[string]string{"max_call_ms": "60000", "slot_ms": "100"})); !errors.Is(err, ErrInvalidLimits) || !strings.Contains(err.Error(), "slots") {
		t.Fatalf("slots: %v", err)
	}
	if _, err := ParseVaultUse(vaultJSON(map[string]string{"max_call_ms": "6400", "slot_ms": "100"})); err != nil {
		t.Fatalf("exactly 64 slots: %v", err)
	}
	if _, err := ParseVaultUse([]byte(`{"bucket_size":1}`)); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("missing fields: %v", err)
	}
	if v := (VaultUse{Secrets: []string{"S"}, Hosts: []string{"a b"}, MaxAttempts: 1, BucketSize: 1, BucketCount: 1, MaxCallMS: 1000, SlotMS: 1000}); v.Validate() == nil {
		t.Fatal("a bad host in the charter")
	}
}

func TestTheEntryDialogSpellsOutEveryExplicitAny(t *testing.T) { // T24
	c := typedCharter()
	c.SendThirdParty.Destinations = []string{Any}
	c.VaultUse.Hosts = []string{Any}
	c.HostSetting.Keys["motd"] = []string{Any}
	text := strings.Join(c.Describe(), "\n")
	for _, w := range []string{"ANY DESTINATION", "ANY HOST", "ANY VALUE"} {
		if !strings.Contains(text, w) {
			t.Fatalf("%q is not in:\n%s", w, text)
		}
	}
	if strings.Contains(strings.ReplaceAll(text, "*", ""), " * ") || strings.Contains(text, "*") {
		t.Fatalf("a bare star is shown:\n%s", text)
	}
	if got := (Constraints{}).Describe(); len(got) != 0 {
		t.Fatalf("a charter with nothing enabled describes %v", got)
	}
}
