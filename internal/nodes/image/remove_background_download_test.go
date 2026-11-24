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
