package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
)

var fakePNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake")

// fakeBrowser stands in for the `browser …` commands: it records each argv
// and writes what the real command would at --out.
type fakeBrowser struct {
	calls   [][]string
	fail    string // a subcommand that fails
	console string // what `browser console` prints (default: no entries)
	tabs    string // what `browser tabs` prints (default: fakeTabs)
}

// fakeTabs is the browser's view of its tabs (chrome.tabs): tab 7 is active.
const fakeTabs = `{"browser":"edge-1","tabs":[` +
	`{"tabId":9,"url":"https://example.com/","title":"Example","active":false},` +
	`{"tabId":7,"url":"http://localhost:3000/pricing?token=secret#plans","title":"Pricing\n\tplans","active":true}]}`

func (f *fakeBrowser) Run(_ context.Context, args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	sub := ""
	for i, a := range args {
		if a == "browser" && i+1 < len(args) {
			sub = args[i+1]
			break
		}
	}
	if sub == f.fail {
		return nil, errors.New("no extension connected")
	}
	out := ""
	for i, a := range args {
		if a == "--out" && i+1 < len(args) {
			out = args[i+1]
		}
	}
	switch sub {
	case "tabs":
		if f.tabs != "" {
			return []byte(f.tabs), nil
		}
		return []byte(fakeTabs), nil
	case "eval":
		// What a hostile page's own script would claim; never asked.
		return []byte(`{"result":"{\"url\":\"http://localhost:3000/admin\",\"title\":\"spoofed\"}"}`), nil
	case "screenshot", "tokens":
		return []byte(`{"path":"` + out + `"}`), os.WriteFile(out, fakePNG, 0o600)
	case "styles":
		if err := os.MkdirAll(out, 0o700); err != nil {
			return nil, err
		}
		_ = os.WriteFile(filepath.Join(out, "bundle.json"), []byte(`{"selector":".cta"}`), 0o600)
		return []byte(`{"dir":"` + out + `"}`), os.WriteFile(filepath.Join(out, "element.png"), fakePNG, 0o600)
	}
	if sub == "console" && f.console != "" {
		return []byte(f.console), nil
	}
	return []byte(`{"entries":[],"next":0}`), nil
}

func withFakeBrowser(t *testing.T) *fakeBrowser {
	t.Helper()
	f := &fakeBrowser{}
	old := queueRunner
	queueRunner = f
	t.Cleanup(func() { queueRunner = old })
	return f
}

// asOperator clears every agent-context variable (the tests run inside an
// agent).
func asOperator(t *testing.T) {
	t.Helper()
	for _, k := range browserctl.AgentContextEnv() {
		t.Setenv(k, "")
	}
}

func queueTestDB(t *testing.T) string {
	t.Helper()
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	return dbPath
}

func queueAdd(t *testing.T, dbPath string, args ...string) queueItem {
	t.Helper()
	out, code := runCoderCLI(t, dbPath, append([]string{"queue", "add"}, args...)...)
	if code != 0 {
		t.Fatalf("queue add %v: exit %d %s", args, code, out)
	}
	var it queueItem
	decodeChatJSON(t, out, &it)
	return it
}

