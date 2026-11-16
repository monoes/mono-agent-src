package update

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewerRelease(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v0.73.0", "v0.72.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v0.72.0", "v0.72.0", false},
		{"v0.71.0", "v0.72.0", false}, // local is newer: not an "update"
		{"v1.0.0", "0.99.1", true},
		{"v0.73.0", "dev", false},
		{"v0.73.0", "v0.72.0-4-gabc1234", false},
		{"", "v0.72.0", false},
	} {
		if got := newerRelease(c.latest, c.current); got != c.want {
			t.Errorf("newerRelease(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func runUpdateCheckCmd(t *testing.T, d Deps, args ...string) (Check, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := NewCommand(d)
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var res Check
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
			t.Fatalf("not JSON: %v\n%s", jerr, out.String())
		}
	}
	return res, err
}

func TestUpdateCheckReportsWithoutDownloading(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"tag_name":"v0.73.0","html_url":"https://example.test/r/v0.73.0","assets":[{"name":"x","browser_download_url":"https://example.test/x"}]}`))
	}))
	defer srv.Close()
	d := testDeps(t, srv.URL+"/latest")

	res, err := runUpdateCheckCmd(t, d, "--check", "--current", "v0.72.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpdateAvailable || res.LatestVersion != "v0.73.0" || res.CurrentVersion != "v0.72.0" || res.ReleaseURL != "https://example.test/r/v0.73.0" {
		t.Fatalf("res = %+v", res)
	}
	mu.Lock()
	n := len(paths)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("--check must only read the release, got requests %v", paths)
	}
	if res, _ := runUpdateCheckCmd(t, d, "--check", "--current", "v0.73.0"); res.UpdateAvailable {
		t.Fatalf("same version reported as an update: %+v", res)
	}
}

func TestUpdateCheckNetworkFailureIsAResult(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	res, err := runUpdateCheckCmd(t, testDeps(t, srv.URL), "--check", "--current", "v0.72.0")
	if err != nil || res.Error == "" || res.UpdateAvailable {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func closedServerURL() string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens any more: the dial is refused
	return url
}

func TestUpdateCheckUnreachableServerIsActionable(t *testing.T) {
	t.Parallel()
	res, err := runUpdateCheckCmd(t, testDeps(t, closedServerURL()), "--check", "--current", "v0.72.0")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Error != "could not reach the update server; check your connection" {
		t.Fatalf("error = %q", res.Error)
	}
	if !strings.Contains(res.Detail, "connect") {
		t.Fatalf("the raw error must stay in detail, got %q", res.Detail)
	}
}

func TestUpdateUnreachableServerPlainMessage(t *testing.T) {
	t.Parallel()
	d := testDeps(t, closedServerURL())
	d.JSONOutput = func() bool { return false }
	cmd := NewCommand(d)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--check", "--current", "v0.72.0"})
	err := cmd.Execute()
	if err == nil || err.Error() != "could not reach the update server; check your connection" {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateHTTPFailureKeepsItsMessage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	res, _ := runUpdateCheckCmd(t, testDeps(t, srv.URL), "--check", "--current", "v0.72.0")
	if !strings.Contains(res.Error, "403") || res.Detail != "" {
		t.Fatalf("res = %+v", res)
	}
}

func TestUpdateCurrentNeedsCheck(t *testing.T) {
	t.Parallel()
	if _, err := runUpdateCheckCmd(t, testDeps(t, "http://127.0.0.1:1"), "--current", "v1"); !isInvalidInput(err) {
		t.Fatalf("err = %v, want invalid input", err)
	}
}
