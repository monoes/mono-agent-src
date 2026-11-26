package hil

import (
	"testing"

	"github.com/monoes/mono-agent/internal/hilsend"
)

// `hil approve|reject --if-digest` decides a workflow item only while it is as
// `hil list` showed it.
func TestDecisionsNamingAnOldDigestAreRefused(t *testing.T) {
	cfg, db, _ := hilSuggestEnv(t, false, nil)
	seedPending(t, db, "alpha", "bravo")
	items, err := runHIL(t, cfg, "list")
	if err != nil || len(items) != 2 || items[0].Digest == "" || items[0].Digest == items[1].Digest {
		t.Fatalf("list: %v %+v", err, items)
	}
	id, old := items[0].ID, items[0].Digest
	var ro, ed string
	if err := db.QueryRow(`SELECT readonly_data, editable_data FROM hil_pending WHERE id = ?`, id).Scan(&ro, &ed); err != nil || hilsend.Digest(ro, ed, "") != old {
		t.Fatalf("digest of the row: %v", err)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET editable_data = '{"caption":"changed"}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	status := func() (s string) {
		_ = db.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, id).Scan(&s)
		return s
	}
	for _, verb := range []string{"approve", "reject"} {
		_, err := runHIL(t, cfg, verb, id, "--if-digest", old)
		if exitCodeFor(err) != 3 || status() != "pending" {
			t.Fatalf("%s with the old digest: %v, status %s", verb, err, status())
		}
	}
	items, err = runHIL(t, cfg, "list")
	if err != nil || items[0].Digest == old {
		t.Fatalf("the digest did not move: %v %+v", err, items)
	}
	if _, err := runHIL(t, cfg, "approve", id, "--if-digest", items[0].Digest); err != nil || status() != "approved" {
		t.Fatalf("current digest: %v, status %s", err, status())
	}
	if _, err := runHIL(t, cfg, "reject", items[1].ID, "--if-digest", items[1].Digest); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := runHIL(t, cfg, "approve", id, "--if-digest", items[0].Digest); exitCodeFor(err) != 2 {
		t.Fatalf("a decided item: %v", err)
	}
}