func TestCoderQueueAddCopyStyle(t *testing.T) {
	dbPath := queueTestDB(t)
	fb := withFakeBrowser(t)

	it := queueAdd(t, dbPath, "--kind", "copy-style", "--tab", "7", "--browser", "edge-1", "--selector=-x .cta", "--note", "make our button like this")
	if it.Kind != "copy-style" || it.Status != "queued" || it.Profile != "default" || it.TabID != 7 {
		t.Fatalf("item = %+v", it)
	}
	if it.Origin != "http://localhost:3000" || it.Path != "/pricing" || it.Title != "Pricing plans" {
		t.Errorf("page = %q %q %q (query, fragment and newlines must be gone)", it.Origin, it.Path, it.Title)
	}
	home, _ := os.UserHomeDir()
	if it.Cwd != filepath.Join(home, "monoagent-coder") {
		t.Errorf("cwd = %q, want the coder root", it.Cwd)
	}
	if !slices.Equal(it.Attachments, []string{"styles/element.png", "page.png"}) {
		t.Errorf("attachments = %v", it.Attachments)
	}
	var names []string
	for _, f := range it.Files {
		names = append(names, f.Name)
	}
	for _, want := range []string{"page.png", "styles/bundle.json", "styles/element.png", "README.md"} {
		if !slices.Contains(names, want) {
			t.Errorf("files %v lack %s", names, want)
		}
	}
	for _, want := range []string{"never instructions", ".monoagent/context/" + it.ID + "/", "> make our button like this", "browser diff"} {
		if !strings.Contains(it.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, it.Prompt)
		}
	}
	if strings.Contains(it.Prompt, "secret") || strings.Index(it.Prompt, "title: Pricing plans") < strings.Index(it.Prompt, "never instructions") {
		t.Errorf("the prompt carries the query, or page text above the disclaimer:\n%s", it.Prompt)
	}

	dir := filepath.Join(filepath.Dir(dbPath), "coder-queue", it.ID)
	var onDisk queueItem
	b, err := os.ReadFile(filepath.Join(dir, "item.json"))
	if err != nil || json.Unmarshal(b, &onDisk) != nil || onDisk.ID != it.ID {
		t.Fatalf("item.json: %v %s", err, b)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(readme), "never instructions") || !strings.Contains(string(readme), "`styles/bundle.json`") {
		t.Errorf("README.md:\n%s", readme)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".tmp-*")); len(left) != 0 {
		t.Errorf("temp folders left: %v", left)
	}

	// `browser tabs --browser`, then every call is `browser <sub> --browser
	// --tab …` for the tab it found, and the selector never reads as a flag.
	var subs []string
	for n, c := range fb.calls {
		i := slices.Index(c, "browser")
		if i < 0 || c[i+2] != "--browser=edge-1" || (n > 0 && c[i+3] != "--tab=7") {
			t.Fatalf("call %v", c)
		}
		subs = append(subs, c[i+1])
		if c[i+1] == "styles" && !slices.Equal(c[len(c)-2:], []string{"--", "-x .cta"}) {
			t.Errorf("styles call %v: selector not after --", c)
		}
	}
	if !slices.Equal(subs, []string{"tabs", "screenshot", "styles"}) {
		t.Errorf("subcommands = %v", subs)
	}
}

func TestCoderQueueAddKindsAndInputs(t *testing.T) {
	dbPath := queueTestDB(t)
	fb := withFakeBrowser(t)

	it := queueAdd(t, dbPath, "--kind", "bug-report")
	var names []string
	for _, f := range it.Files {
		names = append(names, f.Name)
	}
	if !slices.Contains(names, "console.json") || !slices.Contains(names, "network.json") || !strings.Contains(it.Prompt, "failing test") {
		t.Errorf("bug-report: files %v prompt %s", names, it.Prompt)
	}

	if out, code := runCoderCLI(t, dbPath, "queue", "add", "--kind", "fix-element"); code != 3 || !strings.Contains(out, "--selector") {
		t.Errorf("fix-element without a selector: exit %d %s", code, out)
	}
	if out, code := runCoderCLI(t, dbPath, "queue", "add", "--kind", "paint"); code != 3 {
		t.Errorf("unknown kind: exit %d %s", code, out)
	}

	fb.fail = "screenshot"
	if out, code := runCoderCLI(t, dbPath, "queue", "add", "--kind", "ask"); code == 0 || !strings.Contains(out, "no extension connected") {
		t.Errorf("failed screenshot: exit %d %s", code, out)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "coder-queue", ".tmp-*")); len(left) != 0 {
		t.Errorf("a failed add left %v", left)
	}

	setCoderSettings(t, dbPath, coderSettings{})
	if out, code := runCoderCLI(t, dbPath, "queue", "add", "--kind", "ask"); code != 3 || !strings.Contains(out, "coder_disabled") {
		t.Errorf("coder mode off: exit %d %s", code, out)
	}
}

