package apiconfig

import (
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/openaiapi"
)

// The precedence of api models' three classes: flag, then environment, then default; a bad
// value is invalid input (exit 3).

// md is the CLI around the rules: only its exit codes.
var md = Deps{AsCLIError: AsCLIError}

func TestEffectiveContextMaxPrecedence(t *testing.T) {
	t.Parallel()
	env := func(v string) func(string) string { return func(string) string { return v } }
	for _, c := range []struct {
		flag, env string
		want      openaiapi.Class
	}{
		{"", "", openaiapi.ChatOnly}, // nothing set: chat-only
		{"", "sandboxed", openaiapi.Sandboxed},
		{"any", "chat-only", openaiapi.Unconfined}, // the flag beats the environment
	} {
		got, err := md.effectiveContextMax(c.flag, env(c.env))
		if err != nil || got != c.want {
			t.Errorf("md.effectiveContextMax(%q, env=%q) = %v, %v; want %v", c.flag, c.env, got, err, c.want)
		}
	}
	if _, err := md.effectiveContextMax("nope", env("")); clicore.ExitCodeFor(err) != 3 {
		t.Errorf("a bad value must be invalid input, got exit %d", clicore.ExitCodeFor(err))
	}
}

func TestEffectiveAutoMaxPrecedence(t *testing.T) {
	t.Parallel()
	env := func(v string) func(string) string { return func(string) string { return v } }
	for _, c := range []struct {
		flag, env string
		want      openaiapi.Class
	}{
		{"", "", openaiapi.ChatOnly}, // nothing set: chat-only
		{"", "sandboxed", openaiapi.Sandboxed},
		{"any", "chat-only", openaiapi.Unconfined}, // the flag beats the environment
	} {
		got, err := md.effectiveAutoMax(c.flag, env(c.env))
		if err != nil || got != c.want {
			t.Errorf("md.effectiveAutoMax(%q, env=%q) = %v, %v; want %v", c.flag, c.env, got, err, c.want)
		}
	}
	if _, err := md.effectiveAutoMax("nope", env("")); clicore.ExitCodeFor(err) != 3 {
		t.Errorf("a bad value must be invalid input, got exit %d", clicore.ExitCodeFor(err))
	}
}

func TestEffectivePolicyPrecedence(t *testing.T) {
	t.Parallel()
	env := func(v string) func(string) string { return func(string) string { return v } }
	for _, c := range []struct {
		addr, flag, env, want string
	}{
		{"127.0.0.1:1", "", "", "any"},
		{"0.0.0.0:1", "", "", "chat-only"},
		{"127.0.0.1:1", "", "chat-only", "chat-only"},
		{"0.0.0.0:1", "any", "chat-only", "any"}, // the flag beats the environment
	} {
		got, err := md.effectivePolicy(c.addr, c.flag, env(c.env))
		if err != nil || got.String() != c.want {
			t.Errorf("md.effectivePolicy(%q, %q, env=%q) = %s, %v; want %s", c.addr, c.flag, c.env, got, err, c.want)
		}
	}
	if _, err := md.effectivePolicy("127.0.0.1:1", "nope", env("")); clicore.ExitCodeFor(err) != 3 {
		t.Errorf("a bad value must be invalid input, got exit %d", clicore.ExitCodeFor(err))
	}
}
