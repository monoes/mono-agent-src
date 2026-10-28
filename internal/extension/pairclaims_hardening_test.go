package extension

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// runHelper is the other process of the cross-process tests (see TestMain):
// "hold" takes the state lock and keeps it until stdin gets a line, "create"
// records one pending claim. It reports whether this process was a helper.
func runHelper() bool {
	switch os.Getenv("EXT_HELPER") {
	case "hold":
		unlock, err := lockState()
		if err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("locked\n")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		unlock()
		return true
	case "create":
		_, _, err := CreatePendingClaim(PendingClaim{Label: "child", Origin: "chrome-extension://child", Instance: "child-instance-" + os.Getenv("EXT_N")})
		if err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(2)
		}
		return true
	}
	return false
}

func helper(t *testing.T, mode string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), append([]string{"EXT_HELPER=" + mode}, env...)...)
	return cmd
}

// Another process (the bridge) holds the state lock: the owner's decision
// waits for it instead of writing over what that process is about to write.
func TestDecisionWaitsForAnotherProcessHoldingTheStateLock(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	c, code := newClaimT(t, "wait")

	holder := helper(t, "hold")
	stdin, _ := holder.StdinPipe()
	out, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(out).ReadString('\n'); strings.TrimSpace(line) != "locked" {
		t.Fatalf("holder said %q", line)
	}
	done := make(chan error, 1)
	go func() {
		_, err := DecidePendingClaim(c.ID, true, code)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the decision ran while another process held the lock (err = %v)", err)
	case <-time.After(400 * time.Millisecond):
	}
	_, _ = stdin.Write([]byte("\n"))
	_ = stdin.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the decision never ran after the lock was released")
	}
	_ = holder.Wait()
}

// Several processes record claims at once: none is lost.
func TestConcurrentProcessesLoseNoUpdate(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("USERPROFILE", home)
	const n = 4
	var cmds []*exec.Cmd
	for i := 0; i < n; i++ {
		cmd := helper(t, "create", "EXT_N="+strconv.Itoa(i))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := ListPendingClaims(); len(list) != n {
		t.Fatalf("%d claims survived, want %d: a concurrent write undid another", len(list), n)
	}
}

const hostile = "Edge\x1b[2K\x1b[1A\x1b[2KMonoAgent official‮⁦ x y\tz"

func noUnsafe(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			t.Errorf("%s keeps %U: %q", what, r, s)
		}
	}
}

func TestHostileTextIsStrippedEverywhereItIsStored(t *testing.T) {
	setHome(t, t.TempDir())
	noUnsafe(t, "cleanLabel", cleanLabel(hostile))
	if got := cleanLabel(hostile); !strings.Contains(got, "MonoAgent official") {
		t.Fatalf("cleanLabel dropped the text: %q", got)
	}
	c, _, err := CreatePendingClaim(PendingClaim{Label: hostile, Origin: "chrome-extension://abc‮" + hostile, Agent: hostile, Instance: hostile})
	if err != nil {
		t.Fatal(err)
	}
	noUnsafe(t, "claim label", c.Label)
	noUnsafe(t, "claim origin", c.Origin)
	noUnsafe(t, "claim agent", c.Agent)
	_, entry, err := addPairedBrowser(PairedBrowser{Label: hostile, Origin: hostile, Agent: hostile}, mintOpts{})
	if err != nil {
		t.Fatal(err)
	}
	noUnsafe(t, "browser label", entry.Label)
	noUnsafe(t, "browser origin", entry.Origin)
}

// Through the bridge: the label a forged browser sends reaches the claim, the
// card and the log without an escape sequence or a bidi control.
func TestHostileLabelThroughTheClaimHandler(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	cards := &fakeCards{}
	srv.SetPairCards(cards)
	body, _ := json.Marshal(map[string]string{"instance": "0123456789abcdef", "label": hostile})
	if w := claim(srv, claimReqBody(string(body), nil)); w.Code != 202 {
		t.Fatalf("claim = %d %s", w.Code, w.Body)
	}
	list, _ := ListPendingClaims()
	if len(list) != 1 {
		t.Fatalf("%d pending", len(list))
	}
	noUnsafe(t, "label", list[0].Label)
	srv.flushCards()
	for _, o := range cards.opened {
		noUnsafe(t, "card label", o.Label)
		noUnsafe(t, "card origin", o.Origin)
	}
}

func TestResolvePairModeEnvOnlyTightens(t *testing.T) {
	cases := []struct {
		env, stored string
		set         bool
		mode, from  string
	}{
		{"", "", false, PairModeAsk, PairSourceDefault},
		{"", "auto", true, PairModeAuto, PairSourceSettings},
		{"auto", "", false, PairModeAsk, PairSourceDefault},
		{"auto", "ask", true, PairModeAsk, PairSourceSettings},
		{"auto", "auto", true, PairModeAuto, PairSourceSettings},
		{"ask", "auto", true, PairModeAsk, PairSourceEnv},
		{"off", "auto", true, PairModeOff, PairSourceEnv},
		{"0", "ask", true, PairModeOff, PairSourceEnv},
		{"garbage", "auto", true, PairModeOff, PairSourceEnv},
		{"garbage", "", false, PairModeOff, PairSourceEnv},
		{"yes", "ask", true, PairModeOff, PairSourceEnv},
		{"ask", "off", true, PairModeOff, PairSourceSettings},
		{" AUTO ", "auto", true, PairModeAuto, PairSourceSettings},
	}
	for _, c := range cases {
		t.Setenv(AutoPairEnv, c.env)
		mode, from := ResolvePairMode(c.stored, c.set)
		if mode != c.mode || from != c.from {
			t.Errorf("env %q stored %q(%v) = %s from %s, want %s from %s", c.env, c.stored, c.set, mode, from, c.mode, c.from)
		}
	}
}

func TestClaimReplyCarriesTheCodeThePollSecretMakes(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	rep := askClaim(t, srv, 1, extOrigin)
	if rep.Code == "" || rep.Code != PairCode(rep.ClaimID, rep.PollSecret) {
		t.Fatalf("code %q is not the code of the claim and its secret", rep.Code)
	}
	if rep.Code == PairCode(rep.ClaimID, strings.Repeat("0", 64)) {
		t.Fatal("the code does not depend on the secret")
	}
	list, _ := ListPendingClaims()
	if raw, _ := json.Marshal(list); strings.Contains(string(raw), rep.Code) || strings.Contains(string(raw), rep.PollSecret) {
		t.Fatalf("the pending file holds the code or the secret in the clear: %s", raw)
	}
}
