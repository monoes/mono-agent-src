package vault

import (
	"context"
	"testing"
)

// Audio and video discovery is off until a profile switches it on, and the
// switch is per profile.
func TestScanAVIsOffByDefaultAndPerProfile(t *testing.T) {
	db := newVaultTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if on, err := ScanAVEnabled(ctx, db, "p1"); err != nil || on {
		t.Fatalf("fresh profile: on=%v err=%v, want off", on, err)
	}
	if err := SetScanAV(ctx, db, "p1", true); err != nil {
		t.Fatal(err)
	}
	if on, _ := ScanAVEnabled(ctx, db, "p1"); !on {
		t.Fatal("p1 should be on after SetScanAV(true)")
	}
	if on, _ := ScanAVEnabled(ctx, db, "p2"); on {
		t.Fatal("p2 must stay off: the switch is per profile")
	}
	if err := SetScanAV(ctx, db, "p1", false); err != nil {
		t.Fatal(err)
	}
	if on, _ := ScanAVEnabled(ctx, db, "p1"); on {
		t.Fatal("p1 should be off again")
	}
	if err := SetScanAV(ctx, db, "", true); err == nil {
		t.Fatal("an empty profile must be refused")
	}
}
