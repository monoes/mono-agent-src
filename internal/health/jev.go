package health

import (
	"context"
	"fmt"
)

// TypeSafe Jev (optional decision provider): is a key configured, and —
// on demand, over the network — does the API accept it. When another decision
// provider is the default, jev.api checks that one and a missing TypeSafe key
// is not applicable (skipped).
const (
	CheckJevKey = "jev.key"
	CheckJevAPI = "jev.api"

	FixJevKey = "jev.key.add"
)

func jevChecks() []Check {
	return []Check{
		{ID: CheckJevKey, Group: GroupIntegrations, Title: "TypeSafe Jev API key", Features: []string{"jev"}, Run: checkJevKey},
		{ID: CheckJevAPI, Group: GroupIntegrations, Title: "TypeSafe Jev API", Features: []string{"jev"},
			Network: true, OnDemand: true, Run: checkJevAPI},
	}
}

func jevFixes() []Fix {
	return []Fix{
		{FixInfo: FixInfo{ID: FixJevKey, Label: "Store a TypeSafe API key in the vault (or set TYPESAFE_API_KEY)", Safety: SafetyManual,
			Command: "monoagentcli jev key set"},
			Apply: func(context.Context, *Env, func(string)) error { return fmt.Errorf("this needs to be done by hand") }},
	}
}

// jevProvider is the default decision provider env names ("typesafe" when it names none).
func jevProvider(env *Env) string {
	if env.JevProvider != nil {
		if p := env.JevProvider(); p != "" {
			return p
		}
	}
	return "typesafe"
}

func checkJevKey(ctx context.Context, env *Env) Result {
	if env.JevKey == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	if env.ProfileID == "" {
		return noProfile()
	}
	source, err := env.JevKey(ctx)
	if err != nil || source == "" {
		if p := jevProvider(env); p != "typesafe" {
			// The key is TypeSafe's; another provider answers by default.
			return Result{Status: StatusSkip, Summary: fmt.Sprintf("not needed: the default decision provider is %q, not TypeSafe", p)}
		}
		return Result{Status: StatusWarn, Summary: "no key (Jev is optional; its surfaces stay off without one)", FixID: FixJevKey}
	}
	if source == "vault" {
		// KeySource only lists vault entries; it never decrypts one.
		return Result{Status: StatusOK, Summary: "vault entry `typesafe` present (not decrypted)"}
	}
	return Result{Status: StatusOK, Summary: "key from " + source}
}

func checkJevAPI(ctx context.Context, env *Env) Result {
	if env.JevModels == nil || env.JevKey == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	if env.ProfileID == "" {
		return noProfile()
	}
	if p := jevProvider(env); p != "typesafe" {
		// JevModels checks the default provider: its models, else one tiny question.
		if err := env.JevModels(ctx); err != nil {
			return Result{Status: StatusWarn, Summary: fmt.Sprintf("decision provider %q: %v", p, err)}
		}
		return Result{Status: StatusOK, Summary: fmt.Sprintf("decision provider %q answers", p)}
	}
	if source, err := env.JevKey(ctx); err != nil || source == "" {
		return Result{Status: StatusSkip, Summary: "no key"}
	}
	if err := env.JevModels(ctx); err != nil {
		return Result{Status: StatusWarn, Summary: err.Error()}
	}
	return Result{Status: StatusOK, Summary: "API reachable, key accepted"}
}
