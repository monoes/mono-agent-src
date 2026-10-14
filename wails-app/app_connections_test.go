package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlatformNamesComeFromConnectListAll(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
echo '[{"ID":"generic_api","Name":"Generic API Key","Category":"generic"},{"ID":"gmail","Name":"Gmail"}]'
`
	bin := filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a := newTestApp(t)
	a.ctx = context.Background()

	names, err := a.PlatformNames()
	if err != nil || names["generic_api"] != "Generic API Key" || names["gmail"] != "Gmail" {
		t.Fatalf("PlatformNames = %v, %v", names, err)
	}
	log, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(log), "connect list --all") {
		t.Errorf("argv log:\n%s", log)
	}
}

// ShowPlatform returns `connect show <platform> --json` verbatim.
func TestShowPlatformShellsOut(t *testing.T) {
	argsLog, _ := connFakeCLI(t, `echo '{"id":"generic_oauth1","methods":["oauth1"],"oauth1":{}}'`)
	a := newConnTestApp(t)
	if got := a.ShowPlatform("generic_oauth1"); got != `{"id":"generic_oauth1","methods":["oauth1"],"oauth1":{}}` {
		t.Fatalf("ShowPlatform = %q", got)
	}
	if got := strings.TrimSpace(readLog(t, argsLog)); got != "--profile work --json connect show generic_oauth1" {
		t.Fatalf("argv = %q", got)
	}
}

// The OAuth connect passes each connect field as --field and the secret
// ones as a JSON object on stdin (--stdin-json), never in argv; the flow's
// stdout lines ahead of the connection JSON become progress.
func TestOAuthConnectFieldArgs(t *testing.T) {
	const secret = "consumer-secret-xyz"
	argsLog, stdinLog := connFakeCLI(t, `echo '→ Opening browser: http://127.0.0.1:1/authorize?oauth_token=t'
echo '→ Could not open browser automatically. Please open this URL manually:'
echo '  http://127.0.0.1:1/authorize?oauth_token=t'
sleep 0.2
echo '{"message":"Waiting for the browser","kind":"info"}' >&2
echo '{"id":"c1","platform":"generic_oauth1","method":"oauth1","account_id":"acct","status":"active","created_at":"","updated_at":""}'
`)
	a := newConnTestApp(t)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		t.Fatal(err)
	}
	pairs := []string{"request_token_url=http://127.0.0.1:1/rt", "consumer_key=ck"}
	var got []string
	conn, err := runOAuthConnect(a.ctx, cliBin, oauthConnectArgs("work", "generic_oauth1", pairs, true),
		`{"consumer_secret":"`+secret+`"}`, func(msg, kind string) { got = append(got, kind+":"+msg) })
	if err != nil || conn.AccountID != "acct" || strings.Join(got, "|") != "info:Opening browser: http://127.0.0.1:1/authorize?oauth_token=t|info:Could not open browser automatically. Please open this URL manually: http://127.0.0.1:1/authorize?oauth_token=t|info:Waiting for the browser" {
		t.Fatalf("runOAuthConnect = %+v, %v, %v", conn, err, got)
	}
	want := "--profile work --json connect oauth generic_oauth1 --field request_token_url=http://127.0.0.1:1/rt --field consumer_key=ck --stdin-json"
	if args := strings.TrimSpace(readLog(t, argsLog)); args != want {
		t.Fatalf("argv = %q\nwant %q", args, want)
	}
	if stdin := readLog(t, stdinLog); stdin != `{"consumer_secret":"`+secret+`"}` {
		t.Fatalf("stdin = %q", stdin)
	}

	// No secret fields: no --stdin-json, nothing on stdin.
	if got := strings.Join(oauthConnectArgs("p", "zendesk_oauth", []string{"subdomain=acme"}, false), " "); got != "--profile p --json connect oauth zendesk_oauth --field subdomain=acme" {
		t.Fatalf("args = %q", got)
	}
}

// Cancelling a running flow kills the CLI; cancelling none is an error. A
// new flow for the platform replaces (cancels) the running one, and a
// finished flow leaves its successor tracked.
func TestCancelPlatformOAuth(t *testing.T) {
	a := newConnTestApp(t)
	if r := a.CancelPlatformOAuth("trello_oauth1"); !strings.HasPrefix(r, "error:") {
		t.Fatalf("cancel with no flow = %q", r)
	}
	first, finishFirst := trackOAuthFlow(context.Background(), "trello_oauth1")
	ctx, finish := trackOAuthFlow(context.Background(), "trello_oauth1")
	defer finish()
	if first.Err() == nil {
		t.Fatal("a second flow left the first running")
	}
	finishFirst()
	if _, ok := oauthFlows.Load("trello_oauth1"); !ok {
		t.Fatal("the first flow's end untracked the second")
	}

	slow := fakeCLI(t, "exec sleep 30\n")
	errc := make(chan error, 1)
	go func() {
		_, err := runOAuthConnect(ctx, slow, nil, "", func(string, string) {})
		errc <- err
	}()
	if r := a.CancelPlatformOAuth("trello_oauth1"); r != "ok" {
		t.Fatalf("cancel = %q", r)
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a cancelled flow succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the CLI kept running after cancel")
	}
	if r := a.CancelPlatformOAuth("trello_oauth1"); !strings.HasPrefix(r, "error:") {
		t.Fatalf("second cancel = %q", r)
	}
}

// ListPlatformsJSON lists the CLI's platforms (`connect list --all`), which
// include the ones API packages register, in the frontend's shape.
func TestListPlatformsJSONComesFromCLI(t *testing.T) {
	argsLog, _ := connFakeCLI(t, `echo '[{"ID":"trello_oauth1","Name":"Trello (OAuth 1.0a)","Category":"app","ConnectVia":"API","Methods":["oauth1"],"Fields":{"oauth1":[{"Key":"consumer_key","Label":"API Key","Required":true}]},"OAuth1":{"RequestTokenURL":"https://trello.com/1/OAuthGetRequestToken"},"IconEmoji":"T"},
{"ID":"telegram","Name":"Telegram","Category":"communication","ConnectVia":"Both","Methods":["apikey"]}]'`)
	a := newConnTestApp(t)
	var got []PlatformInfo
	if err := json.Unmarshal([]byte(a.ListPlatformsJSON("")), &got); err != nil || len(got) != 2 {
		t.Fatalf("ListPlatformsJSON = %+v, %v", got, err)
	}
	if p := got[0]; p.ID != "trello_oauth1" || p.Category != "app" || p.Methods[0] != "oauth1" || p.Fields["oauth1"][0].Key != "consumer_key" || p.IconEmoji != "T" {
		t.Fatalf("platform = %+v", p)
	}
	if err := json.Unmarshal([]byte(a.ListPlatformsJSON("Both")), &got); err != nil || len(got) != 1 || got[0].ID != "telegram" {
		t.Fatalf("connectVia Both = %+v, %v", got, err)
	}
	if args := readLog(t, argsLog); !strings.HasPrefix(args, "--profile work --json connect list --all\n") {
		t.Fatalf("argv = %q", args)
	}
}
