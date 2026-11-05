package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
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
// The text that legitimately differs between the two builds is in doomsday_default_stub_test.go / doomsday_built_test.go.

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

// tableCounts is the row count of every table of the database, read on a connection of its own.
func tableCounts(t *testing.T, path string) map[string]int {
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
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

func requireSameCounts(t *testing.T, label string, before, after map[string]int) {
	t.Helper()
	var diff []string
	for n, b := range before {
		if after[n] != b {
			diff = append(diff, n)
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("%s changed rows of %v", label, diff)
	}
}

type noCharterVerb struct {
	name  string
	args  []string
	gated bool // an operator-only verb: an agent's process is refused with exit 3
	// readOnlyEngine: the verb reads through the read engine or a reader and must make no pinned connection.
	readOnlyEngine bool
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
	file := writeDoomsdayCharter(t)
	vs := []noCharterVerb{
		{"start", []string{"start", "--charter", file, "--yes-i-lift", "--hash", strings.Repeat("a", 64)}, true, false, wantRefusal},
		{"stop", []string{"stop"}, true, false, wantRefusal},
		{"finish", []string{"finish"}, true, false, wantRefusal},
		{"finish --begin", []string{"finish", "--begin"}, true, false, wantRefusedOrNotAccepted},
		{"rubric", []string{"rubric", "c1", "approve", "--dossier", "abcdef12"}, true, false, wantRefusal},
		{"render", []string{"render", "--charter", file}, false, false, wantRefusal},
		{"overview", []string{"overview"}, true, true, wantUnavailableDoc},
		{"feed", []string{"feed"}, true, true, wantUnavailableDoc},
		{"status", []string{"status"}, false, false, wantText(`"active":false`)},
		{"strategy status", []string{"strategy", "status"}, false, true, wantText(`"off":true`)},
		{"strategy epochs", []string{"strategy", "epochs"}, false, true, wantText(`"off":true`)},
		{"strategy directions", []string{"strategy", "directions"}, false, true, wantText(`"off":true`)},
	}
	if governorVerbExpected {
		vs = append(vs, noCharterVerb{"governor status", []string{"governor", "status"}, false, true, wantText(`"off":true`)})
	}
	return vs
}

// The user's own process, signed in, no charter: each verb answers as above, exit code included, and writes nothing.
func TestEveryDoomsdayVerbWithNoCharterIsInert(t *testing.T) {
	for _, v := range noCharterVerbs(t) {
		t.Run(v.name, func(t *testing.T) {
			pins := noPins(t)
			accounttest.Install(t, accounttest.SignedIn)
			home := freshHome(t)
			asOperatorProcess(t)
			db := newHILTestDB(t)
			beforeFiles, beforeRows := homeFiles(t, home), tableCounts(t, db)
			code, stdout, stderr := runMain(t, append([]string{"--db-path", db, "--profile", "default", "--json", "doomsday"}, v.args...)...)
			v.want(t, code, stdout, stderr)
			if after := homeFiles(t, home); after != beforeFiles {
				t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
			}
			requireSameCounts(t, v.name, beforeRows, tableCounts(t, db))
			// The control room's read engine and the strategy and governor reads never pin. The verbs on the full engine (start,
			// render, stop, finish, rubric, status) build its sequencer when the command constructs it, in a build with the mode
			// wired, for the life of that one command (see the report of PR2): not asserted here, only that it is refused.
			if v.readOnlyEngine && pins.Load() != 0 {
				t.Errorf("%d pinned connection(s) made by a read verb", pins.Load())
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

// STOP while another connection holds the database's write lock: with no run there is nothing to stop and the answer comes
// from reads only (WAL readers are not blocked), so it is the same refusal; either way it returns within a bound (the
// driver's busy timeout is 5 s) and never says "stopped". The case with a run open and the lock held is
// TestStopDuringALockedDatabaseIsBoundedAndNeverSaysStopped in internal/cli/doomsday.
func TestStopDuringALockedDatabaseWithNoRunIsBoundedAndRefuses(t *testing.T) {
	noPins(t)
	accounttest.Install(t, accounttest.LockedNoLogin)
	home := freshHome(t)
	asOperatorProcess(t)
	db := newHILTestDB(t)
	lock := holdWriteLock(t, db)
	defer lock()
	beforeFiles := homeFiles(t, home)
	start := time.Now()
	code, stdout, stderr := runMain(t, "--db-path", db, "--profile", "default", "doomsday", "stop")
	if d := time.Since(start); d > 45*time.Second {
		t.Fatalf("stop took %v with the database write-locked", d)
	}
	if code == 0 || code == 4 || strings.Contains(stdout, "stopped") || strings.Contains(stderr, loginRequiredLine) {
		t.Fatalf("exit %d, stdout %q, stderr %q: want a refusal that is not a stop and not the sign-in", code, stdout, stderr)
	}
	if after := homeFiles(t, home); after != beforeFiles {
		t.Errorf("files under HOME: before [%s], after [%s]", beforeFiles, after)
	}
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
