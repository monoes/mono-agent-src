package hil

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

// setHome points both variables os.UserHomeDir reads (HOME on Unix, USERPROFILE on Windows) at dir.
func setHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// A browser's pairing card is decided only by the operator path: `hil approve`
// approves the claim, an agent's approve changes nothing, and an approval on
// the bare row (the MCP/API route) does not approve the claim.
func TestHILPairCardDecidesTheClaim(t *testing.T) {
	setHome(t, t.TempDir())
	cfg := &testConfig{DBPath: newHILTestDB(t), ProfileID: "default"}
	claim, secret, err := extension.CreatePendingClaim(extension.PendingClaim{Label: "Edge", Origin: "chrome-extension://aaaa"})
	code := extension.PairCode(claim.ID, secret)
	if err != nil {
		t.Fatal(err)
	}
	sdb, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if err := extension.OpenPairCard(context.Background(), sdb.DB, "default", claim); err != nil {
		t.Fatal(err)
	}
	var cardID, ro string
	if err := sdb.DB.QueryRow(`SELECT id, readonly_data FROM hil_pending WHERE execution_id = ?`, "extpair:"+claim.ID).Scan(&cardID, &ro); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ro, code) || strings.Contains(strings.ToUpper(ro), strings.ReplaceAll(code, "-", "")) {
		t.Fatalf("the card shows the confirmation code, which only the browser may show: %s", ro)
	}
	for _, want := range []string{"Edge", "chrome-extension://aaaa", extension.PairClaimWording, "Compare the code shown in the browser panel"} {
		if !strings.Contains(ro, want) {
			t.Fatalf("card does not show %q: %s", want, ro)
		}
	}

	// A row updated by a route that bypasses the CLI does not approve the claim.
	if _, err := sdb.DB.Exec(`UPDATE hil_pending SET status = 'approved' WHERE id = ?`, cardID); err != nil {
		t.Fatal(err)
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 1 {
		t.Fatalf("a bare row update decided the claim: %+v", left)
	}
	if _, err := sdb.DB.Exec(`UPDATE hil_pending SET status = 'pending' WHERE id = ?`, cardID); err != nil {
		t.Fatal(err)
	}

	deps := testDeps(cfg)
	deps.RequireOperator = func(string) error { return errors.New("operator only") }
	cmd := NewCommand(deps)
	cmd.SetArgs([]string{"approve", cardID})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("an agent approved a pairing card")
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 1 {
		t.Fatalf("a refused approve changed the claim: %+v", left)
	}

	deps = testDeps(cfg)
	deps.RequireOperator = func(string) error { return nil }
	// Without the code of the claim, or with another one's, nothing is approved.
	for name, args := range map[string][]string{
		"no code":    {"approve", cardID},
		"wrong code": {"approve", cardID, "--data", `{"code":"ZZZ-ZZZ"}`},
	} {
		cmd := NewCommand(deps)
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		if err := cmd.Execute(); err == nil {
			t.Fatalf("%s: the card was approved", name)
		}
		if left, _ := extension.ListPendingClaims(); len(left) != 1 {
			t.Fatalf("%s: changed the claim: %+v", name, left)
		}
	}
	out := captureStdout(t, func() {
		cmd := NewCommand(deps)
		cmd.SetArgs([]string{"approve", cardID, "--data", `{"code":"` + strings.ToLower(code) + `"}`})
		err = cmd.Execute()
	})
	if err != nil || !strings.Contains(out, "Approved HIL item") {
		t.Fatalf("approve = %v\n%s", err, out)
	}
	var status string
	_ = sdb.DB.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, cardID).Scan(&status)
	if status != "approved" {
		t.Fatalf("card status = %q", status)
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 0 {
		t.Fatalf("claim still waiting after approval: %+v", left)
	}
	c, err := extension.DecidePendingClaim(claim.ID, true, code)
	_ = c
	if err == nil {
		t.Fatal("the claim should be decided already")
	}
}

// The code can also arrive in the card's editable "code" field (what the app
// stores when the owner types it), and a hostile label reaches the card
// without an escape sequence or a bidi control.
func TestHILPairCardCodeInEditableFieldAndHostileLabel(t *testing.T) {
	setHome(t, t.TempDir())
	cfg := &testConfig{DBPath: newHILTestDB(t), ProfileID: "default"}
	claim, secret, err := extension.CreatePendingClaim(extension.PendingClaim{
		Label: "Edge\x1b[2K\x1b[1A\x1b[2KMonoAgent official‮", Origin: "chrome-extension://aaaa‮"})
	if err != nil {
		t.Fatal(err)
	}
	sdb, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if err := extension.OpenPairCard(context.Background(), sdb.DB, "default", claim); err != nil {
		t.Fatal(err)
	}
	var cardID, name, ro, editable string
	if err := sdb.DB.QueryRow(`SELECT id, node_name, readonly_data, editable_data FROM hil_pending WHERE execution_id = ?`, "extpair:"+claim.ID).Scan(&cardID, &name, &ro, &editable); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{name, ro} {
		if strings.ContainsRune(text, 0x1b) || strings.ContainsRune(text, 0x202e) || strings.Contains(text, `\u001b`) || strings.Contains(text, `‮`) {
			t.Fatalf("the card carries a terminal or bidi control: %s", text)
		}
	}
	if !strings.Contains(editable, `"code"`) {
		t.Fatalf("the card has no code field: %s", editable)
	}
	if _, err := sdb.DB.Exec(`UPDATE hil_pending SET editable_data = ? WHERE id = ?`, `{"code":"`+extension.PairCode(claim.ID, secret)+`"}`, cardID); err != nil {
		t.Fatal(err)
	}
	deps := testDeps(cfg)
	deps.RequireOperator = func(string) error { return nil }
	cmd := NewCommand(deps)
	cmd.SetArgs([]string{"approve", cardID})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 0 {
		t.Fatalf("claim still waiting: %+v", left)
	}
}

