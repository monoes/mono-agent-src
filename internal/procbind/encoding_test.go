package procbind

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeLengthDelimitsEveryField(t *testing.T) {
	a := Encode("d", 1, "ab", "c")
	b := Encode("d", 1, "a", "bc")
	if bytes.Equal(a, b) {
		t.Fatal("moving a byte between adjacent fields must change the encoding")
	}
	if bytes.Equal(Encode("d", 1, "x", ""), Encode("d", 1, "", "x")) {
		t.Fatal("an empty field must keep its position")
	}
	if bytes.Equal(Encode("d", 1, "a"), Encode("d", 1, "a", "")) {
		t.Fatal("a trailing empty field is a different message")
	}
	if bytes.Equal(Encode("d", 1, "a"), Encode("d", 2, "a")) || bytes.Equal(Encode("d", 1, "a"), Encode("e", 1, "a")) {
		t.Fatal("domain and version are part of the encoding")
	}
	if !bytes.Equal(Encode("d", 1, "a", "b"), Encode("d", 1, "a", "b")) {
		t.Fatal("encoding must be deterministic")
	}
}

func TestEncodeLayout(t *testing.T) {
	got := Encode("ab", 7, "c")
	want := []byte{
		0, 0, 0, 0, 0, 0, 0, 2, 'a', 'b', // domain
		0, 0, 0, 0, 0, 0, 0, 1, 7, // version
		0, 0, 0, 0, 0, 0, 0, 1, 'c',
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("layout:\n got %v\nwant %v", got, want)
	}
}

// Any two distinct (domain, version, fields) tuples must encode differently: shift bytes across
// every field boundary of a small alphabet.
func FuzzEncodeInjective(f *testing.F) {
	f.Add("ab", "c", "", "a", "bc", "")
	f.Add("", "", "x", "x", "", "")
	f.Fuzz(func(t *testing.T, a1, a2, a3, b1, b2, b3 string) {
		x, y := []string{a1, a2, a3}, []string{b1, b2, b3}
		same := x[0] == y[0] && x[1] == y[1] && x[2] == y[2]
		if bytes.Equal(Encode("d", 1, x...), Encode("d", 1, y...)) != same {
			t.Fatalf("injectivity broken for %q vs %q", x, y)
		}
	})
}

func nonce(b byte) []byte { return bytes.Repeat([]byte{b}, NonceLen) }

func goodAnswer() Answer {
	return Answer{NV: nonce(1), NS: nonce(2), Generation: "gen-1", TokenID: "tok-1", Profile: "p1", Scope: "session:7", Verdict: VerdictAllow}
}

var testKey = bytes.Repeat([]byte{9}, 32)

func TestAnswerMACRoundTrip(t *testing.T) {
	a := goodAnswer()
	mac, err := a.MAC(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(testKey, mac); err != nil {
		t.Fatalf("a genuine answer must verify: %v", err)
	}
}

// Every field is bound: changing any one, or moving a byte across a boundary, fails.
func TestAnswerMACBindsEveryField(t *testing.T) {
	base := goodAnswer()
	mac, _ := base.MAC(testKey)
	cases := map[string]func(a *Answer){
		"verifier nonce":  func(a *Answer) { a.NV = nonce(3) },
		"service nonce":   func(a *Answer) { a.NS = nonce(3) },
		"generation":      func(a *Answer) { a.Generation = "gen-2" },
		"token id":        func(a *Answer) { a.TokenID = "tok-2" },
		"profile":         func(a *Answer) { a.Profile = "p2" },
		"scope":           func(a *Answer) { a.Scope = "session:8" },
		"verdict":         func(a *Answer) { a.Verdict = VerdictDeny },
		"profile<->scope": func(a *Answer) { a.Profile, a.Scope = a.Scope, a.Profile },
		"boundary shift":  func(a *Answer) { a.Profile, a.Scope = "p1s", "ession:7" },
	}
	for name, mutate := range cases {
		a := base
		mutate(&a)
		if err := a.Verify(testKey, mac); err == nil {
			t.Errorf("%s: a changed answer verified under the original MAC", name)
		}
	}
}

// A captured genuine transcript cannot answer a request that carries a fresh verifier nonce.
func TestAnswerReplayWithAFreshVerifierNonceFails(t *testing.T) {
	old := goodAnswer()
	mac, _ := old.MAC(testKey)
	fresh := old
	fresh.NV = nonce(0xAA)
	if err := fresh.Verify(testKey, mac); err == nil {
		t.Fatal("a replayed answer verified against a new verifier nonce")
	}
}

func TestAnswerMACKeyAndFormChecks(t *testing.T) {
	a := goodAnswer()
	mac, _ := a.MAC(testKey)
	if err := a.Verify(bytes.Repeat([]byte{8}, 32), mac); err == nil {
		t.Error("a wrong key verified")
	}
	if err := a.Verify(testKey, mac[:len(mac)-2]); err == nil {
		t.Error("a truncated MAC verified")
	}
	if err := a.Verify(testKey, strings.ToUpper(mac)+"00"); err == nil {
		t.Error("a malformed MAC verified")
	}
	if err := a.Verify(testKey, ""); err == nil {
		t.Error("an empty MAC verified")
	}
	if _, err := a.MAC(testKey[:16]); err == nil {
		t.Error("a short key was accepted")
	}
	bad := a
	bad.NV = a.NV[:31]
	if _, err := bad.MAC(testKey); err == nil {
		t.Error("a short verifier nonce was accepted")
	}
	bad = a
	bad.NS = append(append([]byte{}, a.NS...), 0)
	if _, err := bad.MAC(testKey); err == nil {
		t.Error("a long service nonce was accepted")
	}
	bad = a
	bad.Verdict = "maybe"
	if _, err := bad.MAC(testKey); err == nil {
		t.Error("an unknown verdict was accepted")
	}
	bad = a
	bad.TokenID, bad.Profile = "", ""
	if _, err := bad.MAC(testKey); err == nil {
		t.Error("an answer with no token id or profile was accepted")
	}
}

func TestNewNonce(t *testing.T) {
	a, err := NewNonce()
	if err != nil || len(a) != NonceLen {
		t.Fatalf("nonce: %v len %d", err, len(a))
	}
	b, _ := NewNonce()
	if bytes.Equal(a, b) {
		t.Fatal("two nonces were equal")
	}
}

// The domain tag keeps an answer MAC from validating as any other MAC of this package.
func TestAnswerDomainIsDistinct(t *testing.T) {
	a := goodAnswer()
	if !bytes.HasPrefix(a.encode(), Encode(answerDomain, answerVersion)) {
		t.Fatal("the answer encoding must start with its domain and version")
	}
	if answerDomain != "procbind/answer/v2" {
		t.Fatalf("domain changed: %q", answerDomain)
	}
}
