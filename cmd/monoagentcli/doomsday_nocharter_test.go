package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/account/accounttest"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// With no charter (no run ever started) every verb of the open-ended mode is inert, in a build with the mode compiled out and
// in one with it wired: the same words, exit codes and non-effects through the real command tree. Nothing is created under
// HOME, no database row changes, and no pinned connection (inotify watch) is made by a verb that only reads or refuses.
// The text of the wired build is in doomsday_built_test.go.

// homeFiles lists the files under HOME except the three a start of the process may create in a fresh HOME whatever the command
// (automations/index.json, .lock and .claude_init: controlroom.go documents them), so a difference is the verb's own.
func homeFiles(t *testing.T, home string) string {
	t.Helper()
	var out []string
	for _, f := range filesUnder(t, home) {
		switch f {
		case ".monoagent/.claude_init", ".monoagent/.lock", ".monoagent/automations/index.json":
		default:
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
}

// noPins refuses every pinned connection and counts the attempts (the pin map is process-global: a test must neither make a
// real one nor leave one behind).
func noPins(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	real := doomsdaycli.PinFunc
	doomsdaycli.PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) {
		n.Add(1)
		return nil, errors.New("pins are refused in this test")
	}
	t.Cleanup(func() { doomsdaycli.PinFunc = real; doomsdaycli.ClosePins() })
	return &n
}

// countPins counts the attempts and passes them to the real pin, for the verbs whose real no-charter answer is behind it
// (a failing fake would answer for them). Pins made are closed at the end of the test.
func countPins(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	real := doomsdaycli.PinFunc
	doomsdaycli.PinFunc = func(ctx context.Context, o stop.PinOptions) (*stop.Pinned, error) {
		n.Add(1)
		return real(ctx, o)
	}
	t.Cleanup(func() { doomsdaycli.PinFunc = real; doomsdaycli.ClosePins() })
	return &n
}

// tableCounts is a snapshot of the database: for every table the row count and a checksum of all its rows' contents, and (under
// the key "(files)") the files next to the database apart from the database itself and its -wal/-shm, so a stop marker or any
// stray file written beside it shows. Read on a connection of its own.
func tableCounts(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string]string{}
	for _, n := range names {
		rs, err := db.Query(`SELECT * FROM "` + n + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rs.Columns()
		var lines []string
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprintf("%v", vals))
		}
		rs.Close()
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		out[n] = fmt.Sprintf("%d rows, %x", len(lines), sum[:8])
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	var files []string
	for _, e := range entries {
		switch e.Name() {
		// the migrator's lock file is made by any command that opens the database for writing (a wired engine does)
		case filepath.Base(path), filepath.Base(path) + "-wal", filepath.Base(path) + "-shm", filepath.Base(path) + ".migrate.lock":
		default:
			files = append(files, e.Name())
		}
	}
	out["(files)"] = strings.Join(files, ",")
	return out
}