// --stdin-json is the bridge's way: the selector and note never ride argv.
func TestCoderQueueAddStdinJSON(t *testing.T) {
	dbPath := queueTestDB(t)
	withFakeBrowser(t)
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}))
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(`{"selector":"#hero h1","note":"why is this red?"}`))
	cmd.SetArgs([]string{"queue", "add", "--kind", "ask", "--stdin-json"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("add: %v %s", err, out.String())
	}
	var it queueItem
	decodeChatJSON(t, out.String(), &it)
	if it.Selector != "#hero h1" || it.Note != "why is this red?" || !slices.Contains(it.Attachments, "element.png") {
		t.Fatalf("item = %+v", it)
	}
}

// The gate is the CLI's operator check (Deps.RequireOperator): queueing and starting a coder chat ask it, so
// whatever it refuses (a marked agent here) is refused with operator_only, before the verb does anything.
func TestCoderQueueRefusedWhereTheOperatorGateRefuses(t *testing.T) {
	dbPath := queueTestDB(t)
	fb := withFakeBrowser(t)
	it := queueAdd(t, dbPath, "--kind", "tokens")

	for _, marker := range []string{"CLAUDECODE", "MONOAGENT_AGENT_CONTEXT", "MONOAGENT_ACTOR"} {
		t.Run(marker, func(t *testing.T) {
			t.Setenv(marker, "1")
			for _, args := range [][]string{{"queue", "add", "--kind", "tokens"}, {"queue", "start", it.ID}} {
				out, code := runCoderCLIGated(t, dbPath, args...)
				if code != 3 || !strings.Contains(out, `"code":"operator_only"`) || !strings.Contains(out, "only the user can") || !strings.Contains(out, marker) {
					t.Errorf("%v with %s set: exit %d %s", args, marker, code, out)
				}
			}
		})
	}
	if len(fb.calls) != 3 { // the first add's eval, screenshot, tokens
		t.Errorf("the refused add still ran %d browser commands", len(fb.calls)-3)
	}
}

// A gate that is not wired refuses (clicore.RequireOperator), so the queue verbs never run unguarded.
func TestCoderQueueRefusesWhenTheOperatorGateIsNotWired(t *testing.T) {
	dbPath := queueTestDB(t)
	deps := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	deps.OperatorRefusal = nil
	for _, args := range [][]string{{"queue", "add", "--kind", "tokens"}, {"queue", "start", "q1"}, {"projects", "unmap", "http://localhost:3000"}} {
		cmd := newCoderCmd(deps)
		cmd.SetOut(&strings.Builder{})
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		if code := exitCodeFor(cmd.Execute()); code != 3 {
			t.Errorf("%v with no gate wired: exit %d", args, code)
		}
	}
}

func TestWithoutAgentMarkers(t *testing.T) {
	env := withoutAgentMarkers([]string{"HOME=/h", "CLAUDECODE=1", "MONOMIND_AGENT_EXEC=1", "PATH=/bin", "AGENT=x",
		"MONOAGENT_AGENT_CONTEXT=coder", "MONOAGENT_ACTOR=w1"})
	if !slices.Equal(env, []string{"HOME=/h", "PATH=/bin"}) {
		t.Fatalf("env = %v", env)
	}
}

