package novelty

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func TestNormaliseEquivalences(t *testing.T) {
	base, err := Normalise("Fix the login bug")
	if err != nil {
		t.Fatal(err)
	}
	same := []string{
		"fix login bug", "FIX   THE\tLOGIN\nBUG", "Fix, the login-bug!", "bug login fix",
		"ｆｉｘ ｌｏｇｉｎ ｂｕｇ",          // full-width
		"Fix the login bug bug",  // repeated word
		"fix a login bug please", // stopwords only differ
	}
	for _, s := range same {
		if got, _ := Normalise(s); got != base {
			t.Errorf("%q -> %q, want %q", s, got, base)
		}
	}
	if got, _ := Normalise("fix the logout bug"); got == base {
		t.Error("different words must give a different key")
	}
}

func TestNormaliseAccents(t *testing.T) {
	a, _ := Normalise("café menu")
	b, _ := Normalise("café MENU")
	c, _ := Normalise("cafe menu")
	if a != b || b != c {
		t.Errorf("composed %q decomposed %q plain %q", a, b, c)
	}
}

func TestNormaliseErrors(t *testing.T) {
	if _, err := Normalise(strings.Repeat("a", MaxInputBytes+1)); err != ErrTooLong {
		t.Errorf("over cap: %v", err)
	}
	if _, err := Normalise(strings.Repeat("a", MaxInputBytes)); err != nil {
		t.Errorf("at cap must pass: %v", err)
	}
	for _, s := range []string{"", "   ", "the a an", "!!! ???", "́"} {
		if _, err := Normalise(s); err != ErrEmptyKey {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestNormaliseBoundsOutput(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < MaxInputBytes-8; i++ {
		b.WriteString("w")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26)) + " ")
	}
	got, err := Normalise(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(got)); n > MaxTokens {
		t.Errorf("%d tokens", n)
	}
	long, _ := Normalise(strings.Repeat("z", MaxTokenRunes*3))
	if utf8.RuneCountInString(long) != MaxTokenRunes {
		t.Errorf("token not capped: %d", utf8.RuneCountInString(long))
	}
}

func TestKeyDeterministicAndDomainHash(t *testing.T) {
	n1, h1, _ := Key("Ship the importer")
	n2, h2, _ := Key("importer ship")
	if n1 != n2 || h1 != h2 || h1 != types.KeyHash(n1) {
		t.Fatal("key not stable")
	}
}

// Property: shuffling words, changing case, padding whitespace and sprinkling stopwords and punctuation never changes the key.
func TestNormaliseProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	vocab := []string{"import", "ledger", "rollback", "Ünïcode", "查询", "cache", "retry", "schema", "n42"}
	noise := []string{"the", "a", "of", ",", "-", "!!", "  ", "\t", "and"}
	for i := 0; i < 500; i++ {
		words := rng.Perm(len(vocab))[:2+rng.Intn(5)]
		var a, b []string
		for _, w := range words {
			a = append(a, vocab[w])
		}
		for _, j := range rng.Perm(len(a)) {
			w := a[j]
			if rng.Intn(2) == 0 {
				w = strings.ToUpper(w)
			}
			b = append(b, w, noise[rng.Intn(len(noise))])
		}
		ka, _ := Normalise(strings.Join(a, " "))
		kb, _ := Normalise(strings.Join(b, " "))
		if ka != kb {
			t.Fatalf("iter %d: %q vs %q (%q / %q)", i, ka, kb, a, b)
		}
	}
}

// Property: the result is idempotent, sorted, de-duplicated and free of upper case, for any bytes.
func FuzzNormalise(f *testing.F) {
	for _, s := range []string{"", "Fix the bug", "\xff\xfe", "ｆｉｘ", "á", strings.Repeat("é", 100), "a b a B"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := Normalise(s)
		if err != nil {
			return
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid utf8 %q", got)
		}
		if again, err := Normalise(got); err != nil || again != got {
			t.Fatalf("not idempotent: %q -> %q (%v)", got, again, err)
		}
		toks := strings.Fields(got)
		if len(toks) == 0 || len(toks) > MaxTokens {
			t.Fatalf("token count %d", len(toks))
		}
		for i := 1; i < len(toks); i++ {
			if toks[i-1] >= toks[i] {
				t.Fatalf("not strictly sorted: %q", got)
			}
		}
		if got != strings.ToLower(got) {
			t.Fatalf("upper case left: %q", got)
		}
	})
}
