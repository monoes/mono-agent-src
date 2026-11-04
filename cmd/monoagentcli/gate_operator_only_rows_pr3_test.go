package main

import (
	"context"
	"sort"
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

// browserRunTrustFamilies are the command families PR 3 converted verbs in. Every leaf command of them is
// either a row of operatorOnlyRows (it asks the gate) or listed in openInTheBrowserRunTrustFamilies with the
// reason it does not: a converted verb whose row is deleted, and a new verb nobody decided about, both fail.
var browserRunTrustFamilies = []string{"automation", "record", "browser allow", "browser permissions", "coder queue", "coder projects"}

const (
	readOnlyVerb  = "read-only or shows what exists"
	notDecided    = "not part of the browser-run trust change; whether it is the user's alone is not decided (issue 1333)"
	heldInstall   = "held: class B. The shipped action-template-generator skill, `crawl` and `ref crawling` tell an agent to run `automation install <id> --local --yes`; waits for the owner's answer (issue 1333)"
	opensNoRecord = "touches only the item or recording it names and runs nothing in a browser"
)

var openInTheBrowserRunTrustFamilies = map[string]string{
	"automation list": readOnlyVerb, "automation show": readOnlyVerb, "automation validate": readOnlyVerb,
	"automation test": readOnlyVerb + " (--live is not supported)", "automation export": readOnlyVerb, "automation pack": readOnlyVerb,
	"automation install": heldInstall, "automation new": heldInstall + " (--install installs as local)",
	"automation enable": notDecided, "automation disable": notDecided, "automation uninstall": notDecided,
	"automation restore": notDecided, "automation rollback": notDecided,
	"browser allow list": readOnlyVerb, "coder projects list": readOnlyVerb,
	"coder queue list": readOnlyVerb, "coder queue show": readOnlyVerb, "coder queue image": readOnlyVerb,
	"coder queue dismiss": opensNoRecord,
	"record list":         readOnlyVerb, "record show": readOnlyVerb, "record api": readOnlyVerb,
	"record analyze": notDecided + " (the AI call reads a recording; it runs nothing in a browser)", "record delete": opensNoRecord,
}

func TestEveryLeafOfTheBrowserRunTrustFamiliesIsARowOrAnAcknowledgedOpenVerb(t *testing.T) {
	all, _ := treeKeys(t)
	rows := map[string]bool{}
	for _, r := range operatorOnlyRows() {
		rows[r.path] = true
	}
	root := newRootCmd()
	var bad []string
	seen := map[string]bool{}
	for _, c := range allCommands(root) {
		k := commandKey(c)
		in := false
		for _, f := range browserRunTrustFamilies {
			in = in || k == f || strings.HasPrefix(k, f+" ")
		}
		if !in || c.HasSubCommands() || c.Hidden {
			continue
		}
		seen[k] = true
		_, open := openInTheBrowserRunTrustFamilies[k]
		switch {
		case rows[k] && open:
			bad = append(bad, k+": both a row and an open verb")
		case !rows[k] && !open:
			bad = append(bad, k+": a leaf of a converted family with no row and no entry in openInTheBrowserRunTrustFamilies")
		}
	}
	for k, why := range openInTheBrowserRunTrustFamilies {
		if !all[k] || !seen[k] {
			bad = append(bad, k+": listed as open, but not a leaf command of the tree")
		}
		if why == "" {
			bad = append(bad, k+": an open verb needs a reason")
		}
	}
	for _, r := range operatorOnlyRowsPR3() {
		if !seen[r.path] {
			bad = append(bad, r.path+": a PR 3 row whose command is not a leaf of a converted family")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("the converted families, the rows and the open verbs disagree:\n%s", strings.Join(bad, "\n"))
	}
}

// The flags of `automation trust` that change a package's trust gate in every spelling, and the bare form
// (which shows the package) stays open to a process the gate refuses.
func TestAutomationTrustGatesEveryFlagAndStaysOpenToShow(t *testing.T) {
	needAncestry(t)
	for _, flag := range []string{"--scripts", "--no-scripts", "--live", "--no-live"} {
		cfg := opEnv(t)
		markThisProcessAsACoderTurn(t, cfg)
		err := opRow{build: newAutomationCmd, args: []string{"trust", flag, "no-such-package"}}.run(t, cfg)
		requireOperatorOnly(t, err)
		if !strings.Contains(err.Error(), "coder session's turn") {
			t.Errorf("trust %s in a turn: %v", flag, err)
		}
	}
	cfg := opEnv(t)
	markThisProcessAsACoderTurn(t, cfg)
	err := opRow{build: newAutomationCmd, args: []string{"trust", "no-such-package"}}.run(t, cfg)
	if err == nil || isOperatorOnly(err) {
		t.Fatalf("trust show in a turn: %v (want the package not found, not a refusal)", err)
	}
	// both flags of a pair are a usage error before any gate, for everyone
	cfg = opEnv(t)
	if err := (opRow{build: newAutomationCmd, args: []string{"trust", "--live", "--no-live", "x"}}).run(t, cfg); err == nil || isOperatorOnly(err) {
		t.Fatalf("--live --no-live: %v", err)
	}
}
