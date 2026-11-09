package org

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// chatFake is an org for `org chat`: the item lists are fixtures, and a
// resolve flips the item the way monomind would.
type chatFake struct {
	mu     sync.Mutex
	status string
	lists  map[string]json.RawMessage
	sent   []string
}

func (f *chatFake) Status(context.Context, string, string) (json.RawMessage, error) {
	st := map[string]interface{}{"v": 1, "status": f.status, "run": "run-a", "paused": false}
	if f.status == "crashed" {
		st["error"] = "pid 4242 gone"
	}
	return json.Marshal(st)
}
func (f *chatFake) All(_ context.Context, _, _, kind string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists[kind], nil
}
func (f *chatFake) Answer(_ context.Context, _, _, id, answer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p map[string][]map[string]interface{}
	_ = json.Unmarshal(f.lists["questions"], &p)
	for _, q := range p["items"] {
		if q["questionId"] == id {
			q["answer"] = answer
		}
	}
	f.lists["questions"], _ = json.Marshal(p)
	f.sent = append(f.sent, "answer "+id+" "+answer)
	return nil
}
func (f *chatFake) Dismiss(_ context.Context, _, _, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p map[string][]map[string]interface{}
	_ = json.Unmarshal(f.lists["questions"], &p)
	for _, q := range p["items"] {
		if q["questionId"] == id {
			q["state"] = "dismissed"
		}
	}
	f.lists["questions"], _ = json.Marshal(p)
	f.sent = append(f.sent, "dismiss "+id+" "+reason)
	return nil
}
func (f *chatFake) Approve(_ context.Context, _, _, role, action string, approve bool, _ string) error {
	f.sent = append(f.sent, "approve "+role+" "+action)
	return nil
}
func (f *chatFake) Gate(_ context.Context, _, _, id string, approve bool, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p map[string][]map[string]interface{}
	_ = json.Unmarshal(f.lists["gates"], &p)
	for _, g := range p["items"] {
		if g["id"] == id {
			g["status"] = map[bool]string{true: "approved", false: "rejected"}[approve]
		}
	}
	f.lists["gates"], _ = json.Marshal(p)
	f.sent = append(f.sent, "gate "+id+" "+note)
	return nil
}

// setupOrgChat writes the acme org (ceo → dev → qa) under a fresh profile
// folder, points the chat at fakes, and returns the config and the fake.
func setupOrgChat(t *testing.T) (*testConfig, *chatFake, string) {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "chat.db"), ProfileID: "default"}
	env := testEnv(cfg)
	root := env.root()
	env.Close()
	ceo, dev := "ceo", "dev"
	doc := &orgdesign.Doc{Name: "acme", Goal: "ship", Roles: []orgdesign.Role{
		{ID: "ceo", Title: "Chief", Type: "boss"},
		{ID: "dev", Title: "Developer", Type: "coder", ReportsTo: &ceo},
		{ID: "qa", Title: "QA", Type: "tester", ReportsTo: &dev},
	}}
	b, _ := json.Marshal(doc)
	if err := os.MkdirAll(orgdesign.OrgsDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgdesign.OrgsDir(root), "acme.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("../../orgchat/testdata/human-items.json")
	if err != nil {
		t.Fatal(err)
	}
	fake := &chatFake{status: "running"}
	if err := json.Unmarshal(raw, &fake.lists); err != nil {
		t.Fatal(err)
	}
	prevClient, prevLogs, prevSend := orgChatClient, orgChatLogs, orgChatSend
	t.Cleanup(func() { orgChatClient, orgChatLogs, orgChatSend = prevClient, prevLogs, prevSend })
	orgChatClient = fake
	orgChatLogs = func(_ context.Context, _, _, _ string) (json.RawMessage, error) {
		f, err := os.Open("../../orgchat/testdata/boss-thread.jsonl")
		if err != nil {
			return nil, err
		}
		defer f.Close()
		var items []json.RawMessage
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			items = append(items, append(json.RawMessage(nil), sc.Bytes()...))
		}
		return json.Marshal(map[string]interface{}{"v": 1, "items": items})
	}
	return cfg, fake, root
}

func runOrgChat(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := newOrgCmd(cfg)
		cmd.SetArgs(append([]string{"chat"}, args...))
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return out, err
}

