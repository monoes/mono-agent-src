package chat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

// fakeLauncher records what the gate passes on.
type fakeLauncher struct {
	calls   [][]any
	turn    coderhost.Turn
	err     error
	adopted []any
}

func (f *fakeLauncher) Start(p, c, turn, msg string, att []string, mt string) (coderhost.Turn, error) {
	f.calls = append(f.calls, []any{"Start", p, c, turn, msg, att, mt})
	return f.turn, f.err
}

func (f *fakeLauncher) StartCapped(p, c, turn, msg string, att []string, mt string, capUSD float64) (coderhost.Turn, error) {
	f.calls = append(f.calls, []any{"StartCapped", p, c, turn, msg, att, mt, capUSD})
	return f.turn, f.err
}

func (f *fakeLauncher) Adopt(pid int, stamp string) coderhost.Turn {
	f.adopted = append(f.adopted, pid, stamp)
	return f.turn
}

func (f *fakeLauncher) Finish(p, c, turn, reason string) {
	f.calls = append(f.calls, []any{"Finish", p, c, turn, reason})
}

type fakeTurn struct{ coderhost.Turn }

func gateRig(t *testing.T, rawSettings string) (*gatedLauncher, *fakeLauncher) {
	t.Helper()
	db, err := storage.NewDatabase(newChatCLITestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if rawSettings != "" {
		if _, err := db.DB.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data`, coderConfigName, rawSettings); err != nil {
			t.Fatal(err)
		}
	}
	inner := &fakeLauncher{turn: fakeTurn{}}
	return &gatedLauncher{inner: inner, db: db.DB}, inner
}

// With the setting off in every way it can be absent or false, the gate is a pass-through: the same calls reach the
// launcher with the same arguments and the launcher's own results come back, for Start, StartCapped, Adopt and Finish.
func TestTheGatePassesEverythingOnWhileTheSettingIsOff(t *testing.T) {
	for name, raw := range map[string]string{
		"no coder settings": "",
		"field absent":      `{"enabled":true}`,
		"explicit false":    `{"enabled":true,"peercredBinding":false}`,
		"null":              `{"enabled":true,"peercredBinding":null}`,
	} {
		g, inner := gateRig(t, raw)
		turn, err := g.Start("p", "c1", "t1", "hello", []string{"a.txt"}, "mission")
		if err != nil || turn == nil {
			t.Fatalf("%s: Start: %v %v", name, turn, err)
		}
		if _, err := g.StartCapped("p", "c1", "t2", "again", nil, "", 1.5); err != nil {
			t.Fatalf("%s: StartCapped: %v", name, err)
		}
		g.Adopt(42, "stamp")
		g.Finish("p", "c1", "t1", "done")
		want := [][]any{
			{"Start", "p", "c1", "t1", "hello", []string{"a.txt"}, "mission"},
			{"StartCapped", "p", "c1", "t2", "again", []string(nil), "", 1.5},
			{"Finish", "p", "c1", "t1", "done"},
		}
		if !reflect.DeepEqual(inner.calls, want) || !reflect.DeepEqual(inner.adopted, []any{42, "stamp"}) {
			t.Errorf("%s: the launcher saw %v / %v", name, inner.calls, inner.adopted)
		}
		// the launcher's own error is returned as it is
		inner.err = errors.New("the launcher failed")
		if _, err := g.Start("p", "c1", "t3", "x", nil, ""); err == nil || err.Error() != "the launcher failed" {
			t.Errorf("%s: the launcher's error was changed: %v", name, err)
		}
	}
}

// With the setting on, however it was written (including by hand, and including values that are not a plain true),
// no turn starts while the build cannot issue bound tokens, and the launcher is not called.
func TestTheGateRefusesEveryStartWhileTheSettingIsOn(t *testing.T) {
	if procbind.IssuanceReady {
		t.Skip("issuance is on")
	}
	for name, raw := range map[string]string{
		"true":            `{"peercredBinding":true}`,
		"a string":        `{"peercredBinding":"true"}`,
		"a number":        `{"peercredBinding":1}`,
		"an object":       `{"peercredBinding":{}}`,
		"yes":             `{"peercredBinding":"yes"}`,
		"on with others":  `{"enabled":true,"box":"strict","peercredBinding":true}`,
		"unreadable json": `{"peercredBinding":`,
	} {
		g, inner := gateRig(t, raw)
		for _, start := range []func() error{
			func() error { _, err := g.Start("p", "c1", "t1", "hi", nil, ""); return err },
			func() error { _, err := g.StartCapped("p", "c1", "t1", "hi", nil, "", 2); return err },
		} {
			err := start()
			var r *bindingRefusal
			if !errors.As(err, &r) {
				t.Errorf("%s: not refused: %v", name, err)
				continue
			}
			if strings.Contains(err.Error(), "/") || strings.Contains(strings.ToLower(err.Error()), "key") {
				t.Errorf("%s: the refusal names a path or a key: %q", name, err)
			}
			if name != "unreadable json" && !strings.Contains(err.Error(), "does not issue bound tokens") {
				t.Errorf("%s: the refusal does not say why: %q", name, err)
			}
			if name != "unreadable json" && !strings.Contains(err.Error(), "coder set --peercred-binding off") {
				t.Errorf("%s: the refusal does not say how to turn it off: %q", name, err)
			}
		}
		if len(inner.calls) != 0 {
			t.Errorf("%s: a launcher call got through: %v", name, inner.calls)
		}
	}
}

// The setting is read at every start: a host that began with it off refuses once it is switched on, and starts again
// once it is off.
func TestTheGateReadsTheSettingAtEveryStart(t *testing.T) {
	if procbind.IssuanceReady {
		t.Skip("issuance is on")
	}
	g, inner := gateRig(t, `{"enabled":true}`)
	if _, err := g.Start("p", "c", "t1", "m", nil, ""); err != nil {
		t.Fatal(err)
	}
	set := func(raw string) {
		if _, err := g.db.Exec(`UPDATE configs SET config_data = ? WHERE name = ?`, raw, coderConfigName); err != nil {
			t.Fatal(err)
		}
	}
	set(`{"enabled":true,"peercredBinding":true}`)
	if _, err := g.Start("p", "c", "t2", "m", nil, ""); err == nil {
		t.Fatal("a start went through after the setting was switched on")
	}
	set(`{"enabled":true}`)
	if _, err := g.Start("p", "c", "t3", "m", nil, ""); err != nil {
		t.Fatalf("a start was refused after the setting was switched off: %v", err)
	}
	if len(inner.calls) != 2 {
		t.Errorf("the launcher saw %d starts, want 2", len(inner.calls))
	}
}

// A database that cannot be read refuses (whether the setting is on is not known); it does not guess "off".
func TestTheGateRefusesWhenTheSettingCannotBeRead(t *testing.T) {
	g, inner := gateRig(t, "")
	g.db.Close()
	if _, err := g.Start("p", "c", "t1", "m", nil, ""); err == nil || len(inner.calls) != 0 {
		t.Fatalf("a start went through with an unreadable database: %v", err)
	}
}

// The setter: only the operator, only on or off, and "on" is refused while the build cannot issue bound tokens.
func TestThePeercredSettingCannotBeSwitchedOnYet(t *testing.T) {
	if procbind.IssuanceReady {
		t.Skip("issuance is on")
	}
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	if out, code := runCoderCLI(t, dbPath, "status"); code != 0 || strings.Contains(out, "peercredBinding") {
		t.Fatalf("default status (%d): %s", code, out)
	}
	for _, bad := range []string{"yes", "true", "", "ON"} {
		if _, code := runCoderCLI(t, dbPath, "set", "--peercred-binding="+bad); code != 3 {
			t.Errorf("--peercred-binding=%q accepted (exit %d)", bad, code)
		}
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=on"); code != 3 {
		t.Errorf("switching binding on was not refused (exit %d)", code)
	}
	if out, _ := runCoderCLI(t, dbPath, "status"); strings.Contains(out, `"peercredBinding":true`) {
		t.Errorf("the refused setter changed the setting: %s", out)
	}
	// off is accepted and clears a hand-edited true
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, PeercredBinding: true})
	if out, _ := runCoderCLI(t, dbPath, "status"); !strings.Contains(out, `"peercredBinding":true`) {
		t.Fatalf("status hides a hand-edited setting: %s", out)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=off"); code != 0 {
		t.Fatalf("switching binding off: exit %d", code)
	}
	if out, _ := runCoderCLI(t, dbPath, "status"); strings.Contains(out, `"peercredBinding":true`) {
		t.Errorf("off did not clear the setting: %s", out)
	}
}

// fakeService counts what the gate asks of the binding service.
type fakeService struct{ ensured, asked int }

func (f *fakeService) Ensure(_ context.Context, _ string) error { f.ensured++; return nil }
func (f *fakeService) Running(string) bool                      { f.asked++; return true }

// The service is never started while it is off or while this build cannot issue bound tokens: the gate asks
// procbind first and only then the service.
func TestTheGateNeverStartsTheServiceWhileItIsNotAllowed(t *testing.T) {
	if procbind.IssuanceReady {
		t.Skip("issuance is on")
	}
	for name, raw := range map[string]string{"off": `{"enabled":true}`, "on": `{"peercredBinding":true}`} {
		g, _ := gateRig(t, raw)
		svc := &fakeService{}
		g.svc = svc
		_, _ = g.Start("p", "c", "t", "m", nil, "")
		_, _ = g.StartCapped("p", "c", "t", "m", nil, "", 1)
		if svc.ensured != 0 || svc.asked != 0 {
			t.Errorf("%s: the service was asked (%d ensure, %d running)", name, svc.ensured, svc.asked)
		}
	}
}

// The setting is read in ONE conservative way by the gate, `coder status` and the setter: a duplicated key, a key in
// another letter case and a junk value all count as on if any occurrence is not a plain false or null.
func TestTheSettingIsReadTheSameConservativeWayEverywhere(t *testing.T) {
	if procbind.IssuanceReady {
		t.Skip("issuance is on")
	}
	for name, raw := range map[string]string{
		"true then null":         `{"peercredBinding":true,"peercredBinding":null}`,
		"true then false":        `{"peercredBinding":true,"peercredBinding":false}`,
		"false then true":        `{"peercredBinding":false,"peercredBinding":true}`,
		"other letter case":      `{"peercredBinding":false,"PeercredBinding":true}`,
		"junk then false":        `{"peercredBinding":"on","peercredBinding":false}`,
		"false then junk string": `{"peercredBinding":false,"peercredBinding":"yes"}`,
	} {
		g, inner := gateRig(t, raw)
		if _, err := g.Start("p", "c", "t", "m", nil, ""); err == nil || len(inner.calls) != 0 {
			t.Errorf("%s (gate): a start went through: %v", name, err)
		}
		var s coderSettings
		if err := json.Unmarshal([]byte(raw), &s); err != nil || !bool(s.PeercredBinding) {
			t.Errorf("%s (status decoding): on=%v err=%v", name, s.PeercredBinding, err)
		}
	}
	// plain false and null, in any number, stay off
	for name, raw := range map[string]string{
		"false twice":    `{"peercredBinding":false,"peercredBinding":false}`,
		"null and false": `{"peercredBinding":null,"peercredBinding":false}`,
	} {
		g, inner := gateRig(t, raw)
		if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
			t.Errorf("%s: a start was refused: %v", name, err)
		}
	}
}

// `coder set --peercred-binding off` overwrites any stored junk, including a duplicated key, and leaves the key once.
func TestOffOverwritesAJunkSetting(t *testing.T) {
	asOperator(t)
	for name, raw := range map[string]string{
		"a string":      `{"enabled":true,"peercredBinding":"on"}`,
		"a number":      `{"enabled":true,"peercredBinding":7}`,
		"an object":     `{"enabled":true,"peercredBinding":{"x":1}}`,
		"duplicated on": `{"enabled":true,"peercredBinding":true,"peercredBinding":true}`,
	} {
		dbPath := newChatCLITestDB(t)
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data`, coderConfigName, raw); err != nil {
			t.Fatal(err)
		}
		if out, _ := runCoderCLI(t, dbPath, "status"); !strings.Contains(out, `"peercredBinding":true`) {
			t.Errorf("%s: status does not show a junk setting as on: %s", name, out)
		}
		if _, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=off"); code != 0 {
			t.Errorf("%s: off was refused (exit %d)", name, code)
		}
		var stored string
		if err := db.DB.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, coderConfigName).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stored, "peercredBinding") {
			t.Errorf("%s: the stored settings still carry the key: %s", name, stored)
		}
		db.Close()
	}
}