func requireSameCounts(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	var diff []string
	for n, b := range before {
		if after[n] != b {
			diff = append(diff, n)
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("%s changed the contents of %v (files: before [%s] after [%s])", label, diff, before["(files)"], after["(files)"])
	}
}

type noCharterVerb struct {
	name  string
	args  []string
	gated bool // an operator-only verb: an agent's process is refused with exit 3
	// readOnlyEngine: the verb reads through the read engine or a reader and must make no pinned connection.
	readOnlyEngine bool
	// realPin: the verb's real no-charter answer is behind the containment probe, which needs the real pin, so the test counts
	// the attempts and lets them through instead of refusing them (a refusing fake would be the answer, and vacuous).
	realPin bool
	// want checks the answer of the user's own process with nothing started.
	want func(t *testing.T, code int, stdout, stderr string)
}

func wantRefusal(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code == 0 || code == 4 || strings.Contains(stdout, "operator_only") {
		t.Errorf("exit %d (stdout %q, stderr %q): want a refusal of its own, neither success nor the operator or sign-in refusal", code, stdout, stderr)
	}
	claimsAStop := strings.Contains(stdout, "stopped") && !strings.Contains(stdout, "NOT")
	if claimsAStop || strings.Contains(stdout, "started") || strings.Contains(stdout, `"run_id"`) {
		t.Errorf("stdout %q claims a run started or stopped", stdout)
	}
}

// wantRefusedOrNotAccepted is the begin of a finish: refused as an error (the mode off), or answered with accepted:false (a
// build with the mode, no running run); never accepted.
func wantRefusedOrNotAccepted(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	var doc map[string]any
	if json.Unmarshal([]byte(stdout), &doc) == nil && doc["accepted"] == false && code == 0 {
		return
	}
	wantRefusal(t, code, stdout, stderr)
}

// wantNoCharterRefusal is start/render with no charter: the build's own refusal (startRenderNoCharterRefusal), never the
// pin refusal of a test's fake.
func wantNoCharterRefusal(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	wantRefusal(t, code, stdout, stderr)
	if strings.Contains(stdout+stderr, "pins are refused in this test") {
		t.Errorf("the refusal is the test's own pin refusal, not the verb's: %q", stderr)
	}
	if strings.Contains(stderr, "systemd-run") || strings.Contains(stderr, "transient") {
		t.Errorf("the real containment probe answered: %q", stderr)
	}
	if !regexp.MustCompile(startRenderNoCharterRefusal).MatchString(stderr) {
		t.Errorf("stderr %q does not match the build's refusal %q", stderr, startRenderNoCharterRefusal)
	}
}

func wantUnavailableDoc(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	var doc map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &doc) != nil || doc["available"] != false || doc["reason"] != controlRoomNoRunReason {
		t.Errorf("exit %d, stdout %q, stderr %q: want available:false reason %q, exit 0", code, stdout, stderr, controlRoomNoRunReason)
	}
}

func wantText(sub string) func(*testing.T, int, string, string) {
	return func(t *testing.T, code int, stdout, stderr string) {
		t.Helper()
		if code != 0 || !strings.Contains(stdout, sub) || stderr != "" {
			t.Errorf("exit %d, stdout %q, stderr %q: want exit 0, %q, nothing on stderr", code, stdout, stderr, sub)
		}
	}
}

func noCharterVerbs(t *testing.T) []noCharterVerb {
	vs := []noCharterVerb{
		{"start", []string{"start"}, true, false, true, wantNoCharterRefusal},
		{"stop", []string{"stop"}, true, false, false, wantRefusal},
		{"finish", []string{"finish"}, true, false, false, wantRefusal},
		{"finish --begin", []string{"finish", "--begin"}, true, false, false, wantRefusedOrNotAccepted},
		{"rubric", []string{"rubric", "c1", "approve", "--dossier", "abcdef12"}, true, false, false, wantRefusal},
		{"render", []string{"render"}, false, false, true, wantNoCharterRefusal},
		{"overview", []string{"overview"}, true, true, false, wantUnavailableDoc},
		{"feed", []string{"feed"}, true, true, false, wantUnavailableDoc},
		{"status", []string{"status"}, false, false, false, wantText(`"active":false`)},
		{"strategy status", []string{"strategy", "status"}, false, true, false, wantText(`"off":true`)},
		{"strategy epochs", []string{"strategy", "epochs"}, false, true, false, wantText(`"off":true`)},
		{"strategy directions", []string{"strategy", "directions"}, false, true, false, wantText(`"off":true`)},
	}
	if governorVerbExpected {
		vs = append(vs, noCharterVerb{"governor status", []string{"governor", "status"}, false, true, false, wantText(`"off":true`)})
	}
	return vs
}

// The user's own process, signed in, no charter: each verb answers as above, exit code included, and writes nothing.
func TestEveryDoomsdayVerbWithNoCharterIsInert(t *testing.T) {
	for _, v := range noCharterVerbs(t) {
		t.Run(v.name, func(t *testing.T) {
			var pins *atomic.Int32
			var probes *atomic.Int32
			if v.realPin {
				pins = countPins(t)
				probes = noRealContainment(t) // never the real probe: it starts a transient systemd unit
			} else {
				pins = noPins(t)
			}
			accounttest.Install(t, accounttest.SignedIn)
			home := freshHome(t)
			asOperatorProcess(t)
			db := newHILTestDB(t)
			beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
			code, stdout, stderr := runMain(t, append([]string{"--db-path", db, "--profile", "default", "--json", "doomsday"}, v.args...)...)
			v.want(t, code, stdout, stderr)
			if probes != nil && strings.Contains(stderr, "(test containment probe)") != (probes.Load() > 0) {
				t.Errorf("the refusal %q and the %d call(s) of the injected probe disagree: the answer did not come from the injected probe", stderr, probes.Load())
			}
			if after := homeFiles(t, home); after != beforeFiles {
				t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
			}
			requireSameCounts(t, v.name, beforeRows, tableCounts(t, db))
			// With no charter and no run no verb makes a pinned connection (inotify watch), reads a key or touches the secret
			// store: the read engine, the strategy and governor reads never did, and since the engine is lazy the acting verbs
			// (stop, finish, finish --begin, rubric, status) pin only when there is a run or a charter to act on. start and
			// render stop at the containment probe (a fake here) before the witness is built.
			if pins.Load() != 0 {
				t.Errorf("%d pinned connection(s) made with no charter and no run, want 0", pins.Load())
			}
		})
	}
}

