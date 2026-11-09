package capture

import (
	"database/sql"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// profileHome points HOME (and USERPROFILE, for the Windows lookup) at a
// disposable directory, so profiledir resolves profile roots inside it.
func profileHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(InboxEnv, filepath.Join(home, "default-inbox"))
	return home
}

func profileEnvelope(profile string) *Envelope {
	return &Envelope{
		Meta: Meta{
			URL:        "https://example.com/post",
			Title:      "A Post",
			CapturedAt: "2026-09-21T10:11:12Z",
			Profile:    profile,
		},
		Artifacts: map[string]Artifact{ArtifactReadable: Inline([]byte("# A Post"))},
	}
}

func TestProfileInbox_DefaultRoot(t *testing.T) {
	home := profileHome(t)

	got, err := ProfileInbox("work")
	if err != nil {
		t.Fatalf("ProfileInbox: %v", err)
	}
	want := filepath.Join(home, ".monoagent", "profiles", "work", ".monomind", "inbox")
	if got != want {
		t.Errorf("ProfileInbox = %q, want %q", got, want)
	}
}

// TestProfileInbox_RejectsTraversal is the security guard: the profile id
// comes from the browser, and it becomes a filesystem path. Every hostile
// spelling must be refused outright — never joined, never written to, and
// never resolved to anything outside the profiles root.
func TestProfileInbox_RejectsTraversal(t *testing.T) {
	home := profileHome(t)
	profilesRoot := filepath.Join(home, ".monoagent", "profiles")

	hostile := []string{
		"", "   ", "..", "../evil", "../../etc", "evil/..", `..\evil`,
		"a/b", `a\b`, "a..b", "/etc/passwd", "work/../../../..",
	}
	for _, id := range hostile {
		dir, err := ProfileInbox(id)
		if err == nil {
			t.Errorf("ProfileInbox(%q) = %q, want an error", id, dir)
			continue
		}
		if dir != "" {
			t.Errorf("ProfileInbox(%q) returned path %q alongside its error", id, dir)
		}
	}

	// And the same ids must not be able to write anywhere through Write:
	// the capture lands in the default inbox, unprofiled, with a warning.
	for _, id := range hostile {
		if strings.TrimSpace(id) == "" {
			continue // an absent profile is not a rejected one
		}
		w := &Writer{Now: func() time.Time { return time.Unix(0, 0).UTC() }}
		res, err := w.Write(profileEnvelope(id))
		if err != nil {
			t.Fatalf("Write(profile %q): %v", id, err)
		}
		if !strings.HasPrefix(res.Path, DefaultInbox()+string(os.PathSeparator)) {
			t.Errorf("profile %q wrote to %q, outside the default inbox %q", id, res.Path, DefaultInbox())
		}
		if res.Meta.Profile != "" {
			t.Errorf("profile %q survived into meta as %q", id, res.Meta.Profile)
		}
		if len(res.Warnings) == 0 {
			t.Errorf("profile %q was dropped without a warning", id)
		}
		if _, err := os.Stat(profilesRoot); err == nil {
			t.Errorf("profile %q created something under the profiles root %q", id, profilesRoot)
		}
	}
}