// The production `coder host run` installs the gate: the host it builds launches through a gatedLauncher, which
// refuses a start when the setting is on. Putting the bare launcher back fails this test.
func TestTheProductionHostInstallsTheGate(t *testing.T) {
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(string) error { return nil }
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, PeercredBinding: true}) // hand-edited on
	builtc := make(chan *coderhost.Host, 1)
	old := hostBuilt
	hostBuilt = func(h *coderhost.Host) { builtc <- h }
	t.Cleanup(func() { hostBuilt = old })
	t.Cleanup(func() { RegisterChildTokenMinter(nil) }) // the host registers its launcher process-wide; later tests expect none
	ctx, cancel := context.WithCancel(context.Background())
	cmd := newCoderHostRunCmd(cfg)
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--poll=20ms"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	errc := make(chan error, 1)
	go func() { errc <- cmd.Execute() }()
	var built *coderhost.Host
	select {
	case built = <-builtc:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatalf("the host was not built: %v", <-errc)
	}
	g, ok := built.L.(*gatedLauncher)
	if !ok {
		cancel()
		<-errc
		t.Fatalf("the production host launches through %T, not the gate", built.L)
	}
	if _, err := g.Start("default", "c", "t", "m", nil, ""); err == nil {
		t.Error("the production gate let a start through with the setting on")
	}
	cancel()
	<-errc
}

