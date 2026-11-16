package sshhosts

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"
)

// Every shape below is a revoked-key line (or a lookalike) next to a valid
// line for the same key and host. The helper must refuse the file, so the key
// is never accepted through a callback that skipped the revocation.
func TestAmbiguousRevocationShapesAreRefused(t *testing.T) {
	k := testKey(t)
	valid := knownhosts.Line([]string{"box.test"}, k)
	kt, blob, _ := strings.Cut(strings.TrimPrefix(valid, "box.test "), " ")
	typeBlob := kt + " " + blob
	otherType := "ssh-rsa " + blob
	shapes := map[string]string{
		"marker then @host":        "@revoked @box.test " + typeBlob,
		"marker then @h.example":   "@revoked @h.example " + typeBlob,
		"mistyped marker":          "@revokd box.test " + typeBlob,
		"upper-case marker":        "@REVOKED box.test " + typeBlob,
		"double marker":            "@revoked @revoked box.test " + typeBlob,
		"cert marker then revoked": "@cert-authority @revoked box.test " + typeBlob,
		"NBSP separator":           "@revoked\xc2\xa0box.test " + typeBlob,
		"EM space separator":       "@revoked\xe2\x80\x83box.test " + typeBlob,
		"NEL separator":            "@revoked\u0085box.test " + typeBlob,
		"mid-line CR":              "@revoked\rbox.test " + typeBlob,
		"BOM":                      "\xef\xbb\xbf@revoked box.test " + typeBlob,
		"no host field":            "@revoked " + typeBlob,
		"key type mismatch":        "@revoked box.test " + otherType,
		"trailing base64 junk":     "@revoked box.test " + typeBlob + "AAAA!!",
		"truncated blob":           "@revoked box.test " + typeBlob[:len(typeBlob)-12],
		"70k line":                 "@revoked box.test " + typeBlob + " " + strings.Repeat("x", 70000),
	}
	for name, bad := range shapes {
		for _, first := range []bool{true, false} {
			content := bad + "\n" + valid + "\n"
			if !first {
				content = valid + "\n" + bad + "\n"
			}
			cb, err := New(write(t, content))
			if err == nil {
				if aerr := cb("box.test:22", addr, k); aerr == nil {
					t.Errorf("%s (bad line first=%v): the revoked key was ACCEPTED", name, first)
				} else {
					t.Errorf("%s (bad line first=%v): file not refused (callback: %v)", name, first, aerr)
				}
			}
		}
	}
}

// Shapes the library reads as it should, accepted and matching.
func TestOrdinaryShapesAreAccepted(t *testing.T) {
	k := testKey(t)
	line := func(hosts string) string { return knownhosts.Line([]string{hosts}, k) }
	hashed := knownhosts.HashHostname("box.test")
	cases := map[string]string{
		"leading space":    " " + line("box.test"),
		"leading tab":      "\t" + line("box.test"),
		"CRLF":             line("box.test") + "\r",
		"trailing comment": line("box.test") + " some comment here",
		"wildcard":         line("*"),
		"negation":         line("!x.test,*"),
		"port":             line("[box.test]:22"),
		"hashed":           knownhosts.Line([]string{hashed}, k),
		"cert-authority":   "@cert-authority " + line("*.test"),
	}
	for name, l := range cases {
		cb, err := New(write(t, "# c\n\n"+l+"\n"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name == "cert-authority" || name == "negation" {
			continue
		}
		if err := cb("box.test:22", addr, k); err != nil {
			t.Errorf("%s: matching host refused: %v", name, err)
		}
	}
}

func TestTooManyLinesAreRefused(t *testing.T) {
	k := testKey(t)
	line := knownhosts.Line([]string{"box.test"}, k) + "\n"
	if _, err := New(write(t, strings.Repeat(line, 20001))); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("want a too-many-lines refusal, got %v", err)
	}
}
