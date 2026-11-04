package coderhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lenient scan (the recovery verbs) skips an unreadable live-host entry and still judges the entries
// after it; the strict scan stops at it. The unreadable entry sorts first on purpose.
var errUnreadable = errors.New("file is not a database")

func TestTheLenientScanFindsATurnPastAnUnreadableEntryAndTheStrictScanStopsAtIt(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	base := t.TempDir()
	junk, turn, clean := filepath.Join(base, "a-junk.db"), filepath.Join(base, "z-turn.db"), filepath.Join(base, "m-clean.db")
	for _, p := range []string{junk, turn, clean} {
		writeEntry(t, p, "x", 0o600)
	}
	for _, p := range []string{junk, turn, clean} {
		registerRaw(t, dir, canonicalOrSelf(t, p), os.Getpid(), ProcStamp(os.Getpid()))
	}
	prev := registeredDBCheck
	t.Cleanup(func() { registeredDBCheck = prev })
	registeredDBCheck = func(p string) (bool, error) {
		switch filepath.Base(p) {
		case "a-junk.db":
			return false, errUnreadable
		case "z-turn.db":
			return true, nil
		}
		return false, nil
	}

	if in, skipped, _ := CallerInARegisteredTurnLenient(""); !in || len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "a-junk.db") {
		t.Fatalf("lenient: in=%v skipped=%v; want the turn found and the junk entry reported", in, skipped)
	}
	if in, err := CallerInARegisteredTurn(""); in || err == nil || !strings.Contains(err.Error(), "a-junk.db") {
		t.Fatalf("strict: in=%v err=%v; want the unreadable entry to refuse", in, err)
	}

	registeredDBCheck = func(p string) (bool, error) {
		if filepath.Base(p) == "a-junk.db" {
			return false, errUnreadable
		}
		return false, nil
	}
	if in, skipped, _ := CallerInARegisteredTurnLenient(""); in || len(skipped) != 1 {
		t.Fatalf("lenient with no readable turn: in=%v skipped=%v; want not in a turn, one entry skipped", in, skipped)
	}
}
