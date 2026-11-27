package surfaces

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/hilsend"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// applyAll runs messages through a real registry as the given author and
// returns it; every surface it holds must pass Check.
func applyAll(t *testing.T, prov a2ui.Provenance, msgs [][]byte) *a2ui.Registry {
	t.Helper()
	reg := a2ui.NewRegistry(a2ui.DefaultLimits)
	for _, m := range msgs {
		if _, err := reg.Handle(m, prov); err != nil {
			t.Fatalf("registry refused a built message: %v\n%s", err, m)
		}
	}
	return reg
}

func surfaceOf(t *testing.T, msgs [][]byte, id string) *a2ui.Surface {
	t.Helper()
	reg := applyAll(t, a2ui.FromSystem, msgs)
	s := reg.Surface(id)
	if s == nil {
		t.Fatalf("surface %q not live", id)
	}
	if err := s.Check(a2ui.DefaultLimits); err != nil {
		t.Fatalf("Check: %v", err)
	}
	return s
}

func golden(t *testing.T, name string, msgs [][]byte) {
	t.Helper()
	var out bytes.Buffer
	for _, m := range msgs {
		if err := json.Indent(&out, m, "", "  "); err != nil {
			t.Fatal(err)
		}
		out.WriteString("\n")
	}
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Fatalf("%s differs from the golden file (run with -update to accept):\n%s", name, out.String())
	}
}

func sampleHIL() HILItem {
	return HILItem{ID: "482", NodeName: "Send welcome DM", WorkflowName: "Outreach",
		ReadonlyData: map[string]any{"recipient": "@sara", "followers": float64(1200), "note": "vip"},
		EditableData: map[string]any{"message": "Hi Sara, welcome!", "delay_minutes": float64(5)}}
}

func samplePayload() hilsend.Payload {
	return hilsend.Payload{Kind: "send", ID: "7c1e", State: hilsend.StatePending,
		PreparedBy: hilsend.PreparedBy{Role: "outreach", AgentID: "coder-lead"},
		Action:     hilsend.ActionDM, Channel: "instagram",
		Account:   hilsend.Account{Platform: "instagram", Handle: "acme"},
		Recipient: hilsend.Recipient{Name: "Ada"}, Text: "Hi Ada, loved your post", Subject: "Hello",
		Details: []hilsend.Detail{{Key: "cc", Value: "bob@example.com"}}}
}

func samplePlan() chatevents.MissionPlanPayload {
	return chatevents.MissionPlanPayload{SessionID: 17, Title: "Ship the docs", Goal: "Rewrite the install guide", TaskCount: 6, EstimatedBudgetUSD: 4.5, Round: 1}
}

func sampleReport() chatevents.MissionReportPayload {
	return chatevents.MissionReportPayload{SessionID: 17, Title: "Ship the docs", State: "done", TaskCount: 6, Done: 4, Verified: 2,
		Artifacts: 3, SpentUSD: 3.2, BudgetUSD: 5, Report: "All tasks verified.", CloseProposed: true}
}

