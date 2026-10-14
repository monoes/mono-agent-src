package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/connections"
)

func TestConnectListAllShowsUmbrellasFirst(t *testing.T) {
	e := newSessionsTestEnv(t)
	out, errOut, code := e.run(t, "", "--json", "connect", "list", "--all")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	var platforms []struct{ ID string }
	if err := json.Unmarshal([]byte(out), &platforms); err != nil || len(platforms) < 3 {
		t.Fatalf("list --all = %q (%v)", out, err)
	}
	if platforms[0].ID != "google" || platforms[1].ID != "microsoft" {
		t.Errorf("first platforms = %s, %s; want google, microsoft", platforms[0].ID, platforms[1].ID)
	}
	out, _, _ = e.run(t, "", "connect", "list", "--all")
	lines := strings.Split(out, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[1], "google ") || !strings.HasPrefix(lines[2], "microsoft ") {
		t.Errorf("table order:\n%s", strings.Join(lines[:3], "\n"))
	}
}

func TestConnectForNodeIncludesUmbrellaConnections(t *testing.T) {
	e := newSessionsTestEnv(t)
	store := connections.NewStore(e.db.DB)
	ctx := context.Background()
	legacy := &connections.Connection{Platform: "gmail", Method: connections.MethodOAuth, Label: "Gmail", Data: map[string]interface{}{"access_token": "a"}}
	google := &connections.Connection{Platform: "google", Method: connections.MethodOAuth, Label: "Google", Data: map[string]interface{}{"access_token": "b", "oauth_platform": "google"}}
	ms := &connections.Connection{Platform: "microsoft", Method: connections.MethodOAuth, Label: "Microsoft", Data: map[string]interface{}{"access_token": "c", "oauth_platform": "microsoft"}}
	for _, c := range []*connections.Connection{legacy, google, ms} {
		if err := store.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	forNode := func(nodeType string) []string {
		out, errOut, code := e.run(t, "", "--json", "connect", "for-node", nodeType)
		if code != 0 {
			t.Fatalf("for-node %s: code %d %s", nodeType, code, errOut)
		}
		var opts []struct{ ID string }
		if err := json.Unmarshal([]byte(out), &opts); err != nil {
			t.Fatalf("for-node %s = %q", nodeType, out)
		}
		var ids []string
		for _, o := range opts {
			ids = append(ids, o.ID)
		}
		return ids
	}
	if got := forNode("service.gmail"); strings.Join(got, ",") != legacy.ID+","+google.ID {
		t.Errorf("gmail candidates = %v", got)
	}
	if got := forNode("service.google_sheets"); strings.Join(got, ",") != google.ID {
		t.Errorf("sheets candidates = %v", got)
	}
	if got := forNode("service.outlook_mail"); strings.Join(got, ",") != ms.ID {
		t.Errorf("outlook candidates = %v", got)
	}
}

func TestConnectScopesFlagValidation(t *testing.T) {
	e := newSessionsTestEnv(t)
	_, errOut, code := e.run(t, "", "connect", "google", "--scopes", "sheets,calender")
	if code == 0 || !strings.Contains(errOut, `unknown google scope group "calender"`) {
		t.Errorf("unknown group: code %d, stderr %q", code, errOut)
	}
	_, errOut, code = e.run(t, "", "connect", "slack", "--scopes", "gmail")
	if code == 0 || !strings.Contains(errOut, "--scopes applies only to the google and microsoft platforms") {
		t.Errorf("non-umbrella: code %d, stderr %q", code, errOut)
	}
	_, errOut, code = e.run(t, "", "connect", "oauth", "gmail", "--scopes", "gmail")
	if code == 0 || !strings.Contains(errOut, "--scopes applies only") {
		t.Errorf("oauth non-umbrella: code %d, stderr %q", code, errOut)
	}
}
