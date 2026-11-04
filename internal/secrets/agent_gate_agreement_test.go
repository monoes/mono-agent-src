package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The gates that decide which entries an agent reads share one predicate
// (AgentCannotRead): a run's @secret reference (permit), a plaintext read
// (CanAgentReveal) and a grant's own --secret list agree on every name.
func TestAgentReadGatesAgreeOnWhichEntriesAreOffLimits(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	allowed := AllowReservedWrite(ctx)
	for name, v := range map[string]string{"openai": "sk-openai-1", "Github Token": "ghp-secret-1", ReservedEntry: "bearer-value-1", "agent-token-ci": "mat-abcdefgh"} {
		field := "secret"
		if IsReservedEntry(name) {
			field = "token"
		}
		if _, err := Add(allowed, db.DB, "default", "secret", name, map[string]string{field: v}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetAgentAccessGrant(ctx, db.DB, "default", AccessGrant{AnyHost: true, Level: "reveal"}); err != nil {
		t.Fatal(err)
	}
	run := AllowResolving(ctx, db.DB, "default", AccessOrigin{WorkflowID: "wf"})
	for _, c := range []struct {
		name string
		off  bool
	}{
		{"openai", false},
		{"Github Token", false},
		{"agent-token-ci", true},
		{"AGENT-TOKEN-ci", true},
		{" agent-token-ci ", true},
		{"agent-token-", true},
		{"agent-tokens", false},
		{"httpapi-token", true},
		{"HTTPAPI-TOKEN", true},
		{" Httpapi-Token\t", true},
		{"httpapi-token-2", false},
		{"my-httpapi-token", false},
	} {
		if got := AgentCannotRead(c.name); got != c.off {
			t.Errorf("AgentCannotRead(%q) = %v", c.name, got)
		}
		_, resolveErr := Resolve(run, db.DB, "default", "@secret:"+c.name)
		if c.off && !errors.Is(resolveErr, ErrResolveRefused) {
			t.Errorf("a run reading %q: %v, want a refusal", c.name, resolveErr)
		}
		canReveal, err := CanAgentReveal(ctx, db.DB, "default", c.name)
		if err != nil {
			t.Fatal(err)
		}
		if c.off && (canReveal || resolveErr == nil) {
			t.Errorf("%q: reveal=%v, run err=%v: the gates disagree", c.name, canReveal, resolveErr)
		}
		// A name nothing holds is refused by the vault, not by the gate: the
		// two gates agree on the names that are off limits, and let the rest by.
		if !c.off && !canReveal {
			t.Errorf("%q: reveal refused an ordinary name", c.name)
		}
		if err := ValidateGrantSecrets([]string{c.name}); c.off != errors.Is(err, ErrGrantNamesReserved) {
			t.Errorf("a grant naming %q: %v", c.name, err)
		}
	}
}

// With agent access on, a workflow that references @secret:httpapi-token does
// not resolve the HTTP API's bearer (scoped use, and any grant).
func TestAgentAccessNeverResolvesTheHTTPAPIBearer(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(AllowReservedWrite(ctx), db.DB, "default", "secret", ReservedEntry, map[string]string{ReservedTokenField: "bearer-value-1"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]AccessGrant{
		"any host": {AnyHost: true},
		"hosts":    {Hosts: []string{"good.example"}},
		"named":    {AnyHost: true, Secrets: []string{"openai"}},
	} {
		if err := SetAgentAccessGrant(ctx, db.DB, "default", g); err != nil {
			t.Fatal(err)
		}
		run := AllowResolving(ctx, db.DB, "default", AccessOrigin{WorkflowID: "wf"})
		v, err := Resolve(run, db.DB, "default", "@secret:"+ReservedEntry)
		if err == nil || strings.Contains(v, "bearer-value-1") {
			t.Errorf("%s: the bearer resolved: %q, %v", name, v, err)
		}
		if got := MaskResolved(run, "bearer-value-1"); got != "bearer-value-1" {
			t.Errorf("%s: a value never read is masked: %q", name, got)
		}
	}
}
