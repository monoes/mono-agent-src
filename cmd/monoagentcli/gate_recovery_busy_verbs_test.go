package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/extension"
)

// realTree builds the production registration of the gate (the real command tree, not a test's own binding)
// for the rest of the test, in a HOME of its own.
func realTree(t *testing.T) string {
	t.Helper()
	asOperatorProcess(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(extension.ExtensionPortEnv, "1") // the extension verbs probe for a bridge: never the real one
	opgate.SetForTest(t, nil)                 // restores the default after the test, whatever the build registers
	prev := inTestBinary
	inTestBinary = func() bool { return false } // what the production binary answers
	t.Cleanup(func() { inTestBinary = prev })
	return home
}

// The recovery verbs of master use the busy verdict: with a fake live host whose database is locked, each exits
// non-zero with the retry text (a plain error, not operator_only), through the real command tree.
func TestEachRecoveryVerbInTheRealTreeIsToldToRetryWhileTheLiveHostIsBusy(t *testing.T) {
	dir := busySetup(t)
	realTree(t)
	db := newHILTestDB(t)
	registerLive(t, db)
	lockDB(t, db)
	for _, args := range [][]string{
		{"secret", "rebind"},
		{"extension", "reset"},
		{"extension", "browsers", "revoke", "abcd1234"},
	} {
		t.Run(args[0]+" "+args[1], func(t *testing.T) {
			_, err := runRoot(t, append([]string{"--db-path", db}, args...)...)
			wantRetryText(t, err, db, entryFileOf(t, dir, db))
		})
	}
}

// The real tree registers the recovery check (not only the string gate): a root built with no flags, whose
// default database is a locked live host's, answers busy.
func TestTheRealCommandTreeRegistersTheRecoveryCheck(t *testing.T) {
	dir := busySetup(t)
	home := realTree(t)
	db := filepath.Join(home, ".monoagent", "monoagent.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMigratedDB(t, db)
	registerLive(t, db)
	lockDB(t, db)
	newRootCmd()
	why, busy := opgate.RecoveryCheck()
	if busy == nil {
		t.Fatalf("the real tree did not register the recovery check (answer %q)", why)
	}
	canon, _ := filepath.EvalSymlinks(db)
	wantRetryText(t, busy, canon, entryFileOf(t, dir, db))
}

// The busy refusal has its own --json code (host_busy, not operator_only) and a non-zero exit that is not
// operator_only's, so a caller can retry instead of giving up.
func TestTheBusyRefusalHasItsOwnJSONCodeAndExit(t *testing.T) {
	busySetup(t)
	realTree(t)
	db := newHILTestDB(t)
	registerLive(t, db)
	lockDB(t, db)
	_, err := runRoot(t, "--db-path", db, "extension", "reset")
	if err == nil {
		t.Fatal("no error")
	}
	if code := jsonCode(err); code != "host_busy" {
		t.Fatalf("json code %q, want host_busy", code)
	}
	if exit := clicore.ExitCodeFor(err); exit == 0 || exit == clicore.ExitCodeFor(clicore.OperatorOnly("x")) {
		t.Fatalf("exit %d must be non-zero and not operator_only's", exit)
	}
	var stdout, stderr bytes.Buffer
	reportGateRefusal([]string{"--json", "extension", "reset"}, err, &stdout, &stderr)
	var doc map[string]any
	if jerr := json.Unmarshal(stdout.Bytes(), &doc); jerr != nil || doc["code"] != "host_busy" {
		t.Fatalf("json document %q (%v)", stdout.String(), jerr)
	}
	if entries, _ := doc["entries"].([]any); len(entries) != 1 {
		t.Fatalf("entries: %v", doc["entries"])
	}
}