// The classification is unchanged by the mode being wired: the operator-only verbs refuse every non-operator context
// (exit 3, before any database is opened by the verb), and every verb, gated or not, answers an agent's process nothing
// about a run and writes nothing. (status, render, the strategy reads and the governor read carry no operator gate today; this
// pins what they reveal and write, not that they are open.)
func TestDoomsdayVerbsKeepTheirGateAndWriteNothingForNonOperators(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, v := range noCharterVerbs(t) {
			t.Run(ctxName+"/"+v.name, func(t *testing.T) {
				noPins(t)
				accounttest.Install(t, accounttest.SignedIn)
				home := freshHome(t)
				db := newHILTestDB(t)
				setup(t)
				beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
				code, stdout, stderr := runMain(t, append([]string{"--db-path", db, "--profile", "default", "--json", "doomsday"}, v.args...)...)
				if v.gated {
					if code != 3 || !strings.Contains(stdout, `"code":"operator_only"`) {
						t.Errorf("exit %d, stdout %q, stderr %q: want the operator refusal (exit 3)", code, stdout, stderr)
					}
				} else {
					if code == 4 {
						t.Errorf("the sign-in refusal for a signed-in caller: %q", stderr)
					}
					if strings.Contains(stdout, `"run_id"`) || strings.Contains(stdout, `"active":true`) {
						t.Errorf("stdout names a run: %q", stdout)
					}
				}
				if after := homeFiles(t, home); after != beforeFiles {
					t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
				}
				requireSameCounts(t, ctxName+"/"+v.name, beforeRows, tableCounts(t, db))
			})
		}
	}
}

// Signed out, exactly stop passes the account gate; the rest wait for the sign-in (exit 4) and write nothing either way.
func TestDoomsdayVerbsSignedOutWithNoCharterWriteNothing(t *testing.T) {
	for _, v := range noCharterVerbs(t) {
		t.Run(v.name, func(t *testing.T) {
			noPins(t)
			accounttest.Install(t, accounttest.LockedNoLogin)
			home := freshHome(t)
			asOperatorProcess(t)
			db := newHILTestDB(t)
			beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
			code, stdout, stderr := runMain(t, append([]string{"--db-path", db, "--profile", "default", "--json", "doomsday"}, v.args...)...)
			refused := code == 4 && strings.Contains(stderr, loginRequiredLine)
			if v.name == "stop" {
				if refused {
					t.Errorf("stop signed out was refused for the account: %q", stderr)
				}
				wantRefusal(t, code, stdout, stderr)
			} else if !refused {
				t.Errorf("exit %d, stderr %q: want the sign-in refusal (exit 4)", code, stderr)
			}
			if after := homeFiles(t, home); after != beforeFiles {
				t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
			}
			requireSameCounts(t, v.name, beforeRows, tableCounts(t, db))
		})
	}
}

