package task

import (
	"errors"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// The person gate sees a turn recorded in a registered host's database whatever database the command
// names (#1332). The seam keeps the test off the real registry.
func TestPersonVerbsRefuseAProcessInsideARegisteredTurnWhateverDatabaseTheyName(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	host, _, _ := giantWithItem(t)
	d, err := storage.NewDatabase(host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coderhost.NewStore(d.DB).EnsureSession("default", "conv-lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE coder_sessions SET turn_pid = ?, turn_stamp = ? WHERE conversation_id = 'conv-lead'`, os.Getpid(), coderhost.ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	d.Close()
	unregister, err := coderhost.RegisterHostDB(host)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	named := map[string]string{
		"an empty file":      empty,
		"a nonexistent path": filepath.Join(t.TempDir(), "nowhere", "x.db"),
		"a second database":  newTaskTestDB(t),
	}
	for name, db := range named {
		t.Run(name, func(t *testing.T) {
			for verb, v := range personVerbArgs(1) {
				doc := failedTaskJSON(t, db, "default", 3, append([]string{"session", "person"}, v...)...)
				if doc["code"] != "operator_only" {
					t.Errorf("%s: %v", verb, doc)
				}
			}
			// the gate that opens the database without resolving a profile (task os uninstall)
			d := testDeps(t, db, "default", true)
			if _, err := d.personGateOpening(&cobra.Command{}, "remove the macOS menu", d.OpenDBUnresolved); err == nil {
				t.Error("personGateOpening let a process inside a registered turn through")
			}
		})
	}
}

// The operator passes: a registry entry for a database with no turn rows, and none at all.
func TestPersonGateLetsTheOperatorThroughWithARegistry(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for name, reg := range map[string]bool{"no registry": false, "an entry with no turn": true} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "coder-hosts")
			coderhost.SetRegistryDirForTest(t, dir)
			if reg {
				unregister, err := coderhost.RegisterHostDB(newTaskTestDB(t))
				if err != nil {
					t.Fatal(err)
				}
				defer unregister()
			}
			db := newTaskTestDB(t)
			d := testDeps(t, db, "default", true)
			if _, err := d.personGateOpening(&cobra.Command{}, "answer a person item", d.OpenDB); err != nil {
				t.Fatalf("the operator was refused: %v", err)
			}
		})
	}
}

// The named database is expanded ("~/") the way the hil gate does, so the registry check leaves that same file
// to the named check instead of reading it twice (here an unreadable one would otherwise refuse as a registered one).
func TestPersonGateExpandsTheNamedDatabaseBeforeComparingItWithTheRegistered(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	junk := filepath.Join(home, "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a database ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	unregister, err := coderhost.RegisterHostDB(junk)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	d := testDeps(t, "~/junk.db", "default", true)
	_, err = d.personGateOpening(&cobra.Command{}, "do a thing", func() (*storage.Database, error) { return nil, errors.New("named opened") })
	if err == nil || err.Error() != "named opened" {
		t.Fatalf("the registered file must be recognised as the named one: %v", err)
	}
}
