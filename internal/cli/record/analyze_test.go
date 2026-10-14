package record

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/recording"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

const recordAnalyzeAnswer = `{
  "automation": {"id": "example-go", "name": "Example Go"},
  "action": {
    "actionType": "press_go",
    "description": "Press the Go button",
    "sideEffects": "write",
    "inputs": {"required": [{"name": "label", "type": "string"}]},
    "steps": [
      {"id": "open", "type": "navigate", "url": "https://example.com/rec1"},
      {"id": "note", "type": "log", "text": "pressing {{label}}"},
      {"id": "go", "type": "click", "configKey": "page.go_button", "intent": "the Go button", "sideEffect": true}
    ]
  },
  "selectors": {"page.go_button": {"candidates": [{"css": "button", "score": 0.8}, {"text": "Go", "score": 0.6}]}},
  "names": {"automation": "example-go", "action": "press_go", "fragment": "press_go"}
}`

// stubRecordAI makes analyze answer with canned JSON instead of monomind.
func stubRecordAI(t *testing.T, answers ...string) *int {
	t.Helper()
	calls := 0
	prev := seams.AnalyzeRunner
	seams.AnalyzeRunner = func(string, string, time.Duration) recordanalyze.Runner {
		return recordanalyze.RunnerFunc(func(context.Context, string) (string, error) {
			calls++
			return answers[min(calls, len(answers))-1], nil
		})
	}
	t.Cleanup(func() { seams.AnalyzeRunner = prev })
	return &calls
}

