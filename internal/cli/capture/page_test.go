package capturecli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/extension"
)

// `capture page` captures through the extension bridge, which these tests never reach: the
// fake Deps.Page.Bridge fails the test when it is called (fixture_test.go).

// TestCapturePageRejectsBadFlagsBeforeOpeningChrome: validation must fail
// with the invalid-input exit code without ever reaching for the browser.
//
// The exit code alone does not say that. A run that got as far as Chrome
// and failed there is also an error, so each case pins the message to the
// flag it is about, and a message that talks about a browser, a daemon or
// a connection fails the test — those are the words of a command that went
// looking. The inbox is pointed somewhere disposable and checked too: a
// validation that ran too late could already have written a capture.
func TestCapturePageRejectsBadFlagsBeforeOpeningChrome(t *testing.T) {
	cases := []struct {
		args []string
		says string
	}{
		{[]string{"page", "--formats", "mhtml,wat"}, "--formats"},
		{[]string{"page", "--formats", ","}, "--formats"},
		{[]string{"page", "--tab", "-3"}, "--tab"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			home := t.TempDir()
			inbox := filepath.Join(home, "inbox")
			t.Setenv(capture.InboxEnv, inbox)
			t.Setenv(capture.HomeEnv, home)

			_, _, err := runGlueCmd(t, nil, tc.args...)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if got := exitCodeFor(err); got != 3 {
				t.Fatalf("exit code = %d (%v), want 3", got, err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error = %v, want it to name %s", err, tc.says)
			}
			// ("chrome" is not in this list: --tab's own refusal names the
			// Chrome tab id, which is the flag talking, not a browser.)
			for _, word := range []string{"browser", "launch", "connect", "daemon", "websocket"} {
				if strings.Contains(strings.ToLower(err.Error()), word) {
					t.Errorf("the command reached for the browser before validating: %v", err)
				}
			}
			if entries, _ := capture.List(inbox); len(entries) != 0 {
				t.Errorf("a refused capture wrote %d envelopes", len(entries))
			}
			if left, _ := os.ReadDir(home); len(left) != 0 {
				t.Errorf("a refused capture wrote %v", left)
			}
		})
	}
}

func TestNormalizeCaptureFormats(t *testing.T) {
	got, err := testDeps(&testConfig{}).normalizeCaptureFormats([]string{"MHTML", " readable ", "mhtml"})
	if err != nil {
		t.Fatalf("normalizeCaptureFormats: %v", err)
	}
	if len(got) != 2 || got[0] != "mhtml" || got[1] != "readable" {
		t.Fatalf("got = %v", got)
	}
	if got, err := testDeps(&testConfig{}).normalizeCaptureFormats(nil); err != nil || got != nil {
		t.Fatalf("nil formats = %v, %v; want nil, nil so the default set applies", got, err)
	}
	if _, err := testDeps(&testConfig{}).normalizeCaptureFormats([]string{"har"}); err == nil {
		t.Fatal("accepted an unknown format")
	}
}

// fakeBridge is an extension bridge whose browser captures every page as the same envelope.
type fakeBridge struct {
	connected bool
	got       extension.CaptureRequest
	fail      error
}

func (b *fakeBridge) IsConnected() bool                    { return b.connected }
func (b *fakeBridge) CreateTab(string) (int, error)        { return 0, errors.New("no tabs here") }
func (b *fakeBridge) CloseTab(int) error                   { return nil }
func (b *fakeBridge) NewPage(int) browserpkg.PageInterface { return nil }
func (b *fakeBridge) CapturePage(req extension.CaptureRequest) (*capture.Result, error) {
	b.got = req
	if b.fail != nil {
		b.connected = false
		return nil, b.fail
	}
	return &capture.Result{Path: "/inbox/x", Meta: capture.Meta{URL: "https://example.com/a"}, Artifacts: []string{"page.mhtml"}, Bytes: 2048}, nil
}

// pageDeps is testDeps with bridge as the extension bridge.
func pageDeps(cfg *testConfig, bridge browserpkg.ExtensionBridge) Deps {
	d := testDeps(cfg)
	d.Page = PageDeps{
		Bridge:             func() browserpkg.ExtensionBridge { return bridge },
		EnsureConnected:    func(browserpkg.ExtensionBridge, time.Duration) error { return nil },
		Advice:             " (advice)",
		LifetimeHint:       func(browserpkg.ExtensionBridge) string { return " (lifetime)" },
		CheckSummaryChoice: func(mode, runtime, model string) error { return nil },
	}
	return d
}

func runPage(d Deps, args ...string) (string, string, error) {
	cmd := NewCommand(d)
	cmd.SetArgs(append([]string{"page"}, args...))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestCapturePageThroughTheBridge(t *testing.T) {
	t.Parallel()
	b := &fakeBridge{connected: true}
	stdout, stderr, err := runPage(pageDeps(&testConfig{}, b), "--tab", "7", "--formats", "readable,MHTML", "--tag", "x")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "/inbox/x\n" || !strings.Contains(stderr, "Captured https://example.com/a (page.mhtml) — 2.0 KB") {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}
	if b.got.TabID != 7 || strings.Join(b.got.Formats, ",") != "mhtml,readable" || len(b.got.Tags) != 1 {
		t.Errorf("request = %+v", b.got)
	}

	stdout, _, err = runPage(pageDeps(&testConfig{JSONOutput: true}, &fakeBridge{connected: true}))
	if err != nil || !strings.Contains(stdout, `"path": "/inbox/x"`) {
		t.Errorf("--json: %q, %v", stdout, err)
	}
}

func TestCapturePageConnectionFailuresAreExitCode4(t *testing.T) {
	t.Parallel()
	// A bridge that cannot capture, and one that drops mid-capture.
	type notACapturer struct{ browserpkg.ExtensionBridge }
	_, _, err := runPage(pageDeps(&testConfig{}, notACapturer{&fakeBridge{}}))
	if exitCodeFor(err) != 4 || !strings.Contains(err.Error(), "cannot capture pages") || !strings.HasSuffix(err.Error(), "(advice)") {
		t.Errorf("not a capturer: %v", err)
	}
	_, _, err = runPage(pageDeps(&testConfig{}, &fakeBridge{connected: true, fail: errors.New("timed out")}))
	if exitCodeFor(err) != 4 || !strings.Contains(err.Error(), "no longer connected") || !strings.HasSuffix(err.Error(), "(lifetime)") {
		t.Errorf("dropped mid-capture: %v", err)
	}
	if _, _, err := runPage(pageDeps(&testConfig{}, &fakeBridge{connected: true}), "--mode", "nope"); exitCodeFor(err) != 3 {
		t.Errorf("--mode nope: %v", err)
	}
}