// STOP with no run: it says so in the build's words (stopNoRunText), exits non-zero (never "stopped"), writes nothing, and
// works signed out. The words are documented in SECURITY.md/AGENTS.md as "no open run to stop" for a build with the mode.
func TestStopWithNoRunSaysSoAndWritesNothingSignedOut(t *testing.T) {
	noPins(t)
	accounttest.Install(t, accounttest.LockedNoLogin)
	home := freshHome(t)
	asOperatorProcess(t)
	db := newHILTestDB(t)
	beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
	code, stdout, stderr := runMain(t, "--db-path", db, "--profile", "default", "doomsday", "stop")
	if code == 0 || code == 4 || strings.Contains(stdout, "operator_only") || strings.Contains(stderr, loginRequiredLine) {
		t.Fatalf("exit %d, stdout %q, stderr %q: want a refusal of its own", code, stdout, stderr)
	}
	if !strings.Contains(stderr, stopNoRunText) || strings.Contains(stdout, "stopped") {
		t.Fatalf("stdout %q, stderr %q: want %q and no claim of a stop", stdout, stderr, stopNoRunText)
	}
	if after := homeFiles(t, home); after != beforeFiles {
		t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
	}
	requireSameCounts(t, "stop", beforeRows, tableCounts(t, db))
}

// A wired build whose database is locked (another connection holds the write lock) says so in its own words, for every verb
// that needs the database: never "not available on this install" (the mode is there) and never a "no run" answer (it does not
// know). Observed bound (10 s): opening the engine waits two busy timeouts of 5 s. Stop adds that nothing was stopped, never
// says "stopped" and needs no sign-in. The case with a run open and the lock held is
// TestStopDuringALockedDatabaseIsBoundedAndNeverSaysStopped in internal/cli/doomsday.
func TestVerbsDuringALockedDatabaseSayTheDatabaseIsBusy(t *testing.T) {
	for _, v := range []struct {
		name string
		args []string
		want string
	}{
		{"stop", []string{"stop"}, stopBusyText + dbBusyText},
		{"finish", []string{"finish"}, dbBusyText},
		{"finish --begin", []string{"finish", "--begin"}, dbBusyText},
		{"rubric", []string{"rubric"}, dbBusyText},
	} {
		t.Run(v.name, func(t *testing.T) {
			noPins(t)
			if v.name == "stop" {
				accounttest.Install(t, accounttest.LockedNoLogin) // stop is the one verb that passes the account gate signed out
			} else {
				accounttest.Install(t, accounttest.SignedIn)
			}
			home := freshHome(t)
			asOperatorProcess(t)
			db := newHILTestDB(t)
			lock := holdWriteLock(t, db)
			defer lock()
			beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
			start := time.Now()
			code, stdout, stderr := runMain(t, append([]string{"--db-path", db, "--profile", "default", "doomsday"}, v.args...)...)
			if d := time.Since(start); d > 45*time.Second {
				t.Fatalf("%s took %v with the database write-locked", v.name, d)
			}
			if code != 1 || strings.Contains(stdout, "stopped") || strings.Contains(stderr, loginRequiredLine) {
				t.Fatalf("exit %d, stdout %q, stderr %q: want exit 1, no stop, no sign-in line", code, stdout, stderr)
			}
			if !strings.Contains(stderr, v.want) {
				t.Fatalf("stderr %q: want %q", stderr, v.want)
			}
			if strings.Contains(stderr, notAvailableOn) || strings.Contains(stderr, stopNoRunText) || strings.Contains(stdout, "no run") {
				t.Fatalf("stdout %q, stderr %q: a wrong statement (not available / no run) for a database that is only busy", stdout, stderr)
			}
			if after := homeFiles(t, home); after != beforeFiles {
				t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
			}
			requireSameCounts(t, v.name+" under a lock", beforeRows, tableCounts(t, db))
		})
	}
}

// `status` reads through the read-only handle (the GUI polls it), so another connection's write lock does not stop it: it answers
// from the database, creating, migrating and writing nothing. Mutant: status through the writing engine (red: busy, exit 1).
func TestStatusDuringALockedDatabaseReadsWithoutWriting(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	noPins(t)
	home := freshHome(t)
	asOperatorProcess(t)
	db := newHILTestDB(t)
	lock := holdWriteLock(t, db)
	defer lock()
	beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
	code, stdout, stderr := runMain(t, "--db-path", db, "--profile", "default", "doomsday", "status")
	if code != 0 || !strings.Contains(stdout, "no run") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if after := homeFiles(t, home); after != beforeFiles {
		t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
	}
	requireSameCounts(t, "status under a lock", beforeRows, tableCounts(t, db))
}

// holdWriteLock opens a second connection on the database and keeps a write transaction open until the returned release.
func holdWriteLock(t *testing.T, path string) (release func()) {
	t.Helper()
	other, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		conn.Close()
		other.Close()
	}
	t.Cleanup(release)
	return release
}