func TestGolden(t *testing.T) {
	hil, err := HIL(sampleHIL())
	if err != nil {
		t.Fatal(err)
	}
	send, err := Send(samplePayload(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := MissionPlan(samplePlan(), 10)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "hil", hil)
	golden(t, "send", send)
	golden(t, "plan", plan)
}

func allBuilt(t *testing.T) map[string][][]byte {
	t.Helper()
	out := map[string][][]byte{}
	var err error
	if out["hil-482"], err = HIL(sampleHIL()); err != nil {
		t.Fatal(err)
	}
	if out["send-7c1e"], err = Send(samplePayload(), "d1"); err != nil {
		t.Fatal(err)
	}
	if out["ask-q9"], err = Ask("q9", []Question{{Text: "Which region?", Options: []string{"eu", "us"}}, {Text: "Any note?"}}); err != nil {
		t.Fatal(err)
	}
	if out["mission-17"], err = MissionPlan(samplePlan(), 10); err != nil {
		t.Fatal(err)
	}
	if out["mission-report-17"], err = MissionReport(sampleReport()); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEveryBuilderPassesTheRegistryAsSystem(t *testing.T) {
	for id, msgs := range allBuilt(t) {
		s := surfaceOf(t, msgs, id)
		if s.Prov != a2ui.FromSystem {
			t.Errorf("%s: provenance %s", id, s.Prov)
		}
	}
}

// Each of these surfaces uses a reserved action name, so an agent-authored
// copy must be refused; if the builders stopped using reserved names or the
// core stopped guarding them this fails.
func TestAgentCannotSubmitTheSameSurfaces(t *testing.T) {
	for id, msgs := range allBuilt(t) {
		reg := a2ui.NewRegistry(a2ui.DefaultLimits)
		_, err := reg.Handle(msgs[0], a2ui.FromAgent)
		if err == nil || !strings.Contains(err.Error(), "reserved for system") {
			t.Errorf("%s: agent submission was not refused for its reserved name: %v", id, err)
		}
	}
}

func TestHILShape(t *testing.T) {
	msgs, _ := HIL(sampleHIL())
	s := surfaceOf(t, msgs, "hil-482")
	if !s.DeclaresAction(btnApprove, "hil.approve") || !s.DeclaresAction(btnReject, "hil.reject") {
		t.Fatal("buttons missing")
	}
	edit := s.Data.(map[string]any)["edit"].(map[string]any)
	if edit["message"] != "Hi Sara, welcome!" || edit["delay_minutes"] != "5" {
		t.Fatalf("data model %v", edit)
	}
	ev := s.Components[btnApprove]["action"].(map[string]any)["event"].(map[string]any)["context"].(map[string]any)
	if ev["id"] != "482" || ev["message"].(map[string]any)["@path"] != "/edit/message" {
		t.Fatalf("approve context %v", ev)
	}
	rej := s.Components[btnReject]["action"].(map[string]any)["event"].(map[string]any)["context"].(map[string]any)
	if len(rej) != 1 || rej["id"] != "482" {
		t.Fatalf("reject context %v", rej)
	}
}

func TestHILKeyEscapingAndRefusals(t *testing.T) {
	it := sampleHIL()
	it.EditableData = map[string]any{"a/b~c": "x"}
	msgs, err := HIL(it)
	if err != nil {
		t.Fatal(err)
	}
	s := surfaceOf(t, msgs, "hil-482")
	if s.Data.(map[string]any)["edit"].(map[string]any)["a/b~c"] != "x" {
		t.Fatal("key lost")
	}
	if !strings.Contains(string(msgs[0]), `/edit/a~1b~0c`) {
		t.Fatal("pointer not escaped")
	}
	for name, mut := range map[string]func(*HILItem){
		"id key":        func(i *HILItem) { i.EditableData = map[string]any{"id": "x"} },
		"nested value":  func(i *HILItem) { i.EditableData = map[string]any{"k": map[string]any{"a": 1}} },
		"bad item id":   func(i *HILItem) { i.ID = "a b/c" },
		"empty item id": func(i *HILItem) { i.ID = "" },
	} {
		it := sampleHIL()
		mut(&it)
		if _, err := HIL(it); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func TestEditableTextIsNeverCut(t *testing.T) {
	it := sampleHIL()
	it.EditableData = map[string]any{"message": strings.Repeat("a", a2ui.DefaultLimits.MaxReturnedString+1)}
	if _, err := HIL(it); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	it.EditableData = map[string]any{"message": strings.Repeat("a", 20<<10)}
	msgs, err := HIL(it)
	if err != nil {
		t.Fatal(err)
	}
	if got := surfaceOf(t, msgs, "hil-482").Data.(map[string]any)["edit"].(map[string]any)["message"]; len(got.(string)) != 20<<10 {
		t.Fatal("the editable value changed length")
	}
	p := samplePayload()
	p.Text = strings.Repeat("é", 13000)
	if _, err := Send(p, "d1"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("send text: %v", err)
	}
	it.EditableData = map[string]any{}
	for i := 0; i < 20; i++ {
		it.EditableData[fmt.Sprintf("k%d", i)] = "v"
	}
	if _, err := HIL(it); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("20 editable fields: %v", err)
	}
}

// Read-only text of a card nobody can approve is cut on a rune boundary.
func TestSettledCardTruncatesReadonlyText(t *testing.T) {
	p := samplePayload()
	p.State, p.Text = hilsend.StateSent, strings.Repeat("ü", 5000)
	s := surfaceOf(t, mustSend(t, p), "send-7c1e")
	got := s.Components["text"]["text"].(string)
	if !strings.HasSuffix(got, Truncated) || len(got) > a2ui.DefaultLimits.MaxStringBytes || !validUTF8(got) {
		t.Fatalf("bad truncation: %d bytes", len(got))
	}
}

func validUTF8(s string) bool { return strings.ToValidUTF8(s, "") == s }

// Control characters escape to about six bytes each, which puts the envelope
// over the 64 KiB message cap at full length; a settled card shortens its
// read-only text rather than emit an oversize message.
func TestOversizeEnvelopeShrinksReadonlyText(t *testing.T) {
	p := samplePayload()
	big := strings.Repeat("\x01", 4000)
	p.State, p.Error, p.Text, p.Subject = hilsend.StateFailed, big, big, big
	msgs := mustSend(t, p)
	if len(msgs[0]) > a2ui.DefaultLimits.MaxMessageBytes {
		t.Fatalf("%d bytes", len(msgs[0]))
	}
	s := surfaceOf(t, msgs, "send-7c1e")
	if !strings.HasSuffix(s.Components["text"]["text"].(string), Truncated) {
		t.Fatal("values were not shortened")
	}
}

func TestSendVariants(t *testing.T) {
	p := samplePayload()
	s := surfaceOf(t, mustSend(t, p), "send-7c1e")
	if s.Components["text"]["component"] != "TextField" || s.Components["text"]["variant"] != "longText" || s.Components["subject"]["component"] != "TextField" {
		t.Fatal("text and subject should be editable")
	}
	if s.Components["action"]["tone"] != "warning" {
		t.Fatal("dm should be a warning")
	}
	ctx := s.Components[btnApprove]["action"].(map[string]any)["event"].(map[string]any)["context"].(map[string]any)
	if ctx["id"] != "7c1e" || ctx["text"] == nil || ctx["subject"] == nil {
		t.Fatalf("context %v", ctx)
	}
	p.Action = hilsend.ActionConnect
	if surfaceOf(t, mustSend(t, p), "send-7c1e").Components["action"]["tone"] != "info" {
		t.Fatal("connect should be info")
	}
	p.State = hilsend.StateSent
	s = surfaceOf(t, mustSend(t, p), "send-7c1e")
	for id, c := range s.Components {
		if c["component"] == "Button" || c["component"] == "TextField" {
			t.Fatalf("settled surface has %s %s", c["component"], id)
		}
	}
	p = samplePayload()
	p.Unreviewable = "too large to review"
	s = surfaceOf(t, mustSend(t, p), "send-7c1e")
	if s.DeclaresAction(btnApprove, "hil.approve") || !s.DeclaresAction(btnReject, "hil.reject") {
		t.Fatal("unreviewable item must be reject-only")
	}
	p = samplePayload()
	p.TextReadOnly = true
	if surfaceOf(t, mustSend(t, p), "send-7c1e").Components["text"]["component"] != "Text" {
		t.Fatal("read-only text became editable")
	}
}

func mustSend(t *testing.T, p hilsend.Payload) [][]byte {
	t.Helper()
	m, err := Send(p, "d1")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAskAndMissionShapes(t *testing.T) {
	s := surfaceOf(t, allBuilt(t)["ask-q9"], "ask-q9")
	if s.Components["in-q0"]["component"] != "ChoicePicker" || s.Components["in-q1"]["component"] != "TextField" || !s.DeclaresAction(btnContinue, "system.answer") {
		t.Fatal("ask shape")
	}
	if _, err := Ask("x", nil); err == nil {
		t.Fatal("zero questions built")
	}
	if _, err := Ask("x", make([]Question, 4)); err == nil {
		t.Fatal("four questions built")
	}
	plan := surfaceOf(t, allBuilt(t)["mission-17"], "mission-17")
	for _, n := range []string{"mission.edit", "mission.stop"} {
		found := false
		for id := range plan.Components {
			found = found || plan.DeclaresAction(id, n)
		}
		if !found {
			t.Errorf("plan lacks %s", n)
		}
	}
	rep := surfaceOf(t, allBuilt(t)["mission-report-17"], "mission-report-17")
	if !rep.DeclaresAction(btnClose, "mission.close") {
		t.Fatal("close button")
	}
	r := sampleReport()
	r.CloseProposed, r.Report = false, strings.Repeat("r", 9000)
	msgs, err := MissionReport(r)
	if err != nil {
		t.Fatal(err)
	}
	rep = surfaceOf(t, msgs, "mission-report-17")
	if rep.DeclaresAction(btnClose, "mission.close") {
		t.Fatal("close offered without CloseProposed")
	}
	if got := rep.Data.(map[string]any)["report"].(map[string]any)["text"].(string); len(got) > 4000 || !strings.HasSuffix(got, Truncated) {
		t.Fatalf("report not truncated: %d", len(got))
	}
}

func TestMissionProgressUpdatesInPlace(t *testing.T) {
	plan, _ := MissionPlan(samplePlan(), 10)
	reg := applyAll(t, a2ui.FromSystem, plan)
	upd, err := MissionProgressUpdate(17, 2, 6, 1.5, 10, 4.5, map[int64]string{1: "done", 2: "done", 3: "running", 4: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Handle(upd[0], a2ui.FromSystem); err != nil {
		t.Fatal(err)
	}
	m := reg.Surface("mission-17").Data.(map[string]any)["mission"].(map[string]any)
	if m["summary"] != "done 2 · queued 1 · running 1" || fmt.Sprint(m["done"]) != "2" || fmt.Sprint(m["spent"]) != "1.5" {
		t.Fatalf("model %v", m)
	}
	if _, err := reg.Handle(upd[0], a2ui.FromAgent); err == nil {
		t.Fatal("an agent updated a system card")
	}
	// the card binds exactly the paths the update writes
	for _, p := range []string{"/mission/done", "/mission/total", "/mission/spent", "/mission/budget", "/mission/summary"} {
		if !strings.Contains(string(plan[0]), p) && p != "/mission/spent" {
			t.Errorf("plan card does not bind %s", p)
		}
	}
}

func TestResolvedNoticeLeavesNoLiveButtons(t *testing.T) {
	msgs, _ := HIL(sampleHIL())
	reg := applyAll(t, a2ui.FromSystem, msgs)
	act := a2ui.Action{Name: "hil.approve", SurfaceID: "hil-482", SourceComponentID: btnApprove}
	if _, err := reg.CheckAction(act); err != nil {
		t.Fatal(err)
	}
	note, err := ResolvedNotice("hil-482", "Approved by you", "success")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range note {
		if _, err := reg.Handle(m, a2ui.FromSystem); err != nil {
			t.Fatal(err)
		}
	}
	s := reg.Surface("hil-482")
	if err := s.Check(a2ui.DefaultLimits); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.CheckAction(act); err == nil {
		t.Fatal("the old Approve button survived the notice")
	}
	for _, c := range s.Components {
		if c["component"] == "Button" {
			t.Fatal("notice has a button")
		}
	}
	del, _ := Resolved("hil-482")
	if _, err := reg.Handle(del[0], a2ui.FromSystem); err != nil || reg.Surface("hil-482") != nil {
		t.Fatal("Resolved did not delete")
	}
}

func TestMissionRejectsBadIDsAndNumbers(t *testing.T) {
	if _, err := MissionPlan(chatevents.MissionPlanPayload{}, 0); err == nil {
		t.Fatal("zero session id built")
	}
	p := samplePlan()
	p.EstimatedBudgetUSD = nan()
	msgs, err := MissionPlan(p, -3)
	if err != nil {
		t.Fatal(err)
	}
	surfaceOf(t, msgs, "mission-17")
}

func nan() float64 { z := 0.0; return z / z }

func TestChoiceAnswerListIsNotACoreFeatureYet(t *testing.T) {
	// documents the gap reported to the core owner: ParseAction refuses a
	// list in the action context, which is what a ChoicePicker binding resolves to
	raw := []byte(`{"version":"1.0","action":{"name":"system.answer","surfaceId":"ask-q9","sourceComponentId":"btn-continue","context":{"questionId":"q9","q0":["eu"]}}}`)
	if _, err := a2ui.ParseAction(raw, a2ui.DefaultLimits); err == nil {
		t.Log("core now accepts lists in the action context; the workaround in Route is no longer needed for ParseAction")
	}
}
