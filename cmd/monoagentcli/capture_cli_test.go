package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/storage"
)

// The capture commands' tests live in internal/cli/capture. `capture page` and `capture task`
// stay in this package, and so do their tests and the helpers they share.

// runCaptureCmd executes a `capture` subcommand with stdout captured.
func runCaptureCmd(t *testing.T, cfg *globalConfig, args ...string) (string, error) {
	t.Helper()
	if cfg == nil {
		cfg = &globalConfig{}
	}
	cmd := newCaptureCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return out.String(), err
}

// runGlueCmd executes a `capture` subcommand, returning stdout and stderr
// separately: the machine-readable answer goes to stdout, the commentary to
// stderr, and a test that conflates them cannot tell.
func runGlueCmd(t *testing.T, cfg *globalConfig, args ...string) (string, string, error) {
	t.Helper()
	if cfg == nil {
		cfg = &globalConfig{}
	}
	cmd := newCaptureCmd(cfg)
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// seedGlueInbox writes captures and returns the inbox holding them.
func seedGlueInbox(t *testing.T, metas ...capture.Meta) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(base, "inbox")
	for _, meta := range metas {
		w := &capture.Writer{Inbox: inbox}
		if _, err := w.Write(&capture.Envelope{
			Meta: meta,
			Artifacts: map[string]capture.Artifact{
				capture.ArtifactMHTML:    capture.Inline([]byte("archive of " + meta.URL)),
				capture.ArtifactReadable: capture.Inline([]byte("# " + meta.Title + "\n\nbody\n")),
			},
		}); err != nil {
			t.Fatalf("seed %s: %v", meta.URL, err)
		}
	}
	return inbox
}

// glueFiles reads a capture directory as name -> bytes. Counting
// directories proves an import created something; only the bytes prove it
// restored a capture.
func glueFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("%s holds a subdirectory %q; an envelope is flat", dir, e.Name())
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(body)
	}
	if len(out) == 0 {
		t.Errorf("%s is empty", dir)
	}
	return out
}

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

			_, err := runCaptureCmd(t, nil, tc.args...)
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
	got, err := normalizeCaptureFormats([]string{"MHTML", " readable ", "mhtml"})
	if err != nil {
		t.Fatalf("normalizeCaptureFormats: %v", err)
	}
	if len(got) != 2 || got[0] != "mhtml" || got[1] != "readable" {
		t.Fatalf("got = %v", got)
	}
	if got, err := normalizeCaptureFormats(nil); err != nil || got != nil {
		t.Fatalf("nil formats = %v, %v; want nil, nil so the default set applies", got, err)
	}
	if _, err := normalizeCaptureFormats([]string{"har"}); err == nil {
		t.Fatal("accepted an unknown format")
	}
}

// TestCaptureCommandIsRegistered guards the wiring: a command that exists
// but is never added to the root tree is invisible to every caller.
func TestCaptureCommandIsRegistered(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"capture"}, {"capture", "page"}, {"capture", "list"}, {"capture", "classify"}, {"capture", "export"}, {"capture", "import"}, {"capture", "task"}} {
		sub, _, err := root.Find(path)
		if err != nil || sub == nil || sub.Name() != path[len(path)-1] {
			t.Fatalf("root.Find(%v) = %v, %v", path, sub, err)
		}
	}
}

// withJevDB and enableJev are the capture classify tests' helpers the people classify tests
// share.
func withJevDB(t *testing.T, dbPath string, fn func(db *storage.Database)) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

func enableJev(t *testing.T, dbPath, profile string, s jevconf.Surface) {
	t.Helper()
	withJevDB(t, dbPath, func(db *storage.Database) {
		if err := jevconf.SetEnabled(db.DB, profile, s, true); err != nil {
			t.Fatal(err)
		}
	})
}
