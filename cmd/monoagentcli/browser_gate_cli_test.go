package main

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/cli/profilegate"
)

// An agent's `browser eval` could send a form around the approval a submit
// needs, so it is refused on allowlisted pages that are not local; local
// pages and a human's terminal are not restricted.
func TestBrowserEvalIsRefusedOnGatedPagesForAgents(t *testing.T) {
	bt := newBrowserTest(t, true)
	if err := browserctl.SaveAllowlist(bt.db().DB, []string{"https://shop.example"}); err != nil {
		t.Fatal(err)
	}
	shop := bt.fake.AddTab("https://shop.example/", "Coder · Agent", true)
	local := bt.fake.AddTab("http://localhost:3000/", "Coder · Agent", false)
	if _, err := bt.run("eval", "--tab", itoa(shop), "fetch('/pay', {method: 'POST'})"); exitCodeFor(err) != 3 || jsonCode(err) != "eval_not_allowed" {
		t.Fatalf("agent eval on shop.example: %v (%s)", err, jsonCode(err))
	}
	for _, c := range bt.fake.Types() {
		if c == "eval_cdp" {
			t.Fatal("the refused script reached the page")
		}
	}
	if res := bt.runJSON("eval", "--tab", itoa(local), "1"); res["result"] == nil {
		t.Fatalf("agent eval on localhost: %v", res)
	}
	t.Setenv("CLAUDECODE", "")
	if _, err := bt.run("eval", "--tab", itoa(shop), "1"); err != nil {
		t.Fatalf("a human's eval on shop.example: %v", err)
	}
}

// After approval the form must still be what the person approved: changing
// a value refuses the submit (submit_changed) and hands the approval back.
func TestBrowserSubmitRefusesAChangedForm(t *testing.T) {
	bt := newBrowserTest(t, true)
	db := bt.db().DB
	if err := browserctl.SaveAllowlist(db, []string{"https://shop.example"}); err != nil {
		t.Fatal(err)
	}
	bt.fake.AddTab("https://shop.example/checkout", "Coder · Agent", true)
	fp := &browserctltest.FormPage{Action: "https://shop.example/pay", Method: "post", Selector: "#pay-form", Submit: true,
		Fields: []browserctl.FormField{{Name: "amount", Type: "text", Value: "10"}}}
	bt.fake.On("eval_cdp", fp.Handler())
	out, err := bt.run("click", "#pay")
	if exitCodeFor(err) != 3 || !strings.Contains(out, "awaiting_approval") {
		t.Fatalf("held click: %v %s", err, out)
	}
	id := strings.Split(strings.Split(out, `"hil_id": "`)[1], `"`)[0]
	t.Setenv("CLAUDECODE", "")
	if _, err := runHIL(t, bt.cfg, "approve", id); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDECODE", "1")

	fp.Set("amount", "10000")
	if _, err := bt.run("submit", "--hil", id); exitCodeFor(err) != 3 || jsonCode(err) != "submit_changed" {
		t.Fatalf("submit after the amount changed: %v (%s)", err, jsonCode(err))
	}
	if it, _ := browserctl.GetSubmitItem(t.Context(), db, "default", id); it.Status != "approved" {
		t.Fatalf("the approval was not handed back: %s", it.Status)
	}
	fp.Set("amount", "10")
	if res := bt.runJSON("submit", "--hil", id); res["status"] != "submitted" {
		t.Fatalf("submit of the approved form: %v", res)
	}
}

// The machine-wide browser and coder commands need no --profile; every
// other browser command drives a profile's browser and does.
func TestBrowserProfileGateClasses(t *testing.T) {
	root := newRootCmd()
	for path, wantWarning := range map[string]bool{
		"browser allow list": false, "browser allow add": false, "browser browsers": false, "browser diff": false,
		"coder projects list": false, "coder projects map": false,
		"browser permissions": false, "browser permissions set": false, "browser permissions site": false, "browser permissions preset": false,
		"browser tabs": true, "browser open": true, "browser click": true, "browser screenshot": true,
		"browser submit": true, "browser styles": true, "coder queue list": true,
	} {
		c, _, err := root.Find(strings.Fields(path))
		if err != nil || c == nil || c.Name() != strings.Fields(path)[len(strings.Fields(path))-1] {
			t.Fatalf("%s: not found (%v)", path, err)
		}
		warning, err := profilegate.Check(c, false, profilegate.ModeWarn, nil)
		if err != nil || (warning != "") != wantWarning {
			t.Errorf("%s without --profile: warning %q, want one: %v", path, warning, wantWarning)
		}
	}
}