// Valid JSON with another field of the wrong type is a settings object that cannot be read as a whole: the gate
// refuses, whatever the one field it cares about says.
func TestTheGateRefusesWhenTheWholeSettingsObjectIsUnreadable(t *testing.T) {
	for name, raw := range map[string]string{
		"junk field, binding false": `{"enabled":"junk","peercredBinding":false}`,
		"junk field, no binding":    `{"maxTurns":"many"}`,
		"junk field, binding null":  `{"timeout":7,"peercredBinding":null}`,
	} {
		g, inner := gateRig(t, raw)
		if _, err := g.Start("p", "c", "t", "m", nil, ""); err == nil || len(inner.calls) != 0 {
			t.Errorf("%s: a start went through: %v", name, err)
		}
	}
}

// Two writers of the settings cannot undo each other: each reads and writes inside one write transaction, so a
// `coder set --box` racing an operator's peer-credential change keeps both. A barrier makes both read before either
// writes, if the transaction lets them.
func TestConcurrentSettingsWritersKeepEachOthersChanges(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	for round := 0; round < 10; round++ {
		var arrived sync.WaitGroup
		arrived.Add(2)
		meet := func() { // both readers have read; gives up after a while if the other cannot get that far
			arrived.Done()
			done := make(chan struct{})
			go func() { arrived.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
			}
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := updateCoderSettings(context.Background(), db.DB, func(s *coderSettings) error { meet(); s.Box = BoxCompat; return nil })
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := updateCoderSettings(context.Background(), db.DB, func(s *coderSettings) error { meet(); s.PeercredBinding = true; return nil })
			if err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		got, err := loadCoderSettings(db.DB)
		if err != nil || got.Box != BoxCompat || !bool(got.PeercredBinding) {
			t.Fatalf("round %d: one writer undid the other: box=%q binding=%v (%v)", round, got.Box, got.PeercredBinding, err)
		}
		// reset for the next round
		if _, err := updateCoderSettings(context.Background(), db.DB, func(s *coderSettings) error { s.Box, s.PeercredBinding = "", false; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// a change that fails leaves the stored settings as they were
	before, _ := loadCoderSettings(db.DB)
	if _, err := updateCoderSettings(context.Background(), db.DB, func(s *coderSettings) error { s.Box = BoxStrict; return errors.New("refused") }); err == nil {
		t.Fatal("the failing change succeeded")
	}
	if after, _ := loadCoderSettings(db.DB); after.Box != before.Box {
		t.Errorf("a failed change was stored: %q", after.Box)
	}
}