func TestOrgChatHistory(t *testing.T) {
	cfg, _, _ := setupOrgChat(t)
	out, err := runOrgChat(t, cfg, "history", "acme")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Boss      string           `json:"boss"`
		BossTitle string           `json:"boss_title"`
		Status    string           `json:"status"`
		Run       string           `json:"run"`
		Roles     []orgChatRole    `json:"roles"`
		Items     []map[string]any `json:"items"`
		Pending   map[string]int   `json:"pending"`
		Warnings  []string         `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Boss != "ceo" || got.BossTitle != "Chief" || got.Status != "running" || got.Run != "run-a" {
		t.Errorf("header = %+v", got)
	}
	if len(got.Roles) != 3 || got.Roles[2].ReportsTo == nil || *got.Roles[2].ReportsTo != "dev" {
		t.Errorf("roles = %+v", got.Roles)
	}
	if got.Pending["questions"] != 2 || got.Pending["gates"] != 1 || got.Pending["approvals"] != 0 {
		t.Errorf("pending = %v", got.Pending)
	}
	if len(got.Items) != 12 || got.Items[1]["kind"] != "human" || got.Items[2]["kind"] != "boss" {
		t.Errorf("items = %v", got.Items)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %v", got.Warnings)
	}

	out, err = runOrgChat(t, cfg, "history", "acme", "--limit", "1")
	if err != nil || strings.Count(out, `"kind":`) != 3 {
		t.Errorf("--limit 1 keeps the newest plus every pending item: %v %s", err, out)
	}
	if _, err := runOrgChat(t, cfg, "history", "nope"); exitCodeFor(err) != 2 {
		t.Errorf("unknown org: %v", err)
	}
}

func TestOrgChatAnswerIdempotentAndStopped(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	out, err := runOrgChat(t, cfg, "answer", "acme", "q-2250-cd34", "--", "yes,", "run", "it")
	if err != nil || !strings.Contains(out, `"already":false`) || !strings.Contains(out, `"state":"answered"`) {
		t.Fatalf("answer: %v %s", err, out)
	}
	out, err = runOrgChat(t, cfg, "answer", "acme", "q-2250-cd34", "--", "yes")
	if err != nil || !strings.Contains(out, `"already":true`) {
		t.Fatalf("second answer: %v %s", err, out)
	}
	if len(fake.sent) != 1 || fake.sent[0] != "answer q-2250-cd34 yes, run it" {
		t.Fatalf("sent %v", fake.sent)
	}

	fake.status = "stopped"
	_, err = runOrgChat(t, cfg, "approve", "acme", "gate-1850-x1", "--", "ship", "it")
	if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("gate on a stopped org: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sent to a stopped org: %v", fake.sent)
	}
	fake.status = "running"
	if out, err := runOrgChat(t, cfg, "approve", "acme", "gate-1850-x1", "--", "ship", "it"); err != nil || !strings.Contains(out, `"state":"approved"`) {
		t.Fatalf("gate: %v %s", err, out)
	}
	if out, err := runOrgChat(t, cfg, "deny", "acme", "gate-1850-x1"); err != nil || !strings.Contains(out, `"already":true`) || !strings.Contains(out, `"state":"approved"`) {
		t.Fatalf("deny after approve reports how it ended: %v %s", err, out)
	}
	if out, err := runOrgChat(t, cfg, "approve", "acme", "dev:Bash"); err != nil || !strings.Contains(out, `"already":true`) {
		t.Fatalf("approval resolved before: %v %s", err, out)
	}
	if _, err := runOrgChat(t, cfg, "answer", "acme", "q-nope", "--", "x"); exitCodeFor(err) != 2 {
		t.Errorf("unknown question: %v", err)
	}
	if _, err := runOrgChat(t, cfg, "answer", "acme", "q-0900-zz99", "--", " "); exitCodeFor(err) != 3 {
		t.Errorf("empty answer: %v", err)
	}
	want := []string{"answer q-2250-cd34 yes, run it", "gate gate-1850-x1 ship it"}
	if strings.Join(fake.sent, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %q, want %q", fake.sent, want)
	}
}

func TestOrgChatSendGoesToTheBossAsTheOperator(t *testing.T) {
	cfg, _, _ := setupOrgChat(t)
	var got orgbridge.SendRequest
	orgChatSend = func(_ context.Context, _ *orgbridge.Ledger, req orgbridge.SendRequest) (*orgbridge.SendResult, error) {
		got = req
		res := &orgbridge.SendResult{}
		res.To, res.Delivery, res.MessageID = "ceo", "live", "msg-1"
		return res, nil
	}
	out, err := runOrgChat(t, cfg, "send", "acme", "--", "Please", "ship", "today")
	if err != nil {
		t.Fatal(err)
	}
	if got.Org != "acme" || got.To != "" || got.From != "human:operator" || got.Body != "Please ship today" || got.Subject != orgChatSubject {
		t.Errorf("request = %+v", got)
	}
	if !strings.Contains(out, `"delivery":"live"`) || !strings.Contains(out, `"to":"ceo"`) {
		t.Errorf("out = %s", out)
	}
	if _, err := runOrgChat(t, cfg, "send", "acme", "--", "  "); exitCodeFor(err) != 3 {
		t.Errorf("empty message: %v", err)
	}
	if _, err := runOrgChat(t, cfg, "send", "nope", "--", "hi"); exitCodeFor(err) != 2 {
		t.Errorf("unknown org: %v", err)
	}
}

// The GUI's org bubble tests render exactly what `org chat history` prints
// for the acme fixture: this keeps their copy in step with the CLI.
// UPDATE_ORG_CHAT_FIXTURE=1 rewrites it.
func TestOrgChatHistoryGUIFixture(t *testing.T) {
	cfg, _, _ := setupOrgChat(t)
	out, err := runOrgChat(t, cfg, "history", "acme")
	if err != nil {
		t.Fatal(err)
	}
	var v interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(v, "", "  ")
	want = append(want, '\n')
	path := "../../../wails-app/frontend/src/components/bubbles/__fixtures__/acme-history.json"
	if os.Getenv("UPDATE_ORG_CHAT_FIXTURE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is out of date with `org chat history`; rerun with UPDATE_ORG_CHAT_FIXTURE=1", path)
	}

	// And the bus it was built from, as the JSON array the GUI test imports.
	raw, err := orgChatLogs(context.Background(), "", "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	var logs struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatal(err)
	}
	bus := []byte("[\n")
	for i, line := range logs.Items {
		bus = append(bus, "  "...)
		bus = append(bus, line...)
		if i < len(logs.Items)-1 {
			bus = append(bus, ',')
		}
		bus = append(bus, '\n')
	}
	bus = append(bus, "]\n"...)
	busPath := "../../../wails-app/frontend/src/components/bubbles/__fixtures__/acme-bus.json"
	if os.Getenv("UPDATE_ORG_CHAT_FIXTURE") == "1" {
		if err := os.WriteFile(busPath, bus, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(busPath); err != nil || string(got) != string(bus) {
		t.Fatalf("%s is out of date with internal/orgchat/testdata; rerun with UPDATE_ORG_CHAT_FIXTURE=1 (%v)", busPath, err)
	}
}

// The app puts "--" before every value it passes, and refs are checked, so
// nothing the page sends can be read as a flag (#267 review).
func TestOrgChatRefusesFlagLikeRefs(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	for _, args := range [][]string{
		{"approve", "--", "acme", "--project=/elsewhere"},
		{"deny", "--", "acme", "../gate-1"},
		{"answer", "--", "acme", "-q", "x"},
	} {
		if _, err := runOrgChat(t, cfg, args...); exitCodeFor(err) != 3 {
			t.Errorf("%v: %v, want exit 3", args, err)
		}
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sent %v", fake.sent)
	}
	// The app's own shape: "--" first, then org, ref and the text.
	out, err := runOrgChat(t, cfg, "answer", "--", "acme", "q-2250-cd34", "yes", "--", "all")
	if err != nil || !strings.Contains(out, `"state":"answered"`) {
		t.Fatalf("answer after --: %v %s", err, out)
	}
	if fake.sent[0] != "answer q-2250-cd34 yes -- all" {
		t.Fatalf("sent %q", fake.sent)
	}
	out, err = runOrgChat(t, cfg, "approve", "--", "acme", "gate-1850-x1", "ship")
	if err != nil || !strings.Contains(out, `"state":"approved"`) || fake.sent[1] != "gate gate-1850-x1 ship" {
		t.Fatalf("approve after --: %v %s %v", err, out, fake.sent)
	}
}

func TestOrgChatHistoryCarriesACrashReason(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	fake.status = "crashed"
	out, err := runOrgChat(t, cfg, "history", "acme")
	if err != nil || !strings.Contains(out, `"status":"crashed"`) || !strings.Contains(out, `"status_error":"pid 4242 gone"`) {
		t.Fatalf("history of a crashed run: %v %s", err, out)
	}
}

func TestOrgChatDismiss(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	out, err := runOrgChat(t, cfg, "dismiss", "acme", "q-2250-cd34", "--reason", "moot")
	if err != nil || !strings.Contains(out, `"already":false`) || !strings.Contains(out, `"state":"dismissed"`) {
		t.Fatalf("dismiss: %v %s", err, out)
	}
	out, err = runOrgChat(t, cfg, "dismiss", "acme", "q-2250-cd34")
	if err != nil || !strings.Contains(out, `"already":true`) {
		t.Fatalf("second dismiss: %v %s", err, out)
	}
	if len(fake.sent) != 1 || fake.sent[0] != "dismiss q-2250-cd34 moot" {
		t.Fatalf("sent %v", fake.sent)
	}
	if _, err := runOrgChat(t, cfg, "dismiss", "acme", "q-nope"); exitCodeFor(err) != 2 {
		t.Errorf("unknown question: %v", err)
	}
	// A stopped org still takes a dismissal: monomind queues the note.
	fake.status = "stopped"
	out, err = runOrgChat(t, cfg, "dismiss", "acme", "q-0900-zz99")
	if err != nil || !strings.Contains(out, `"state":"dismissed"`) {
		t.Errorf("stopped org dismiss: %v %s", err, out)
	}
}