func analyzeRec1(t *testing.T) string {
	t.Helper()
	recordTestHome(t, "rec1")
	stubRecordAI(t, recordAnalyzeAnswer)
	out, err := runRecordCLI(t, true, "analyze", "rec1")
	if err != nil {
		t.Fatalf("analyze: %v\n%s", err, out)
	}
	var res struct {
		DraftDir string                  `json:"draftDir"`
		Draft    recordanalyze.DraftView `json:"draft"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("analyze JSON: %v\n%s", err, out)
	}
	if res.Draft.Action != "press_go" || !res.Draft.IsNew || res.Draft.TargetAutomation != "example-go" {
		t.Fatalf("draft = %+v", res.Draft)
	}
	if res.Draft.ActionDef == nil || len(res.Draft.ActionDef.Steps) != 3 || len(res.Draft.Inputs) != 1 || len(res.Draft.Selectors) != 1 {
		t.Errorf("draft view = %+v", res.Draft)
	}
	for _, is := range res.Draft.Lint {
		if is.Severity == "error" {
			t.Errorf("lint error: %+v", is)
		}
	}
	return res.DraftDir
}

func TestRecordAnalyzeJSON(t *testing.T) {
	dir := analyzeRec1(t)
	if !strings.HasSuffix(dir, "/.monoagent/recording-drafts/"+filepathBase(dir)) {
		t.Errorf("draft dir = %s", dir)
	}
}

func filepathBase(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func TestRecordAnalyzeErrorsAsJSON(t *testing.T) {
	recordTestHome(t)
	out, err := runRecordCLI(t, true, "analyze", "nope")
	if err == nil {
		t.Fatal("unknown recording accepted")
	}
	var body map[string]any
	if json.Unmarshal([]byte(out), &body) != nil || body["error"] == nil {
		t.Errorf("want {\"error\"} on stdout, got %q", out)
	}
}

func TestRecordAnalyzeVerifyNoBridge(t *testing.T) {
	dir := analyzeRec1(t)
	prev := seams.VerifyExec
	seams.VerifyExec = func(context.Context, string, bool, bool, func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
		return nil, errors.New("browser bridge not connected")
	}
	t.Cleanup(func() { seams.VerifyExec = prev })
	out, err := runRecordCLI(t, true, "verify", dir)
	if err == nil || !strings.Contains(out, `"error":"browser bridge not connected"`) {
		t.Errorf("err %v out %s", err, out)
	}
}

func TestRecordAnalyzeVerifySafeStop(t *testing.T) {
	dir := analyzeRec1(t)
	prev := seams.VerifyExec
	seams.VerifyExec = func(_ context.Context, id string, _, _ bool, _ func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
		if id != "example-go" {
			t.Errorf("automation = %s", id)
		}
		return func(_ context.Context, def *action.ActionDef, _ action.PackageContext, _ map[string]any, safe bool, _ action.SelectorObserver) recordanalyze.RunOutcome {
			if !safe {
				t.Error("verify without --full must be safe")
			}
			return recordanalyze.RunOutcome{
				Events:   []action.ExecutionEvent{{Type: "step_start", StepID: "open"}, {Type: "step_complete", StepID: "open"}},
				Result:   &action.ExecutionResult{},
				SafeStop: &action.SafeStop{StepID: "go", Type: "click"},
			}
		}, nil
	}
	t.Cleanup(func() { seams.VerifyExec = prev })
	out, err := runRecordCLI(t, true, "verify", dir)
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	var rep recordanalyze.VerifyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.StoppedAt == nil || len(rep.Steps) != 3 || rep.Steps[2].Status != recordanalyze.StatusStopped {
		t.Errorf("report = %+v", rep)
	}
	if _, err := runRecordCLI(t, true, "verify", "/etc"); err == nil {
		t.Error("a path outside recording-drafts was accepted")
	}
}

func TestRecordAnalyzeSaveActionAndWorkflow(t *testing.T) {
	dir := analyzeRec1(t)
	out, err := runRecordCLI(t, true, "save", dir)
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	var res recordanalyze.SaveResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Automation != "example-go" || res.NodeType != "example-go.press_go" || res.Version == "" || len(res.Warnings) != 0 {
		t.Errorf("save = %+v", res)
	}
	list, err := recording.List()
	if err != nil || len(list) != 1 || list[0].Automation != "example-go" {
		t.Errorf("recording link: %+v %v", list, err)
	}

	dbPath := filepath.Join(t.TempDir(), "wf.db")
	cmd := NewCommand(testDeps(true, dbPath))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"save", dir, "--as", "workflow", "--name", "press go flow"})
	err = cmd.Execute()
	out = buf.String()
	if err != nil {
		t.Fatalf("save workflow: %v\n%s", err, out)
	}
	res = recordanalyze.SaveResult{}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.WorkflowID == "" || res.NodeType != "example-go.press_go_flow" {
		t.Errorf("workflow save = %+v", res)
	}
	// `workflow run --json` reads node types from the SQLite store (N4).
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wf, err := workflow.NewSQLiteWorkflowStore(db.DB).GetWorkflow(context.Background(), res.WorkflowID)
	if err != nil || wf == nil || len(wf.Nodes) != 2 {
		t.Fatalf("SQLite workflow = %+v, %v", wf, err)
	}
	types := map[string]bool{}
	for _, n := range wf.Nodes {
		types[n.Type] = true
	}
	if !types["trigger.manual"] || !types["example-go.press_go_flow"] || len(wf.Connections) != 1 {
		t.Errorf("SQLite node types = %v, connections = %d", types, len(wf.Connections))
	}
}

func TestRecordAnalyzeBuildWorkflow(t *testing.T) {
	wf := buildRecordedWorkflow("n", "d", "p1", []recordanalyze.WorkflowNode{{Name: "a", Type: "x.a"}, {Name: "b", Type: "x.b"}})
	if len(wf.Nodes) != 3 || wf.Nodes[0].Type != "trigger.manual" || len(wf.Connections) != 2 {
		t.Fatalf("wf = %+v", wf)
	}
	if wf.Connections[1].SourceNodeID != wf.Nodes[1].ID || wf.Connections[1].TargetNodeID != wf.Nodes[2].ID || wf.ProfileID != "p1" {
		t.Errorf("wiring = %+v", wf.Connections)
	}
}

func TestRecordAnalyzeSaveRenameInput(t *testing.T) {
	dir := analyzeRec1(t)
	out, err := runRecordCLI(t, true, "save", dir, "--new", "renamed-go", "--name", "Press Go", "--rename-input", "label=button_label")
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	reg, err := openRegistry()
	if err != nil {
		t.Fatal(err)
	}
	p, err := reg.Get("renamed-go")
	if err != nil {
		t.Fatal(err)
	}
	def, err := p.Action("press_go")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(def)
	if !strings.Contains(string(b), `"name":"button_label"`) || !strings.Contains(string(b), "{{button_label}}") {
		t.Errorf("rename not applied: %s", b)
	}
	if _, err := runRecordCLI(t, true, "save", dir, "--rename-input", "label"); err == nil {
		t.Error("malformed --rename-input accepted")
	}
}

func TestRecordAnalyzeVerifyInputsFile(t *testing.T) {
	dir := analyzeRec1(t)
	var got map[string]any
	prev := seams.VerifyExec
	seams.VerifyExec = func(context.Context, string, bool, bool, func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
		return func(_ context.Context, _ *action.ActionDef, _ action.PackageContext, in map[string]any, _ bool, _ action.SelectorObserver) recordanalyze.RunOutcome {
			got = in
			return recordanalyze.RunOutcome{Result: &action.ExecutionResult{}, Err: errors.New("login with s3cr3t-pw failed")}
		}, nil
	}
	t.Cleanup(func() { seams.VerifyExec = prev })
	file := filepath.Join(t.TempDir(), "inputs.json")
	if err := os.WriteFile(file, []byte(`{"label":"from-file","pw":"s3cr3t-pw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runRecordCLI(t, true, "verify", dir, "--inputs-file", file, "--input", "label=from-flag")
	failedVerifyReport(t, out, err)
	if got["label"] != "from-flag" || got["pw"] != "s3cr3t-pw" {
		t.Errorf("merged inputs = %v", got)
	}
	if strings.Contains(out, "s3cr3t-pw") || strings.Contains(out, "from-flag") {
		t.Errorf("output echoes a value: %s", out)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runRecordCLI(t, true, "verify", dir, "--inputs-file", file)
	if err == nil || !strings.Contains(out, "0600") || strings.Contains(out, "s3cr3t-pw") {
		t.Errorf("world-readable inputs file: err %v out %s", err, out)
	}
}

func TestRecordAnalyzeVerifyVaultLookup(t *testing.T) {
	recordTestHome(t, "rec1")
	stubRecordAI(t, strings.Replace(recordAnalyzeAnswer, `{"name": "label", "type": "string"}`, `{"name": "pw", "type": "secret"}`, 1))
	out, err := runRecordCLI(t, true, "analyze", "rec1")
	if err != nil {
		t.Fatalf("analyze: %v\n%s", err, out)
	}
	var res struct {
		DraftDir string `json:"draftDir"`
	}
	_ = json.Unmarshal([]byte(out), &res)

	var askedFor string
	prevL := seams.SecretLookup
	seams.SecretLookup = func(_ context.Context, id string) (func(string) (string, bool), func()) {
		askedFor = id
		return func(name string) (string, bool) { return "vault-" + name, name == "pw" }, func() {}
	}
	var got map[string]any
	var gotLookup bool
	prevE := seams.VerifyExec
	seams.VerifyExec = func(_ context.Context, _ string, _, _ bool, secrets func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
		gotLookup = secrets != nil
		return func(_ context.Context, _ *action.ActionDef, _ action.PackageContext, in map[string]any, _ bool, _ action.SelectorObserver) recordanalyze.RunOutcome {
			got = in
			return recordanalyze.RunOutcome{Result: &action.ExecutionResult{}, Err: errors.New("pw vault-pw rejected")}
		}, nil
	}
	t.Cleanup(func() { seams.SecretLookup, seams.VerifyExec = prevL, prevE })

	out, err = runRecordCLI(t, true, "verify", res.DraftDir)
	failedVerifyReport(t, out, err)
	if askedFor != "example-go" || !gotLookup || got["pw"] != "vault-pw" || strings.Contains(out, "vault-pw") {
		t.Errorf("askedFor=%q lookup=%v inputs=%v out=%s", askedFor, gotLookup, got, out)
	}
	out, err = runRecordCLI(t, true, "verify", res.DraftDir, "--input", "pw=flag-pw")
	failedVerifyReport(t, out, err)
	if got["pw"] != "flag-pw" {
		t.Errorf("--input must win: %v", got)
	}
}

// failedVerifyReport: a failed replay exits non-zero but stdout is still
// exactly one JSON report, with ok false (no trailing {"error"}).
func failedVerifyReport(t *testing.T, out string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("a failed verify exited 0: %s", out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var rep recordanalyze.VerifyReport
	if derr := dec.Decode(&rep); derr != nil || rep.OK {
		t.Fatalf("stdout is not a failed report: %v\n%s", derr, out)
	}
	if dec.More() {
		t.Fatalf("more than one JSON document: %s", out)
	}
}

// TestRecordAnalyzeDefaultSecretLookup: the production lookup opens the
// profile database and answers "not found" for a secret the vault lacks.
func TestRecordAnalyzeDefaultSecretLookup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := testDeps(false, filepath.Join(t.TempDir(), "vault.db"))
	lookup, release := d.VaultSecretLookup(context.Background(), "example-go")
	defer release()
	if lookup == nil {
		t.Fatal("no lookup for an openable database")
	}
	if v, ok := lookup("pw"); ok || v != "" {
		t.Errorf("missing secret resolved: ok=%v", ok)
	}
}

func TestRecordAnalyzeAllowAdvancedFlag(t *testing.T) {
	recordTestHome(t, "rec1")
	withScript := strings.Replace(recordAnalyzeAnswer, `"steps": [`, `"steps": [{"id": "js", "type": "page_script", "script": "x.js"},`, 1)
	withScript = strings.Replace(withScript, `"names":`, `"scripts": {"x.js": "return 1"}, "names":`, 1)
	stubRecordAI(t, withScript)
	out, err := runRecordCLI(t, true, "analyze", "rec1")
	if err == nil || !strings.Contains(out, "allow-advanced") {
		t.Errorf("page_script accepted without the flag: %v %s", err, out)
	}
	out, err = runRecordCLI(t, true, "analyze", "rec1", "--allow-advanced")
	if err != nil || !strings.Contains(out, `"allowAdvanced": true`) || !strings.Contains(out, `"x.js": "return 1"`) {
		t.Errorf("with flag: %v %s", err, out)
	}
}

func TestRecordAnalyzeRelativeDraftPathAndKeepOpen(t *testing.T) {
	dir := analyzeRec1(t)
	var keep bool
	prev := seams.VerifyExec
	seams.VerifyExec = func(_ context.Context, _ string, _, keepOpen bool, _ func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
		keep = keepOpen
		return func(context.Context, *action.ActionDef, action.PackageContext, map[string]any, bool, action.SelectorObserver) recordanalyze.RunOutcome {
			return recordanalyze.RunOutcome{Result: &action.ExecutionResult{}}
		}, nil
	}
	t.Cleanup(func() { seams.VerifyExec = prev })
	t.Chdir(filepath.Dir(dir))
	rel := filepath.Join(".", filepath.Base(dir))
	if out, err := runRecordCLI(t, true, "verify", "./"+filepath.Base(dir), "--keep-open"); err != nil || !keep {
		t.Errorf("relative %s: %v %s keep=%v", rel, err, out, keep)
	}
	t.Chdir(t.TempDir())
	if _, err := runRecordCLI(t, true, "verify", "../../x"); err == nil {
		t.Error("relative path outside recording-drafts accepted")
	}
}

func TestRecordAnalyzeSaveForce(t *testing.T) {
	recordTestHome(t, "rec1")
	stubRecordAI(t, strings.Replace(recordAnalyzeAnswer, `"url": "https://example.com/rec1"`, `"url": "https://example.com/rec1?token=abc"`, 1))
	out, err := runRecordCLI(t, true, "analyze", "rec1")
	if err != nil {
		t.Fatalf("analyze: %v %s", err, out)
	}
	var res struct {
		DraftDir string `json:"draftDir"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	out, err = runRecordCLI(t, true, "save", res.DraftDir)
	if err == nil || !strings.Contains(out, "--force") {
		t.Errorf("lint errors not refused: %v %s", err, out)
	}
	if out, err = runRecordCLI(t, true, "save", res.DraftDir, "--force"); err != nil && strings.Contains(out, "lint error") {
		t.Errorf("--force ignored: %s", out)
	}
}

func TestRecordAnalyzeSaveKeepPackageSelectors(t *testing.T) {
	dir := analyzeRec1(t)
	if out, err := runRecordCLI(t, true, "save", dir); err != nil {
		t.Fatalf("first save: %v %s", err, out)
	}
	// An older draft of the same page, with a different selector for the key.
	stubRecordAI(t, strings.Replace(recordAnalyzeAnswer, `{"css": "button", "score": 0.8}`, `{"css": "body button", "score": 0.8}`, 1))
	out, err := runRecordCLI(t, true, "analyze", "rec1")
	if err != nil {
		t.Fatalf("analyze: %v %s", err, out)
	}
	var res struct {
		DraftDir string `json:"draftDir"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	out, err = runRecordCLI(t, true, "save", res.DraftDir, "--automation", "example-go", "--name", "press go again")
	if err == nil || !strings.Contains(out, "page.go_button") || !strings.Contains(out, "--keep-package-selectors") {
		t.Fatalf("conflict not explained: %v %s", err, out)
	}
	// Machine-readable too: the side panel branches on code/keys, not text.
	var conflict struct {
		Error string   `json:"error"`
		Code  string   `json:"code"`
		Keys  []string `json:"keys"`
	}
	if jerr := json.Unmarshal([]byte(out), &conflict); jerr != nil || conflict.Code != "selector_conflict" ||
		len(conflict.Keys) != 1 || conflict.Keys[0] != "page.go_button" || conflict.Error == "" {
		t.Fatalf("conflict JSON = %+v (%v) from %s", conflict, jerr, out)
	}
	out, err = runRecordCLI(t, true, "save", res.DraftDir, "--automation", "example-go", "--name", "press go again", "--keep-package-selectors")
	if err != nil || !strings.Contains(out, "kept the package's current selector(s): page.go_button") {
		t.Errorf("with flag: %v %s", err, out)
	}
}
