package recording

import (
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

func TestIngestWritesToStoreNotInbox(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(capture.InboxEnv, filepath.Join(home, ".monomind", "inbox"))
	in := &Ingest{}
	for _, profile := range []string{"", "work"} {
		id := "rec-" + firstNonEmpty(profile, "none")
		mustHandle(t, in, Frame{Op: OpStart, RecordingID: id, URL: "https://example.com/", Profile: profile})
		mustHandle(t, in, Frame{Op: OpEvent, RecordingID: id, Profile: profile, Event: ev("e1", 1, EvClick)})
		out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: id, Profile: profile})
		res, err := in.Finalize(out.Stopped)
		if err != nil {
			t.Fatal(err)
		}
		store, _ := StoreDir(profile)
		if filepath.Dir(res.Path) != store {
			t.Fatalf("profile %q landed in %s, want %s", profile, res.Path, store)
		}
		if fi, _ := os.Stat(store); fi.Mode().Perm() != 0o700 {
			t.Fatalf("store mode %o", fi.Mode().Perm())
		}
	}
	for _, inbox := range legacyInboxes() {
		if entries, _ := capture.List(inbox); len(entries) != 0 {
			t.Fatalf("recording in capture inbox %s", inbox)
		}
	}
}

func TestMigrateMovesLegacyRecordings(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(capture.InboxEnv, filepath.Join(home, ".monomind", "inbox"))
	var logs []string
	prev := Logf
	Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { Logf = prev })

	// What older builds left behind: recordings in the capture inboxes, a
	// spool, and an ordinary capture that must stay put.
	legacyDefault := capture.DefaultInbox()
	legacyWork, _ := capture.ProfileInbox("work")
	old := writeRecording(t, legacyDefault, "rec-old", time.Now().Add(-time.Hour), 2)
	oldWork := writeRecording(t, legacyWork, "rec-work", time.Now(), 1)
	page, err := (&capture.Writer{Inbox: legacyDefault}).Write(&capture.Envelope{Meta: capture.Meta{URL: "https://example.com/p"},
		Artifacts: map[string]capture.Artifact{"readable.md": capture.Inline([]byte("x"))}})
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(legacyDefault, spoolPrefix+"rec-live")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}

	list, err := List() // migrates on first use
	if err != nil || len(list) != 2 {
		t.Fatalf("List after migration = %+v, %v", list, err)
	}
	root, _ := StoreDir("")
	work, _ := StoreDir("work")
	for _, want := range []string{filepath.Join(root, filepath.Base(old)), filepath.Join(work, filepath.Base(oldWork)), filepath.Join(root, filepath.Base(spool))} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("not migrated to %s: %v", want, err)
		}
	}
	for _, gone := range []string{old, oldWork, spool} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still in the capture inbox", gone)
		}
	}
	if _, err := os.Stat(page.Path); err != nil {
		t.Errorf("ordinary capture moved: %v", err)
	}
	if list[0].Profile != "work" || list[1].Profile != "" {
		t.Errorf("profiles = %q, %q", list[0].Profile, list[1].Profile)
	}
	if n := strings.Count(strings.Join(logs, "\n"), "moved"); n != 3 {
		t.Errorf("want one log line per move, got %v", logs)
	}
	// Marker written: a second run moves nothing, even a newly appeared one.
	writeRecording(t, legacyDefault, "rec-late", time.Now(), 1)
	if n, err := MigrateLegacy(); n != 0 || err != nil {
		t.Fatalf("second migration = %d, %v", n, err)
	}
}
