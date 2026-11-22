package browserctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file that grew between Stat and read must not slip past the byte cap:
// the read itself is bounded by what is left of the total.
func TestReadUploadFileBoundsTheRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "grown.bin")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUploadFile(p, 10); err == nil {
		t.Error("100 bytes read against a 10 byte allowance: want an error")
	}
	data, err := readUploadFile(p, 100)
	if err != nil || len(data) != 100 {
		t.Errorf("exactly the allowance: got %d bytes, err %v", len(data), err)
	}
}
