package record

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/action"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/recording"
)

// ingestFormRecording lands the analyzer's form-submit fixture (email, name,
// a masked password, Save) as recording id, plus the given network lines.
func ingestFormRecording(t *testing.T, id string, net ...recording.NetEntry) {
	t.Helper()
	recordTestHome(t)
	f, err := os.Open(filepath.Join("..", "..", "recordanalyze", "testdata", "form-submit", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in := &recording.Ingest{}
	frames := []recording.Frame{{Op: recording.OpStart, RecordingID: id, URL: "https://app.acme-crm.test/contacts/new", Title: "Acme",
		StartedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC).UnixMilli()}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev recording.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, recording.Frame{Op: recording.OpEvent, RecordingID: id, Event: &ev})
	}
	for i := range net {
		frames = append(frames, recording.Frame{Op: recording.OpNetwork, RecordingID: id, Net: &net[i]})
	}
	for _, fr := range frames {
		fr := fr
		if _, err := in.Handle(&fr); err != nil {
			t.Fatal(err)
		}
	}
	out, err := in.Handle(&recording.Frame{Op: recording.OpStop, RecordingID: id, Reason: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Finalize(out.Stopped); err != nil {
		t.Fatal(err)
	}
}

func TestRecordAPIListAndEmit(t *testing.T) {
	ingestFormRecording(t, "rec-api", recording.NetEntry{
		Method: "POST", URL: "https://api.acme-crm.test/v1/contacts", Status: 201, ContentType: "application/json",
		RequestType: "application/json", RequestBody: `{"email":"jane@example.com","password":"x"}`,
		RequestHeaders: []recording.NetHeader{{Name: "Authorization", Value: "Bearer live-123"}, {Name: "Accept", Value: "application/json"}},
	}, recording.NetEntry{Method: "GET", URL: "https://app.acme-crm.test/logo.png", Status: 200, ContentType: "image/png"})

	out, err := runRecordCLI(t, true, "api", "rec-api")
	if err != nil {
		t.Fatalf("api: %v\n%s", err, out)
	}
	var list struct {
		Calls []recording.APICall `json:"calls"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Calls) != 1 {
		t.Fatalf("calls: %v\n%s", err, out)
	}
	if strings.Contains(out, "live-123") {
		t.Errorf("credential stored: %s", out)
	}

	out, err = runRecordCLI(t, false, "api", "rec-api", "--emit-node", "1")
	if err != nil {
		t.Fatalf("emit-node: %v\n%s", err, out)
	}
	var node struct {
		Type    string         `json:"type"`
		Config  map[string]any `json:"config"`
		Secrets []string       `json:"secrets"`
	}
	if err := json.Unmarshal([]byte(out), &node); err != nil {
		t.Fatal(err)
	}
	if node.Type != "http.request" || node.Config["auth_token"] != "@secret:api-acme-crm-test-authorization" || node.Config["method"] != "POST" {
		t.Errorf("node = %+v", node)
	}

	out, err = runRecordCLI(t, false, "api", "rec-api", "--emit-workflow", "--name", "Contacts API")
	if err != nil {
		t.Fatalf("emit-workflow: %v\n%s", err, out)
	}
	var wf recording.ImportWorkflow
	if err := json.Unmarshal([]byte(out), &wf); err != nil {
		t.Fatal(err)
	}
	if wf.Name != "Contacts API" || len(wf.Nodes) != 2 || wf.Nodes[1].Type != "http.request" || len(wf.Connections) != 1 {
		t.Errorf("workflow = %+v", wf)
	}

	if _, err := runRecordCLI(t, false, "api", "rec-api", "--emit-node", "5"); err == nil {
		t.Error("--emit-node past the last call should fail")
	}
	if _, err := runRecordCLI(t, false, "api", "rec-api", "--emit-workflow", "--calls", "2"); err == nil {
		t.Error("--calls with an unknown number should fail")
	}
}

func TestRecordReplayDryRunAndInputs(t *testing.T) {
	ingestFormRecording(t, "rec-replay")
	out, err := runRecordCLI(t, true, "replay", "rec-replay", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	var plan struct {
		Steps []struct {
			Type       string `json:"type"`
			SideEffect bool   `json:"sideEffect"`
		} `json:"steps"`
		Missing []string `json:"missingInputs"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 5 || !plan.Steps[4].SideEffect || strings.Join(plan.Missing, ",") != "account_password" {
		t.Errorf("plan = %+v", plan)
	}

	text, err := runRecordCLI(t, false, "replay", "rec-replay", "--dry-run")
	if err != nil || !strings.Contains(text, "! ") || !strings.Contains(text, "account_password") {
		t.Errorf("text plan: %v\n%s", err, text)
	}

	stubReplayBrowser(t, nil)
	if _, err := runRecordCLI(t, false, "replay", "rec-replay"); err == nil || !strings.Contains(err.Error(), "--input account_password=") {
		t.Errorf("missing secret: %v", err)
	}
}

// stubReplayBrowser replaces the browser and self-heal hooks; the browser
// is never reachable, so a run ends right after setup.
func stubReplayBrowser(t *testing.T, healCalls *int) {
	t.Helper()
	oldPage, oldHeal, oldTerm := seams.ReplayPage, seams.SelfHeal, seams.StdinIsTerminal
	t.Cleanup(func() { seams.ReplayPage, seams.SelfHeal, seams.StdinIsTerminal = oldPage, oldHeal, oldTerm })
	seams.ReplayPage = func(context.Context, bool) (browserpkg.PageInterface, zerolog.Logger, error) {
		return nil, zerolog.Nop(), errors.New("browser bridge not connected: stub")
	}
	seams.SelfHeal = func(context.Context) (func(*action.ActionExecutor), func(), error) {
		if healCalls != nil {
			*healCalls++
		}
		return func(*action.ActionExecutor) {}, func() {}, nil
	}
	seams.StdinIsTerminal = func() bool { return false }
}

func TestRecordReplaySelfHealIsOptIn(t *testing.T) {
	ingestFormRecording(t, "rec-heal")
	calls := 0
	stubReplayBrowser(t, &calls)
	_, err := runRecordCLI(t, false, "replay", "rec-heal", "--input", "account_password=pw")
	if err == nil || !strings.Contains(err.Error(), "stub") || calls != 0 {
		t.Errorf("default run: err=%v heal setups=%d", err, calls)
	}
	_, err = runRecordCLI(t, false, "replay", "rec-heal", "--input", "account_password=pw", "--self-heal")
	if err == nil || calls != 1 {
		t.Errorf("--self-heal: err=%v heal setups=%d", err, calls)
	}
}
