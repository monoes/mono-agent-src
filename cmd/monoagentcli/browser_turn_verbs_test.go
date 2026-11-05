package main

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/extension"
)

const inATurn = "this command runs inside a coder session's turn"

func setLevels(t *testing.T, bt *browserTest, levels map[string]string) {
	t.Helper()
	if _, err := browserctl.UpdatePermissions(bt.db().DB, func(p *browserctl.Permissions) error {
		for k, l := range levels {
			if err := p.SetLevel(k, l); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func reachedThePage(bt *browserTest) bool {
	for _, c := range bt.fake.Calls() {
		if c.Type == extension.CmdFormRun || c.Type == extension.CmdSetFiles {
			return true
		}
	}
	return false
}

// fill and upload from a process in a turn with no markers get the agents' origin policy; the same
// command from the user's own terminal is not held.
func TestFillAndUploadFromAnUnmarkedTurnAreHeldToTheAgentPolicy(t *testing.T) {
	cv := writeUploadFile(t, "cv.pdf", "x")
	for _, args := range [][]string{{"fill"}, {"upload", "#f", cv}} {
		bt := newBrowserTest(t, false)
		tab := bt.fake.AddTab("https://example.com/apply", "", true)
		answerFormRun(bt)
		bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return map[string]any{"count": 1}, nil })
		args = append(args, "--tab", itoa(tab))

		opgate.SetForTest(t, func(bool) string { return inATurn })
		if _, err := bt.run(args...); exitCodeFor(err) != 3 || jsonCode(err) != "origin_not_allowed" {
			t.Fatalf("%v from a turn: exit %d code %q: %v", args[0], exitCodeFor(err), jsonCode(err), err)
		}
		if reachedThePage(bt) {
			t.Fatalf("%v from a turn reached the page", args[0])
		}
		opgate.SetForTest(t, func(bool) string { return "" })
		if _, err := bt.run(args...); err != nil || !reachedThePage(bt) {
			t.Fatalf("%v from the user's terminal was held: %v", args[0], err)
		}
	}
}

// submit --hil and browsers by name obey the level of the kind a turn's process is: other at off is
// refused with browser_off, and an approved submit is handed back, not used up.
func TestSubmitAndBrowsersFromAnUnmarkedTurnObeyTheOtherKindsLevel(t *testing.T) {
	bt := newBrowserTest(t, true)
	if err := browserctl.SaveAllowlist(bt.db().DB, []string{"https://example.com"}); err != nil {
		t.Fatal(err)
	}
	bt.fake.AddTab("https://example.com/compose", "Coder · Agent", true)
	fp := &browserctltest.FormPage{Action: "https://example.com/post", Method: "post", Selector: "#f", Submit: true,
		Fields: []browserctl.FormField{{Name: "body", Type: "textarea", Value: "hi"}}}
	bt.fake.On("eval_cdp", fp.Handler())
	out, err := bt.run("click", "#send")
	if exitCodeFor(err) != 3 {
		t.Fatalf("held click: %v %s", err, out)
	}
	hilID := strings.Split(strings.Split(out, `"hil_id": "`)[1], `"`)[0]
	t.Setenv("CLAUDECODE", "")
	if _, err := runHIL(t, bt.cfg, "approve", hilID); err != nil {
		t.Fatal(err)
	}
	setLevels(t, bt, map[string]string{browserctl.KindOther: browserctl.LevelOff})
	opgate.SetForTest(t, func(bool) string { return inATurn })

	for _, args := range [][]string{{"submit", "--hil", hilID}, {"browsers"}} {
		if _, err := bt.run(args...); exitCodeFor(err) != 3 || jsonCode(err) != "browser_off" {
			t.Fatalf("%v from a turn at off: exit %d code %q: %v", args, exitCodeFor(err), jsonCode(err), err)
		}
	}
	if it, _ := browserctl.GetSubmitItem(t.Context(), bt.db().DB, "default", hilID); it.Status != "approved" {
		t.Fatalf("the refused submit used the approval up: %s", it.Status)
	}
	if fp.Triggered != 0 {
		t.Fatal("the refused submit went through")
	}
	opgate.SetForTest(t, func(bool) string { return "" })
	if res := bt.runJSON("browsers"); len(res["browsers"].([]any)) != 1 {
		t.Fatalf("the user's terminal: %v", res)
	}
}

// A chat marker inside a turn is stricter of chat and other, whichever is stricter; outside a turn
// the chat keeps its own level.
func TestChatInATurnIsHeldToTheStricterOfChatAndOther(t *testing.T) {
	for _, tc := range []struct {
		name, chat, other string
		turn, refused     bool
	}{
		{"chat off, other open, in a turn", browserctl.LevelOff, browserctl.LevelFullNoApprovals, true, true},
		{"chat open, other off, in a turn", browserctl.LevelFullNoApprovals, browserctl.LevelOff, true, true},
		{"chat open, other off, outside a turn", browserctl.LevelFullNoApprovals, browserctl.LevelOff, false, false},
		{"chat off, other open, outside a turn", browserctl.LevelOff, browserctl.LevelFullNoApprovals, false, true},
	} {
		bt := newBrowserTest(t, false)
		t.Setenv(browserctl.EnvAICaller, browserctl.KindChat)
		setLevels(t, bt, map[string]string{browserctl.KindChat: tc.chat, browserctl.KindOther: tc.other})
		turn := ""
		if tc.turn {
			turn = inATurn
		}
		opgate.SetForTest(t, func(bool) string { return turn })
		_, err := bt.run("browsers")
		if tc.refused != (jsonCode(err) == "browser_off") || (err != nil) != tc.refused {
			t.Errorf("%s: browsers: %v (%s), want refused=%v", tc.name, err, jsonCode(err), tc.refused)
		}
		_, err = bt.run("open", "http://localhost:3000/")
		if tc.refused != (jsonCode(err) == "browser_off") || (err != nil) != tc.refused {
			t.Errorf("%s: open: %v (%s), want refused=%v", tc.name, err, jsonCode(err), tc.refused)
		}
	}
}

// Every browser verb asks the turn check once: the policy and the session key share one verdict.
func TestEveryBrowserVerbAsksTheTurnCheckOnce(t *testing.T) {
	cv := writeUploadFile(t, "cv.pdf", "x")
	for _, args := range [][]string{{"tabs"}, {"permissions"}, {"browsers"}, {"fill"}, {"upload", "#f", cv}, {"click", "#go"}} {
		bt := newBrowserTest(t, false)
		bt.fake.AddTab("http://localhost:3000/", "", true)
		answerFormRun(bt)
		bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return map[string]any{"count": 1}, nil })
		asked := 0
		opgate.SetForTest(t, func(bool) string { asked++; return "" })
		if _, err := bt.run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if asked != 1 {
			t.Errorf("%v asked the turn check %d times, want 1", args, asked)
		}
	}
}
