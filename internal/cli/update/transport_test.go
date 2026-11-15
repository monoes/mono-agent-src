package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// within fails the test (instead of hanging it) when f takes longer than d.
func returnsWithin(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not return within %v: a stalled transfer hangs", d)
	}
}

// stallServer serves one chunk of the body and then goes silent until the
// test ends. paths: /bin, /sums and /api all behave the same.
func stallServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte(`{"tag_name":"v1",`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

func shrink(t *testing.T) {
	t.Helper()
	oldS, oldT, oldA := stallTimeout, totalTimeout, apiTimeout
	stallTimeout, totalTimeout, apiTimeout = 300*time.Millisecond, 5*time.Second, 300*time.Millisecond
	t.Cleanup(func() { stallTimeout, totalTimeout, apiTimeout = oldS, oldT, oldA })
}

func releaseFor(srv *httptest.Server, name string) *LatestRelease {
	rel := &LatestRelease{TagName: "v9.9.9"}
	add := func(n, p string) {
		rel.Assets = append(rel.Assets, struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{n, srv.URL + p})
	}
	add(name, "/bin")
	add(SumsAssetName, "/sums")
	return rel
}

func TestLegacyDownloadStalledBinaryErrors(t *testing.T) {
	shrink(t)
	srv := stallServer(t)
	returnsWithin(t, 5*time.Second, func() {
		if _, err := legacyDownload(releaseFor(srv, "bin-x"), "bin-x"); err == nil {
			t.Error("a stalled download must error")
		}
	})
}

func TestFetchLatestReleaseStalledErrors(t *testing.T) {
	shrink(t)
	srv := stallServer(t)
	returnsWithin(t, 5*time.Second, func() {
		if _, err := FetchLatestRelease(context.Background(), srv.URL); err == nil {
			t.Error("a stalled release lookup must error")
		}
	})
}

func TestLegacyDownloadRefusesOversizeBodies(t *testing.T) {
	// Both bodies verify cleanly; only their size is wrong.
	data := []byte(strings.Repeat("a", 200))
	padding := strings.Repeat(strings.Repeat("0", 64)+"  other-file\n", 5)
	sums := fmt.Sprintf("%s  bin-x\n", sha256Hex(data))
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(sums + padding)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldB, oldS := maxBinaryBytes, maxSmallBytes
	t.Cleanup(func() { maxBinaryBytes, maxSmallBytes = oldB, oldS })

	maxBinaryBytes, maxSmallBytes = 100, 1000
	if _, err := legacyDownload(releaseFor(srv, "bin-x"), "bin-x"); err == nil {
		t.Fatal("an oversize binary must be refused")
	}
	maxBinaryBytes, maxSmallBytes = 1000, 50
	if _, err := legacyDownload(releaseFor(srv, "bin-x"), "bin-x"); err == nil {
		t.Fatal("an oversize checksum list must be refused")
	}
	maxBinaryBytes, maxSmallBytes = 1000, 1000
	if _, err := legacyDownload(releaseFor(srv, "bin-x"), "bin-x"); err != nil {
		t.Fatalf("within the caps it must succeed: %v", err)
	}
}

func TestLegacyDownloadNormalUnchanged(t *testing.T) {
	data := []byte("the-binary")
	sums := fmt.Sprintf("%s  bin-x\n", sha256Hex(data))
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(sums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	got, err := legacyDownload(releaseFor(srv, "bin-x"), "bin-x")
	if err != nil || string(got) != string(data) {
		t.Fatalf("got %q, %v", got, err)
	}
}
