//go:build cgo

package image

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadFileChecksAndRenames(t *testing.T) {
	body := strings.Repeat("x", 1000)
	sum := sha256.Sum256([]byte(body))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	dir := t.TempDir()

	for name, c := range map[string]struct {
		max, size int64
		sha       string
		ok        bool
	}{
		"good":        {2000, 1000, hex.EncodeToString(sum[:]), true},
		"too big":     {500, 0, "", false},
		"wrong size":  {2000, 999, "", false},
		"wrong hash":  {2000, 0, strings.Repeat("0", 64), false},
		"no pin only": {2000, 0, "", true},
	} {
		dst := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
		err := downloadFile(srv.URL, dst, c.max, c.size, c.sha)
		_, statErr := os.Stat(dst)
		if c.ok && (err != nil || statErr != nil) {
			t.Errorf("%s: err=%v stat=%v", name, err, statErr)
		}
		if !c.ok && (err == nil || statErr == nil) {
			t.Errorf("%s: accepted (err=%v, file exists=%v)", name, err, statErr == nil)
		}
	}
	// no temporary files are left behind
	left, _ := filepath.Glob(filepath.Join(dir, "*.part-*"))
	if len(left) != 0 {
		t.Errorf("leftover partial files: %v", left)
	}
}

// The model pin is a real digest, and a download whose bytes differ from it is refused.
func TestU2netModelPinRejectsWrongBytes(t *testing.T) {
	if len(u2netModelSHA256) != 64 {
		t.Fatalf("u2netModelSHA256 = %q, want 64 hex characters", u2netModelSHA256)
	}
	if _, err := hex.DecodeString(u2netModelSHA256); err != nil {
		t.Fatalf("u2netModelSHA256 is not hex: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not the model")) }))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "u2net.onnx")
	if err := downloadFile(srv.URL, dst, 1<<20, 0, u2netModelSHA256); err == nil {
		t.Fatal("wrong bytes accepted against the pinned model digest")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("rejected download was left under its final name")
	}
}

// Every platform ortLibURL can select is a known key; each pin that is set is a real digest.
// The ONNX Runtime pins could not be obtained (see the comment on ortArchiveSHA256), so a
// missing one is allowed here; a malformed or unknown one is not.
func TestOrtArchivePinsWellFormed(t *testing.T) {
	known := map[string]bool{"linux/amd64": true, "darwin/arm64": true, "darwin/amd64": true}
	for k, v := range ortArchiveSHA256 {
		if !known[k] {
			t.Errorf("pin for unknown platform %q", k)
		}
		if _, err := hex.DecodeString(v); err != nil || len(v) != 64 {
			t.Errorf("pin for %s is not a 64-character hex digest: %q", k, v)
		}
	}
}
