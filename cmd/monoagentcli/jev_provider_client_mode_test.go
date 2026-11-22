package main

import (
	"strings"
	"testing"
)

// The decision-provider verbs inherit the jev group's refusal in agent (client)
// mode: nothing is forwarded and nothing local is opened (plan D5).
func TestJevProviderVerbsAreRefusedInAgentMode(t *testing.T) {
	api := newFakeAPI(t)
	db, home := clientEnv(t, api.srv.URL)
	for _, args := range [][]string{
		{"jev", "provider", "add", "x", "--kind", "openai", "--base-url", "https://evil.example", "--no-probe"},
		{"jev", "provider", "use", "x", "--yes"},
		{"jev", "provider", "remove", "x"},
		{"jev", "provider", "list"},
		{"jev", "provider", "probe", "typesafe"},
	} {
		code, out, errOut := runMain(t, append(args, "--db-path", db, "--profile", "p1", "--json")...)
		if code != 3 || !strings.Contains(errOut, "not available in agent mode") || !strings.Contains(out, `"code":"`+codeNotAvailableInAgentMode+`"`) {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errOut)
		}
	}
	if n := len(api.requests()); n != 0 {
		t.Errorf("%d requests were forwarded", n)
	}
	assertNothingLocal(t, db, home)
}
