package org

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
)

// `org sign --yes` with no terminal and no marker (an agent that cleared its
// environment) signs nothing without a nonce the operator's terminal issued
// for exactly that definition; the nonce works once.
func TestOrgSignYesWithoutTerminalNeedsANonce(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.22.0")
	orgsigntest.Sign(t, f.root, "growth")
	asOrgOperator(t)
	enforceSignNonceInTest = true
	t.Cleanup(func() { enforceSignNonceInTest = false })
	f.editOutside(t, func(m map[string]interface{}) { m["runtime"] = "codex" })

	f.cfg.TTY = true
	issued := f.mustRun(t, "sign", "growth", "--issue-nonce")
	hash, _ := issued["hash"].(string)
	nonce, _ := issued["nonce"].(string)
	if hash == "" || nonce == "" {
		t.Fatalf("issue = %v", issued)
	}

	f.cfg.TTY = false
	if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", hash); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("signed with --yes and no nonce: %v", err)
	}
	if _, err := f.run(t, "sign", "growth", "--issue-nonce"); err == nil {
		t.Fatal("a nonce was issued without a terminal")
	}
	if len(s.calls) != 0 {
		t.Fatalf("signed without proof: %v", s.calls)
	}
	signed := f.mustRun(t, "sign", "growth", "--yes", "--expect-hash", hash, "--nonce", nonce)
	if signed["signed"] != true || len(s.calls) != 1 {
		t.Fatalf("sign with nonce = %v (calls %v)", signed, s.calls)
	}
	if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", hash, "--nonce", nonce); err == nil {
		t.Fatal("a nonce signed twice")
	}
}
