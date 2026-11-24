package record

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/recording"
)

// A prefix of an id finds a recording, but deleting needs the whole id.
func TestRecordDeleteRefusesAnIDPrefix(t *testing.T) {
	recordTestHome(t, "rec-a")
	all, err := recording.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %+v, %v", all, err)
	}
	id := filepath.Base(all[0].Dir)
	if _, err := runRecordCLI(t, true, "delete", id[:len(id)-2], "--yes"); err == nil {
		t.Fatal("delete by a prefix of the id succeeded")
	}
	if left, _ := recording.List(); len(left) != 1 {
		t.Fatalf("a prefix deleted the recording: %+v", left)
	}
	if _, err := runRecordCLI(t, true, "delete", id, "--yes"); err != nil {
		t.Fatalf("delete by the full id: %v", err)
	}
	if left, _ := recording.List(); len(left) != 0 {
		t.Fatalf("the full id did not delete: %+v", left)
	}
}
