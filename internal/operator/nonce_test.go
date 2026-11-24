package operator

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNonceOneTimeBoundAndOperatorOnly(t *testing.T) {
	asOperator(t)
	t.Setenv("HOME", t.TempDir())

	if _, err := IssueNonce("org-sign", "growth:abc", false); err == nil {
		t.Fatal("a non-interactive run issued a nonce")
	}
	t.Setenv("CLAUDECODE", "1")
	if _, err := IssueNonce("org-sign", "growth:abc", true); err == nil {
		t.Fatal("a marked process issued a nonce")
	}
	asOperator(t)

	n, err := IssueNonce("org-sign", "growth:abc", true)
	if err != nil || n == "" {
		t.Fatalf("issue: %q %v", n, err)
	}
	if err := ConsumeNonce("org-sign", "growth:other", n); err == nil {
		t.Fatal("a nonce for another subject was accepted")
	}
	// the failed attempt used it up: one try per nonce.
	if err := ConsumeNonce("org-sign", "growth:abc", n); err == nil {
		t.Fatal("a nonce survived a wrong-subject attempt")
	}
	n, _ = IssueNonce("org-sign", "growth:abc", true)
	if err := ConsumeNonce("other", "growth:abc", n); err == nil {
		t.Fatal("a nonce for another purpose was accepted")
	}
	n, _ = IssueNonce("org-sign", "growth:abc", true)
	if err := ConsumeNonce("org-sign", "growth:abc", n); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := ConsumeNonce("org-sign", "growth:abc", n); err == nil {
		t.Fatal("a nonce was accepted twice")
	}
	if err := ConsumeNonce("org-sign", "growth:abc", ""); err == nil {
		t.Fatal("an empty nonce was accepted")
	}
}

func TestNonceExpiresAndStoresOnlyAHash(t *testing.T) {
	asOperator(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	n, err := IssueNonce("p", "s", true)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".monoagent", "operator-nonces")
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || strings.Contains(ents[0].Name(), n) {
		t.Fatalf("nonce stored in the clear: %v", ents)
	}
	p := filepath.Join(dir, ents[0].Name())
	old := `{"purpose":"p","subject":"s","expires":` + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10) + `}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeNonce("p", "s", n); err == nil {
		t.Fatal("an expired nonce was accepted")
	}
}
