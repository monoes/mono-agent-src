package main

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

// operatorOnlyRowsPR3 are the verbs that trust a browser run with a user's sign-ins, converted in the third
// change of issue 1333: automation trust, rerecord and doctor accept-repair; record verify, replay and save;
// browser allow and permissions; coder queue add and start and the coder projects map. The args reach the gate
// of each verb before anything else (a name that does not exist, a kind that is not one), so the operator's
// pass reaches no browser, bridge or recording. None of them is a recovery verb: each grants a browser run
// more reach, or changes what one clicks, and none repairs a damaged install.
func operatorOnlyRowsPR3() []opRow {
	return []opRow{
		{path: "automation trust", build: newAutomationCmd, args: []string{"trust", "--scripts", "no-such-package"}},
		{path: "automation rerecord", build: newAutomationCmd, args: []string{"rerecord", "no-such-package", "some.key"}},
		{path: "automation doctor accept-repair", build: newAutomationCmd, args: []string{"doctor", "accept-repair", "no-such-package", "some.key"}},
		{path: "record verify", build: newRecordCmd, args: []string{"verify", "no-such-draft"}},
		{path: "record replay", build: newRecordCmd, args: []string{"replay", "no-such-recording"}},
		{path: "record save", build: newRecordCmd, args: []string{"save", "no-such-draft"}},
		{path: "browser allow add", build: newBrowserCmd, args: []string{"allow", "add", "https://staging.example.com"}},
		{path: "browser allow remove", build: newBrowserCmd, args: []string{"allow", "remove", "https://staging.example.com"}},
		{path: "browser permissions set", build: newBrowserCmd, args: []string{"permissions", "set", "coder", "ask"}},
		{path: "browser permissions site", build: newBrowserCmd, args: []string{"permissions", "site", "https://github.com", "allow"}},
		{path: "coder queue add", build: newCoderCmd, args: []string{"queue", "add", "--kind", "no-such-kind"}},
		{path: "coder queue start", build: newCoderCmd, args: []string{"queue", "start", "no-such-item"}},
		{path: "coder projects map", build: newCoderCmd, args: []string{"projects", "map", "http://localhost:3000", "{tmp}"}},
		{path: "coder projects unmap", build: newCoderCmd, args: []string{"projects", "unmap", "http://localhost:3000"}},
	}
}

func TestNoVerbOfTheBrowserRunTrustChangeIsARecoveryVerb(t *testing.T) {
	for _, r := range operatorOnlyRowsPR3() {
		if r.recovery {
			t.Errorf("%s is marked as a recovery verb", r.path)
		}
	}
}

// `record verify` and `record replay` type a recording's {{secret:}} values into a page. A process the gate
// refuses (here: inside a recorded coder turn, no marker in its environment) reads no secret, because the
// recording and the page it types into may be that process's.
func TestRecordSecretLookupReadsNothingInsideACoderTurn(t *testing.T) {
	needAncestry(t)
	cfg := opEnv(t)
	d, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), d.DB, "default", "secret", "automation:example-go/pw", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	d.Close()
	lookup := func() (string, bool) {
		l, release := recordDeps(cfg).VaultSecretLookup(context.Background(), "example-go")
		defer release()
		if l == nil {
			t.Fatal("no lookup")
		}
		return l("pw")
	}
	if v, ok := lookup(); !ok || v != "vsecret-777" {
		t.Fatalf("the operator's lookup: %q, %v", v, ok)
	}
	markThisProcessAsACoderTurn(t, cfg)
	if v, ok := lookup(); ok || strings.Contains(v, "vsecret") {
		t.Fatalf("a process inside a coder turn read the secret: %q, %v", v, ok)
	}
}

// `record replay --dry-run` runs nothing, so it stays open to a process the gate refuses: inside a recorded
// coder turn it gets as far as looking the recording up.
func TestRecordReplayDryRunStaysOpenInsideACoderTurn(t *testing.T) {
	needAncestry(t)
	cfg := opEnv(t)
	markThisProcessAsACoderTurn(t, cfg)
	err := opRow{build: newRecordCmd, args: []string{"replay", "no-such-recording", "--dry-run"}}.run(t, cfg)
	if err == nil || isOperatorOnly(err) {
		t.Fatalf("replay --dry-run in a turn: %v (want the recording not found, not a refusal)", err)
	}
}
