//go:build doomsday

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// A database that is present but cannot be opened (here: not a database) is an error for the control room's read
// verbs, never "off": a run may be live behind it. The exit is non-zero, and start still starts nothing.
func TestControlRoomOnAnUnopenableDatabaseIsAnErrorNotOff(t *testing.T) {
	// The real engine constructor, on a database file that is not a database (the operator gate reads the healthy
	// test database, so the verbs are run with an engine built from the broken one).
	broken := sendEnv(t)
	broken.DBPath = filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(broken.DBPath, []byte(strings.Repeat("this is not a sqlite database ", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, readEng := doomsdayEngine(broken), doomsdayReadEngine(broken)
	for _, e := range []doomsdaycli.Engine{eng, readEng} {
		if _, ok := e.(doomsdaycli.Unreadable); !ok {
			t.Fatalf("an unopenable database gave %T, not the unreadable engine", e)
		}
	}
	real, realRead := doomsdayEngine, doomsdayReadEngine
	doomsdayEngine = func(*globalConfig) doomsdaycli.Engine { return eng }
	doomsdayReadEngine = func(*globalConfig) doomsdaycli.Engine { return readEng }
	defer func() { doomsdayEngine, doomsdayReadEngine = real, realRead }()
	for _, verb := range doomsdaycli.ControlRoomVerbs {
		cfg := sendEnv(t)
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), verb)
		var doc map[string]any
		if err == nil || json.Unmarshal([]byte(out), &doc) != nil || doc["available"] != false || doc["reason"] != "error" || doc["error_code"] != "db_unavailable" {
			t.Fatalf("%s: %v\n%s", verb, err, out)
		}
	}
	cfg := sendEnv(t)
	if _, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), "start", "--charter", writeDoomsdayCharter(t), "--yes-i-lift", "--hash", strings.Repeat("a", 64)); err == nil {
		t.Fatal("start went ahead on an unopenable database")
	}
}

func TestClientModeOpenFailureIsOffNotAnError(t *testing.T) {
	if _, ok := engineForOpenFailure(errClientModeDB).(doomsdaycli.Off); !ok {
		t.Fatal("client mode is not the mode off")
	}
}
