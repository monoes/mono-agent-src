package bridge

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The refusal reaches both wait loops of EnsureConnected (a browser already running, and one it
// launches): nothing is opened and no nonce is minted for a non-operator; the operator's open stays.
func TestEnsureConnected_PairingGateInBothWaitLoops(t *testing.T) {
	prevInst, prevRun, prevFind := isExtensionInstalled, isChromeRunning, findLocalChromePath
	t.Cleanup(func() { isExtensionInstalled, isChromeRunning, findLocalChromePath = prevInst, prevRun, prevFind })
	isExtensionInstalled = func() bool { return true }
	findLocalChromePath = func() string { return "/bin/true" } // the "launched browser" exits at once

	for _, running := range []bool{true, false} {
		for _, operator := range []bool{false, true} {
			urls := stubOpener(t)
			isChromeRunning = func() bool { return running }
			b := &countingBridge{fakePairingBridge: fakePairingBridge{ready: true, url: "http://127.0.0.1:9222/monoagent/pair?n=abc"}}
			h := Hints{Route: func(ConnChecker) string { return "" }, RouteWaitPoll: time.Millisecond,
				Lifetime: func(ConnChecker) string { return "" },
				Refusal: func() string {
					if operator {
						return ""
					}
					return "this process runs inside a coder turn"
				}}
			captureStderr(t, func() { _ = EnsureConnected(b, 50*time.Millisecond, h) })
			if operator && (len(*urls) != 1 || b.mints == 0) {
				t.Errorf("running=%v operator: opened %v, want one open", running, *urls)
			}
			if !operator && (len(*urls) != 0 || b.mints != 0) {
				t.Errorf("running=%v non-operator: opened %v, minted %d", running, *urls, b.mints)
			}
		}
	}
}

// countingBridge counts how often a pairing nonce is minted (PairingURL is what mints it).
type countingBridge struct {
	fakePairingBridge
	mints int
}

func (c *countingBridge) PairingURL() (string, bool) {
	c.mints++
	return c.fakePairingBridge.PairingURL()
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	f()
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func stubOpener(t *testing.T) *[]string {
	t.Helper()
	var urls []string
	orig := openURLInBrowser
	openURLInBrowser = func(u string) error { urls = append(urls, u); return nil }
	t.Cleanup(func() { openURLInBrowser = orig })
	return &urls
}

// A browser command run from a coder turn (or any non-operator process) must not open the
// one-time pairing page, nor mint a nonce for it; the operator is told the one command to run.
func TestTryOpenPairingPage_RefusedToNonOperator(t *testing.T) {
	urls := stubOpener(t)
	refusal := func() string { return "this process runs inside a coder turn" }
	b := &countingBridge{fakePairingBridge: fakePairingBridge{ready: true, url: "http://127.0.0.1:9222/monoagent/pair?n=abc"}}
	opened := false

	out := captureStderr(t, func() { tryOpenPairingPage(b, &opened, refusal) })

	if len(*urls) != 0 {
		t.Fatalf("pairing page opened for a non-operator caller: %v", *urls)
	}
	if b.mints != 0 {
		t.Fatalf("a pairing nonce was minted for a non-operator caller (%d)", b.mints)
	}
	if !opened {
		t.Fatalf("a refused caller must stop retrying (opened=true) so the refusal prints once")
	}
	if !strings.Contains(out, "monoagentcli extension pair --open") || !strings.Contains(out, "coder turn") {
		t.Fatalf("refusal must name the reason and the command to run, got %q", out)
	}

	// still refused, and silent, on later polls
	out = captureStderr(t, func() { tryOpenPairingPage(b, &opened, refusal) })
	if len(*urls) != 0 || b.mints != 0 || out != "" {
		t.Fatalf("later poll acted: urls=%v mints=%d out=%q", *urls, b.mints, out)
	}
}

// The operator's own terminal keeps the automatic open.
func TestTryOpenPairingPage_OperatorStillOpens(t *testing.T) {
	urls := stubOpener(t)
	refusal := operatorOnly
	b := &fakePairingBridge{ready: true, url: "http://127.0.0.1:9222/monoagent/pair?n=abc"}
	opened := false

	captureStderr(t, func() { tryOpenPairingPage(b, &opened, refusal) })

	if len(*urls) != 1 || (*urls)[0] != b.url {
		t.Fatalf("operator: opened %v, want [%q]", *urls, b.url)
	}
}

// Without a wired operator check the open is refused (fail closed).
func TestTryOpenPairingPage_NilRefusalRefuses(t *testing.T) {
	urls := stubOpener(t)
	b := &countingBridge{fakePairingBridge: fakePairingBridge{ready: true, url: "http://127.0.0.1:9222/monoagent/pair?n=abc"}}
	opened := false
	captureStderr(t, func() { tryOpenPairingPage(b, &opened, nil) })
	if len(*urls) != 0 || b.mints != 0 {
		t.Fatalf("opened %v / minted %d with no operator check", *urls, b.mints)
	}
}
