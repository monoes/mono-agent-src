package update

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func plainUpdate(t *testing.T, d Deps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCommand(d)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// R15-08: a dev build is recognised before any network call, and --json gets one result document.
func TestPlainUpdateDevBuildMakesNoRequestAndReportsJSON(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()
	d := testDeps(t, srv.URL)
	d.Version = func() string { return "v0.72.0-3-gabc123" }
	out, _, err := plainUpdate(t, d)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatalf("a dev build made %d requests", hits.Load())
	}
	var res AppResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.Success || !res.UpToDate {
		t.Fatalf("stdout %q res %+v err %v", out, res, err)
	}
}

func TestPlainUpdateUpToDateJSONKeepsStdoutToOneDocument(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()
	d := testDeps(t, srv.URL)
	d.Version = func() string { return "v1.0.0" }
	out, errOut, err := plainUpdate(t, d)
	if err != nil {
		t.Fatal(err)
	}
	var res AppResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.Success || !res.UpToDate {
		t.Fatalf("stdout %q res %+v err %v", out, res, err)
	}
	if !strings.Contains(errOut, "Already on latest version") {
		t.Fatalf("progress belongs on stderr, got %q", errOut)
	}
}

// R15-05: the binary an update replaces is the running executable, not whatever argv[0] finds on PATH.
// Not parallel: it changes os.Args.
func TestSelfBinaryPathIsTheRunningExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	saved := os.Args[0]
	os.Args[0] = "no-such-monoagentcli-for-this-test"
	t.Cleanup(func() { os.Args[0] = saved })
	got, err := selfBinaryPath()
	if err != nil || got != exe {
		t.Fatalf("selfBinaryPath() = %q, %v; want %q", got, err, exe)
	}
}

// F4: the legacy download takes only https URLs on GitHub's hosts (loopback http for tests).
func TestLegacyDownloadRefusesForeignHosts(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"https://evil.example/monoagentcli", "http://github.com/x", "http://example.com/x", "ftp://github.com/x"} {
		if _, err := getCapped(u, 1<<20); err == nil || !strings.Contains(err.Error(), "release failed verification") {
			t.Errorf("%s: err = %v, want a refusal", u, err)
		}
	}
}