func TestWrite_ProfileLandsInProfileInbox(t *testing.T) {
	profileHome(t)
	w := &Writer{Now: func() time.Time { return time.Unix(0, 0).UTC() }}

	res, err := w.Write(profileEnvelope("work"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want, _ := ProfileInbox("work")
	if filepath.Dir(res.Path) != want {
		t.Errorf("capture landed in %q, want it under %q", res.Path, want)
	}
	if res.Meta.Profile != "work" {
		t.Errorf("meta.profile = %q, want %q", res.Meta.Profile, "work")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}

	// meta.json on disk carries it too — that is what the ingest side reads.
	blob, err := os.ReadFile(filepath.Join(res.Path, MetaFile))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("decode meta.json: %v", err)
	}
	if raw["profile"] != "work" {
		t.Errorf("meta.json profile = %v, want %q", raw["profile"], "work")
	}
}

// A separate profile means a separate directory: this is the whole point of
// the feature, so it is asserted rather than assumed.
func TestWrite_ProfilesDoNotShareAnInbox(t *testing.T) {
	profileHome(t)
	w := &Writer{Now: func() time.Time { return time.Unix(0, 0).UTC() }}

	work, err := w.Write(profileEnvelope("work"))
	if err != nil {
		t.Fatalf("Write(work): %v", err)
	}
	personal, err := w.Write(profileEnvelope("personal"))
	if err != nil {
		t.Fatalf("Write(personal): %v", err)
	}
	if filepath.Dir(work.Path) == filepath.Dir(personal.Path) {
		t.Fatalf("both profiles wrote into %q", filepath.Dir(work.Path))
	}

	workEntries, err := List(filepath.Dir(work.Path))
	if err != nil {
		t.Fatalf("List(work): %v", err)
	}
	if len(workEntries) != 1 || workEntries[0].Meta.Profile != "work" {
		t.Fatalf("work inbox = %+v", workEntries)
	}
}

// No profile is today's behaviour, exactly: the default inbox, and a
// meta.json with no `profile` key at all.
func TestWrite_NoProfileIsUnchanged(t *testing.T) {
	profileHome(t)
	w := &Writer{Now: func() time.Time { return time.Unix(0, 0).UTC() }}

	res, err := w.Write(profileEnvelope(""))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(res.Path) != DefaultInbox() {
		t.Errorf("capture landed in %q, want %q", filepath.Dir(res.Path), DefaultInbox())
	}
	blob, err := os.ReadFile(filepath.Join(res.Path, MetaFile))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(blob), "profile") {
		t.Errorf("meta.json of an unprofiled capture mentions a profile:\n%s", blob)
	}
}

// An explicit --out is an instruction about this one capture and outranks
// the profile's own inbox — but the profile is still recorded.
func TestWrite_ExplicitInboxWinsOverProfile(t *testing.T) {
	profileHome(t)
	out := filepath.Join(t.TempDir(), "elsewhere")
	w := &Writer{Inbox: out, Now: func() time.Time { return time.Unix(0, 0).UTC() }}

	res, err := w.Write(profileEnvelope("work"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(res.Path) != out {
		t.Errorf("capture landed in %q, want %q", filepath.Dir(res.Path), out)
	}
	if res.Meta.Profile != "work" {
		t.Errorf("meta.profile = %q, want %q", res.Meta.Profile, "work")
	}
}

// A profile pointed at a folder of its own (profiles.root_dir) does NOT
// move its capture inbox. That is deliberate — see ProfileInbox's comment:
// the process that writes a capture has no database handle, so a rule that
// needed one would be obeyed by `capture list` and not by the daemon.
// Pinned as a test because the alternative is plausible enough that someone
// will otherwise "fix" it in one place only.
func TestProfileInbox_DoesNotFollowRootDirOverride(t *testing.T) {
	home := profileHome(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, root_dir TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	custom := filepath.Join(home, "external-drive", "work")
	if _, err := db.Exec(`INSERT INTO profiles (id, root_dir) VALUES ('work', ?)`, custom); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// profiledir WOULD point elsewhere with the database in hand...
	if got := profiledir.Root(db, "work"); got != custom {
		t.Fatalf("profiledir.Root = %q, want the override %q", got, custom)
	}
	// ...and the capture inbox deliberately does not use it.
	got, err := ProfileInbox("work")
	if err != nil {
		t.Fatalf("ProfileInbox: %v", err)
	}
	want := filepath.Join(home, ".monoagent", "profiles", "work", ".monomind", "inbox")
	if got != want {
		t.Errorf("ProfileInbox = %q, want %q", got, want)
	}

	w := &Writer{Now: func() time.Time { return time.Unix(0, 0).UTC() }}
	res, err := w.Write(profileEnvelope("work"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(res.Path) != want {
		t.Errorf("capture landed in %q, want %q", filepath.Dir(res.Path), want)
	}
}

func TestInboxFor(t *testing.T) {
	profileHome(t)
	got, err := InboxFor("")
	if err != nil {
		t.Fatalf("InboxFor(\"\"): %v", err)
	}
	if got != DefaultInbox() {
		t.Errorf("InboxFor(\"\") = %q, want %q", got, DefaultInbox())
	}
	if _, err := InboxFor("../evil"); err == nil {
		t.Error("InboxFor(\"../evil\") should fail")
	}
}
