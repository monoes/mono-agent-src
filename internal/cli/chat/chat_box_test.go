package chat

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentbox"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
)

func codeOf(err error) string {
	var c interface{ JSONErrorFields() map[string]any }
	if errors.As(err, &c) {
		s, _ := c.JSONErrorFields()["code"].(string)
		return s
	}
	return ""
}

func TestCoderBoxDecision(t *testing.T) {
	if b, err := coderBox(coderSettings{}, ai.AccessCoding, "/w", "claude", ""); b != nil || err != nil {
		t.Fatalf("unset: %v %v", b, err)
	}
	if b, err := coderBox(coderSettings{Box: "off"}, ai.AccessFull, "/w", "claude", ""); b != nil || err != nil {
		t.Fatalf("off: %v %v", b, err)
	}
	b, err := coderBox(coderSettings{Box: "strict"}, ai.AccessCoding, "/w", "claude", "")
	if err != nil || b == nil || b.Mode != agentbox.Strict || b.Cwd != "/w" || b.Runtime != "claude" {
		t.Fatalf("coding: %+v %v", b, err)
	}
	for _, a := range []string{ai.AccessWorkspace, ai.AccessFull, ""} {
		if b, err := coderBox(coderSettings{Box: "compat"}, a, "/w", "claude", ""); b != nil || codeOf(err) != CodeBoxNeedsAPIMode {
			t.Errorf("access %q: %v %v", a, b, err)
		}
	}
	if _, err := coderBox(coderSettings{Box: "strict"}, ai.AccessCoding, "/w", "claude", ai.OrgModeDynamic); codeOf(err) != CodeBoxOrgUnsupport {
		t.Errorf("dynamic org: %v", err)
	}
	if (coderSettings{Box: "bogus"}).boxMode() != BoxOff {
		t.Error("an unknown value is not off")
	}
}

var tempName = regexp.MustCompile(`/\S*/\.monoagent/tmp/monoagent-[a-z]+-\d+\.(md|json)`)

// With coder.box unset or off the argv monomind gets is the same, and the turn is not
// wrapped.
func TestCoderBoxOffLeavesTheTurnUnchanged(t *testing.T) {
	var lines []string
	for _, box := range []string{"", "off"} {
		r := newAccessRig(t, true)
		setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, Box: box})
		c := r.conv(t, ai.AccessCoding)
		if out, err := runChatCmd(t, r.dbPath, r.bin, "--conversation", c.ID, "--turn", "t1", "--", "hi"); err != nil {
			t.Fatalf("box=%q: %v\n%s", box, err, out)
		}
		line := tempName.ReplaceAllString(strings.ReplaceAll(r.exec(), c.Cwd, "CWD"), "TMP")
		if !strings.HasPrefix(line, "agent exec --runtime claude") || strings.Contains(line, "bwrap") {
			t.Fatalf("box=%q argv: %s", box, line)
		}
		lines = append(lines, line)
	}
	if lines[0] != lines[1] {
		t.Errorf("argv differs between unset and off:\n%s\n%s", lines[0], lines[1])
	}
}

func TestCoderBoxRefusesAccessThatNeedsTheDatabase(t *testing.T) {
	for _, access := range []string{ai.AccessWorkspace, ai.AccessFull} {
		r := newAccessRig(t, true)
		setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, Box: "strict"})
		c := r.conv(t, access)
		r.reset()
		_, err := runChatCmd(t, r.dbPath, r.bin, "--conversation", c.ID, "--turn", "t1", "--", "hi")
		if codeOf(err) != CodeBoxNeedsAPIMode || exitCodeFor(err) == 0 {
			t.Errorf("%s: err = %v", access, err)
		}
		if r.exec() != "" {
			t.Errorf("%s: a turn started although the box refused: %s", access, r.exec())
		}
	}
}

// A coding-access turn with coder.box strict runs inside a real bubblewrap box.
func TestCoderBoxRunsACodingTurnInTheBox(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %s", out)
	}
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	bin, _ := writeCoderMonomind(t, `  touch "$PWD/boxed-$MONOAGENT_SANDBOX"
`+successTranscript)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, Box: "strict"})
	store := openTestChatStore(t, dbPath)
	cwd := t.TempDir()
	c, err := store.CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessCoding, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", c.ID, "--turn", "t1", "--", "hi"); err != nil {
		t.Fatalf("boxed turn: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cwd, "boxed-bwrap-strict")); err != nil {
		t.Fatalf("the turn did not run inside the strict box: %v", err)
	}
}

func TestCoderBoxSettingIsOffByDefaultAndTheOperators(t *testing.T) {
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	if out, code := runCoderCLI(t, dbPath, "status"); code != 0 || !strings.Contains(out, `"box":"off"`) {
		t.Fatalf("default status (%d): %s", code, out)
	}
	for _, bad := range []string{"on", "bwrap", ""} {
		if _, code := runCoderCLI(t, dbPath, "set", "--box="+bad); code != 3 {
			t.Errorf("--box=%q accepted (exit %d)", bad, code)
		}
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--box=strict"); code != 0 {
		t.Fatalf("operator set --box=strict: exit %d", code)
	}
	if out, _ := runCoderCLI(t, dbPath, "status"); !strings.Contains(out, `"box":"strict"`) {
		t.Errorf("status after set: %s", out)
	}
	// That an agent is refused --box is covered by TestEveryCoderSetFlagIsTheOperators.
}