// pairCardFixture files a pairing card in profile "default" and returns what a test needs to decide it.
func pairCardFixture(t *testing.T) (cfg *testConfig, sdb *storage.Database, cardID, code, claimID string) {
	t.Helper()
	setHome(t, t.TempDir())
	cfg = &testConfig{DBPath: newHILTestDB(t), ProfileID: "default"}
	claim, secret, err := extension.CreatePendingClaim(extension.PendingClaim{Label: "Edge", Origin: "chrome-extension://aaaa", Instance: "edge-instance-1"})
	if err != nil {
		t.Fatal(err)
	}
	sdb, err = storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	if err := extension.OpenPairCard(context.Background(), sdb.DB, "default", claim); err != nil {
		t.Fatal(err)
	}
	if err := sdb.DB.QueryRow(`SELECT id FROM hil_pending WHERE execution_id = ?`, "extpair:"+claim.ID).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	return cfg, sdb, cardID, extension.PairCode(claim.ID, secret), claim.ID
}

// approvedClaim reads the decided claim from the pending file (it stays there until its browser polls).
func approvedClaim(t *testing.T, id string) extension.PendingClaim {
	t.Helper()
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".monoagent", "extension.pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []extension.PendingClaim
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("claim %s not in the pending file", id)
	return extension.PendingClaim{}
}

func approveCard(t *testing.T, cfg *testConfig, cardID string, data string) error {
	t.Helper()
	deps := testDeps(cfg)
	deps.RequireOperator = func(string) error { return nil }
	cmd := NewCommand(deps)
	cmd.SetArgs([]string{"approve", cardID, "--data", data})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.Execute()
}

// The card names the profile whose queue it is in as the profile the browser
// may run, and says what an empty field means; approving as it stands limits
// the browser to that profile, with no CLI step for the common one-profile case.
func TestPairCardPreselectsTheQueuesProfile(t *testing.T) {
	cfg, sdb, cardID, code, claimID := pairCardFixture(t)
	var editable, ro string
	if err := sdb.DB.QueryRow(`SELECT editable_data, readonly_data FROM hil_pending WHERE id = ?`, cardID).Scan(&editable, &ro); err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(editable), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["profiles"] == "" {
		t.Fatalf("the card has no preselected profile: %s", editable)
	}
	for _, want := range []string{"profiles field", "any", "can't serve any profile"} {
		if !strings.Contains(ro, want) {
			t.Fatalf("card text lacks %q: %s", want, ro)
		}
	}
	fields["code"] = code
	data, _ := json.Marshal(fields)
	if err := approveCard(t, cfg, cardID, string(data)); err != nil {
		t.Fatal(err)
	}
	c := approvedClaim(t, claimID)
	if !c.Restricted || len(c.AllowedProfiles) != 1 || c.AllowedProfiles[0] != "default" || c.Unrestricted {
		t.Fatalf("approved as shown = %+v", c)
	}
}

func TestPairCardProfilesFieldChoices(t *testing.T) {
	cases := []struct {
		name, profiles string
		restricted     bool
		allowed        []string
		wantErr        bool
	}{
		{"any is an explicit choice", "any", false, nil, false},
		{"star is any too", "*", false, nil, false},
		{"empty serves nothing yet", "", true, nil, false},
		{"several by id", "default, default", true, []string{"default"}, false},
		{"unknown profile is refused", "ghost", false, nil, true},
		{"any mixed with a profile is refused", "any, default", false, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, cardID, code, claimID := pairCardFixture(t)
			data, _ := json.Marshal(map[string]string{"code": code, "profiles": tc.profiles})
			err := approveCard(t, cfg, cardID, string(data))
			if tc.wantErr {
				if err == nil {
					t.Fatal("approved")
				}
				if left, _ := extension.ListPendingClaims(); len(left) != 1 {
					t.Fatalf("a refused choice decided the claim: %+v", left)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c := approvedClaim(t, claimID)
			if c.Restricted != tc.restricted || c.Unrestricted == tc.restricted || strings.Join(c.AllowedProfiles, ",") != strings.Join(tc.allowed, ",") {
				t.Fatalf("claim = %+v", c)
			}
		})
	}
}