func TestCoderQueueStartCreatesTheConversation(t *testing.T) {
	dbPath := queueTestDB(t)
	writeCoderMonomind(t, "")
	withCoderCaps(t, monomind.CoderCapabilities...)
	withFakeBrowser(t)
	project := t.TempDir()
	it := queueAdd(t, dbPath, "--kind", "copy-style", "--selector", ".cta", "--cwd", project)
	if it.Cwd != project {
		t.Fatalf("cwd = %q, want %q", it.Cwd, project)
	}

	out, code := runCoderCLI(t, dbPath, "queue", "start", it.ID)
	if code != 0 {
		t.Fatalf("start: exit %d %s", code, out)
	}
	var res queueStartResult
	decodeChatJSON(t, out, &res)
	if res.ConversationID == "" || !isDesktopInstance(res.TurnID) || res.Prompt != it.Prompt || res.Cwd != project || res.Runtime != "claude" {
		t.Fatalf("start = %+v", res)
	}
	ctxDir := filepath.Join(project, ".monoagent", "context", it.ID)
	if !slices.Equal(res.Attachments, []string{filepath.Join(ctxDir, "styles", "element.png"), filepath.Join(ctxDir, "page.png")}) {
		t.Errorf("attachments = %v", res.Attachments)
	}
	for _, f := range []string{"page.png", "styles/bundle.json", "README.md"} {
		if _, err := os.Stat(filepath.Join(ctxDir, filepath.FromSlash(f))); err != nil {
			t.Errorf("context lacks %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(ctxDir, "item.json")); err == nil {
		t.Error("item.json was copied into the project")
	}
	if b, _ := os.ReadFile(filepath.Join(project, ".monoagent", "context", ".gitignore")); !strings.Contains(string(b), "*") {
		t.Errorf(".gitignore = %q", b)
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := ai.NewAIStore(db.DB)
	conv, err := store.GetConversation(res.ConversationID, "default")
	db.Close()
	if err != nil || conv.Mode != ai.ModeCoder || conv.Cwd != project {
		t.Fatalf("conversation = %+v %v", conv, err)
	}

	// Started: off the default list, and a second start is the same start.
	out, _ = runCoderCLI(t, dbPath, "queue", "list")
	if strings.Contains(out, it.ID) {
		t.Errorf("a started item is still listed: %s", out)
	}
	out, _ = runCoderCLI(t, dbPath, "queue", "list", "--all")
	if !strings.Contains(out, `"status":"started"`) || !strings.Contains(out, res.ConversationID) {
		t.Errorf("list --all: %s", out)
	}
	out, code = runCoderCLI(t, dbPath, "queue", "start", it.ID)
	var again queueStartResult
	decodeChatJSON(t, out, &again)
	if code != 0 || !again.Already || again.ConversationID != res.ConversationID || again.TurnID != res.TurnID {
		t.Errorf("second start: exit %d %+v", code, again)
	}
}

func TestCoderQueueListShowImageDismiss(t *testing.T) {
	dbPath := queueTestDB(t)
	withFakeBrowser(t)
	a := queueAdd(t, dbPath, "--kind", "tokens")
	b := queueAdd(t, dbPath, "--kind", "integration")

	out, _ := runCoderCLI(t, dbPath, "queue", "list")
	var list struct{ Items []queueItem }
	decodeChatJSON(t, out, &list)
	if len(list.Items) != 2 {
		t.Fatalf("list = %s", out)
	}
	if out, code := runCoderCLI(t, dbPath, "queue", "show", a.ID); code != 0 || !strings.Contains(out, `"kind":"tokens"`) {
		t.Errorf("show: exit %d %s", code, out)
	}
	out, code := runCoderCLI(t, dbPath, "queue", "image", a.ID)
	if code != 0 || !strings.Contains(out, `"dataUrl":"data:image/png;base64,`) {
		t.Errorf("image: exit %d %s", code, out)
	}
	for _, name := range []string{"../../x.png", "/etc/passwd", "tokens.json/../../" + b.ID + "/page.png"} {
		if out, code := runCoderCLI(t, dbPath, "queue", "image", a.ID, name); code == 0 {
			t.Errorf("image %s escaped: %s", name, out)
		}
	}
	if _, code := runCoderCLI(t, dbPath, "queue", "show", "../"+a.ID); code != 3 {
		t.Errorf("a path as an id: exit %d", code)
	}

	if out, code := runCoderCLI(t, dbPath, "queue", "dismiss", a.ID); code != 0 || !strings.Contains(out, `"dismissed":true`) {
		t.Fatalf("dismiss: exit %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), "coder-queue", a.ID)); !os.IsNotExist(err) {
		t.Errorf("dismissed folder still there: %v", err)
	}
	if _, code := runCoderCLI(t, dbPath, "queue", "show", a.ID); code != 2 {
		t.Errorf("show after dismiss: exit %d", code)
	}
}

func TestCoderProjectsMapUnmap(t *testing.T) {
	dbPath := queueTestDB(t)
	withFakeBrowser(t)
	project := t.TempDir()

	if out, code := runCoderCLI(t, dbPath, "projects", "map", "ftp://x", project); code != 3 {
		t.Errorf("non-http origin: exit %d %s", code, out)
	}
	if out, code := runCoderCLI(t, dbPath, "projects", "map", "http://x", filepath.Join(project, "missing")); code != 3 {
		t.Errorf("missing folder: exit %d %s", code, out)
	}
	out, code := runCoderCLI(t, dbPath, "projects", "map", "HTTP://LocalHost:3000/some/page?q=1", project)
	var l coderProjectsListing
	decodeChatJSON(t, out, &l)
	if code != 0 || len(l.Projects) != 1 || l.Projects[0].Origin != "http://localhost:3000" || l.Projects[0].Dir != project || !l.Projects[0].Exists {
		t.Fatalf("map: exit %d %s", code, out)
	}

	// The page's origin picks the folder.
	if it := queueAdd(t, dbPath, "--kind", "tokens"); it.Cwd != project {
		t.Errorf("queued for %q, want the mapped %q", it.Cwd, project)
	}

	out, code = runCoderCLI(t, dbPath, "projects", "unmap", "http://localhost:3000")
	decodeChatJSON(t, out, &l)
	if code != 0 || len(l.Projects) != 0 || l.Recent == nil {
		t.Fatalf("unmap: exit %d %s", code, out)
	}
	if _, code := runCoderCLI(t, dbPath, "projects", "unmap", "http://localhost:3000"); code != 2 {
		t.Errorf("unmap twice: exit %d", code)
	}
}

// Only the app's instance ids (UUIDs) count as the desktop: the bridge's
// ext-… ids and a terminal's empty one never do.
func TestIsDesktopInstance(t *testing.T) {
	for owner, want := range map[string]bool{
		"7a1c3f9e-2b4d-4e8f-9a0b-1c2d3e4f5a6b": true,
		"":                                     false,
		"ext-6ba121542143":                     false,
		"ext-7a1c3f9e-2b4d-4e8f-9a0b-1c2d3e4f5a6b":      false,
		"{7a1c3f9e-2b4d-4e8f-9a0b-1c2d3e4f5a6b}":        false,
		"urn:uuid:7a1c3f9e-2b4d-4e8f-9a0b-1c2d3e4f5a6b": false,
		"7a1c3f9e2b4d4e8f9a0b1c2d3e4f5a6b":              false,
	} {
		if got := isDesktopInstance(owner); got != want {
			t.Errorf("isDesktopInstance(%q) = %v, want %v", owner, got, want)
		}
	}
}

func TestCoderListStartedInDesktop(t *testing.T) {
	dbPath := queueTestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := ai.NewAIStore(db.DB)
	mk := func(owner, status string) string {
		conv, err := store.CreateConversationModeEffort("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		turn, _, err := store.CreateTurn(conv.ID, "default", "t-"+conv.ID[:8], owner, "Fix the header\nmore")
		if err != nil {
			t.Fatal(err)
		}
		if status != "active" {
			if _, _, err := store.FinalizeTurn("default", conv.ID, turn.ID, "completed", "", nil, false); err != nil {
				t.Fatal(err)
			}
		}
		return conv.ID
	}
	desktop := mk("7a1c3f9e-2b4d-4e8f-9a0b-1c2d3e4f5a6b", "completed")
	ext := mk("ext-abcdef123456", "completed")
	running := mk("7a1c3f9e-2b4d-4e8f-9a0b-1c2d3e4f5a6c", "active")
	if _, err := store.CreateConversationModeEffort("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, code := runCoderCLI(t, dbPath, "list")
	var res struct{ Conversations []coderChatRow }
	decodeChatJSON(t, out, &res)
	if code != 0 || len(res.Conversations) != 3 {
		t.Fatalf("list (the turnless one left out): exit %d %s", code, out)
	}
	want := map[string][2]bool{desktop: {true, false}, ext: {false, false}, running: {false, true}}
	for _, r := range res.Conversations {
		if w := want[r.ID]; r.StartedInDesktop != w[0] || r.Running != w[1] || r.Title != "Fix the header" {
			t.Errorf("%s = %+v, want desktop=%v running=%v", r.ID, r, w[0], w[1])
		}
	}
	out, code = runCoderCLI(t, dbPath, "list", "--conversation", desktop)
	if code != 0 || !strings.Contains(out, `"startedInDesktop":true`) {
		t.Errorf("--conversation: exit %d %s", code, out)
	}
	if _, code := runCoderCLI(t, dbPath, "list", "--conversation", "nope"); code != 2 {
		t.Errorf("unknown conversation: exit %d", code)
	}
}
