package captureindex_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/captureindex"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

const (
	work     = "711ef586-9f4b-4b1f-b2fd-cad23eec0a03"
	personal = "personal"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	t.Setenv(capture.InboxEnv, "")
	t.Setenv(capture.HomeEnv, "")
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "captureindex-test.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func writeCapture(t *testing.T, profileID, name, text string) string {
	t.Helper()
	inbox, err := capture.ProfileInbox(profileID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(inbox, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"title": name, "url": "https://example.com/" + name, "capturedAt": "2026-09-28T15:00:13Z", "source": "extension", "profile": profileID}
	blob, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, capture.MetaFile), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readable.md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "readable.md")
}

// recorder is a fake ingest: it records (profile, path) and fails paths in
// fail.
type recorder struct {
	mu    sync.Mutex
	calls [][2]string
	fail  map[string]error
}

func (r *recorder) ingest(_ context.Context, profileID, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]string{profileID, path})
	if err := r.fail[path]; err != nil {
		return err
	}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func docByPath(t *testing.T, db *sql.DB, profileID, path string) vault.DocumentEntry {
	t.Helper()
	docs, err := vault.ListDocuments(context.Background(), db, profileID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Path == path {
			return d
		}
	}
	t.Fatalf("no document row for %s in %s", path, profileID)
	return vault.DocumentEntry{}
}

func TestIndexProfileIndexesNewCapturesOnce(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "a", "alpha")
	b := writeCapture(t, work, "b", "beta")
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest}

	rep, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 2 || rep.Failed != 0 || rec.count() != 2 {
		t.Fatalf("want both captures indexed once, got %+v and %d ingests", rep, rec.count())
	}
	for _, p := range []string{a, b} {
		if d := docByPath(t, db, work, p); !d.Indexed || d.IndexError != "" {
			t.Fatalf("row for %s not marked indexed: %+v", p, d)
		}
	}
	for _, c := range rec.calls {
		if c[0] != work {
			t.Fatalf("ingested into profile %q, want %q", c[0], work)
		}
	}

	// A second pass has nothing to do: no double ingest.
	rep, err = ix.IndexProfile(context.Background(), db, work, captureindex.Options{RetryFailed: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 0 || rep.UpToDate != 2 || rec.count() != 2 {
		t.Fatalf("second pass re-ingested: %+v, %d ingests", rep, rec.count())
	}
}

func TestIndexProfileRecordsFailureAndRetriesOnlyWhenAsked(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "a", "alpha")
	rec := &recorder{fail: map[string]error{a: errors.New("knowledge_ingest: all chunk stores failed")}}
	ix := &captureindex.Indexer{Ingest: rec.ingest}

	rep, _ := ix.IndexProfile(context.Background(), db, work, captureindex.Options{})
	if rep.Failed != 1 {
		t.Fatalf("want one failure, got %+v", rep)
	}
	d := docByPath(t, db, work, a)
	if d.Indexed || d.IndexError == "" {
		t.Fatalf("failure not recorded on the row: %+v", d)
	}
	st, _ := captureindex.StatusOf(context.Background(), db, work)
	if st.Failed != 1 || st.Pending != 0 || st.LastError == "" {
		t.Fatalf("status: %+v", st)
	}

	// An ordinary pass leaves the failed row for a retry pass.
	rep, _ = ix.IndexProfile(context.Background(), db, work, captureindex.Options{})
	if rec.count() != 1 || rep.Waiting != 1 {
		t.Fatalf("ordinary pass retried a failed row: %+v, %d ingests", rep, rec.count())
	}

	rec.fail = nil
	rep, _ = ix.IndexProfile(context.Background(), db, work, captureindex.Options{RetryFailed: true})
	if rep.Indexed != 1 {
		t.Fatalf("retry pass did not index: %+v", rep)
	}
	if d := docByPath(t, db, work, a); !d.Indexed || d.IndexError != "" {
		t.Fatalf("retry success not recorded (error must clear): %+v", d)
	}
}

func TestIndexProfileReindexesChangedCapture(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "a", "alpha")
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest}
	if _, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.WriteFile(a, []byte("alpha, rewritten and longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(a, later, later)
	rep, _ := ix.IndexProfile(context.Background(), db, work, captureindex.Options{})
	if rep.Indexed != 1 || rec.count() != 2 {
		t.Fatalf("a changed capture must be re-indexed: %+v, %d ingests", rep, rec.count())
	}
}

func TestIndexProfileIsProfileIsolated(t *testing.T) {
	db := newTestDB(t)
	writeCapture(t, work, "w", "work page")
	p := writeCapture(t, personal, "p", "personal page")
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest}

	if _, err := ix.IndexProfile(context.Background(), db, personal, captureindex.Options{}); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 || rec.calls[0] != [2]string{personal, p} {
		t.Fatalf("a pass over %q touched another profile: %v", personal, rec.calls)
	}
	st, _ := captureindex.StatusOf(context.Background(), db, work)
	if st.Captures != 1 || st.Pending != 1 || st.Indexed != 0 {
		t.Fatalf("work's capture must still be pending: %+v", st)
	}
}

func TestIndexProfileLeavesUploadsAlone(t *testing.T) {
	db := newTestDB(t)
	src := filepath.Join(t.TempDir(), "resume.md")
	if err := os.WriteFile(src, []byte("resume"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(context.Background(), work)
	if _, err := vault.RegisterDocument(ctx, db, src, "upload"); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest}
	rep, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{RetryFailed: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 0 || rep.Indexed != 0 {
		t.Fatalf("an uploaded document went through the capture store: %v", rec.calls)
	}
}

func TestIndexProfileForceOneID(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "a", "alpha")
	writeCapture(t, work, "b", "beta")
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest}
	if _, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{}); err != nil {
		t.Fatal(err)
	}
	id := docByPath(t, db, work, a).ID
	rep, _ := ix.IndexProfile(context.Background(), db, work, captureindex.Options{IDs: []string{id}, Force: true})
	if rep.Indexed != 1 || rec.count() != 3 || rec.calls[2][1] != a {
		t.Fatalf("forced re-index of %s: %+v, calls %v", id, rep, rec.calls)
	}
}

func TestIndexProfileRejectsBadID(t *testing.T) {
	db := newTestDB(t)
	if _, err := (&captureindex.Indexer{}).IndexProfile(context.Background(), db, "../etc", captureindex.Options{}); err == nil {
		t.Fatal("want an error for a traversal id")
	}
}

// TestConcurrentPassesDoNotDoubleIngest: the bridge and a backfill running
// at once must ingest each capture once.
func TestConcurrentPassesDoNotDoubleIngest(t *testing.T) {
	db := newTestDB(t)
	for _, n := range []string{"a", "b", "c"} {
		writeCapture(t, work, n, n)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	slow := func(_ context.Context, _ string, path string) error {
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		seen[path]++
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ix := &captureindex.Indexer{Ingest: slow}
			if _, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(seen) != 3 {
		t.Fatalf("want 3 captures ingested, got %v", seen)
	}
	for p, n := range seen {
		if n != 1 {
			t.Fatalf("%s ingested %d times", p, n)
		}
	}
}

func TestQueueIndexesAndRetries(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "a", "alpha")
	rec := &recorder{fail: map[string]error{a: errors.New("busy")}}
	q := &captureindex.Queue{
		Indexer:     &captureindex.Indexer{Ingest: rec.ingest},
		Open:        func() (*sql.DB, func(), error) { return db, func() {}, nil },
		RetryDelays: []time.Duration{30 * time.Millisecond},
	}
	q.Start()
	defer q.Stop()

	q.Enqueue(work)
	q.Enqueue("")     // the shared inbox: ignored
	q.Enqueue("../x") // hostile: ignored
	waitFor(t, func() bool { return rec.count() >= 1 })
	rec.mu.Lock()
	rec.fail = nil
	rec.mu.Unlock()
	waitFor(t, func() bool { return docByPath(t, db, work, a).Indexed })
	if n := rec.count(); n != 2 {
		t.Fatalf("want one failure and one retry, got %d ingests", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeArtifact(t *testing.T, readable, name, text string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(readable), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCompanionsAreIngestedOnceEach: a video's transcript is where what was
// said lives, so it is ingested beside readable.md; a summary written later
// is picked up by the next pass, and nothing is ingested twice.
func TestCompanionsAreIngestedOnceEach(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "video", "description")
	tr := writeArtifact(t, a, "transcript.md", "what was said")
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest, Companions: func(context.Context) bool { return true }}

	if _, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{}); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 2 || rec.calls[0][1] != a || rec.calls[1][1] != tr {
		t.Fatalf("want readable.md then transcript.md, got %v", rec.calls)
	}

	sum := writeArtifact(t, a, "summary.md", "the gist")
	rep, _ := ix.IndexProfile(context.Background(), db, work, captureindex.Options{})
	if rep.Indexed != 1 || rec.count() != 3 || rec.calls[2][1] != sum {
		t.Fatalf("the late summary must be ingested alone: %+v, %v", rep, rec.calls)
	}

	rep, _ = ix.IndexProfile(context.Background(), db, work, captureindex.Options{RetryFailed: true})
	if rec.count() != 3 || rep.UpToDate != 1 {
		t.Fatalf("a third pass re-ingested: %+v, %v", rep, rec.calls)
	}
}

func TestCompanionsWaitForAMonomindThatTakesThem(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "video", "description")
	writeArtifact(t, a, "transcript.md", "what was said")
	rec := &recorder{}
	ix := &captureindex.Indexer{Ingest: rec.ingest, Companions: func(context.Context) bool { return false }}
	if _, err := ix.IndexProfile(context.Background(), db, work, captureindex.Options{}); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 || rec.calls[0][1] != a {
		t.Fatalf("an older monomind must get readable.md only, got %v", rec.calls)
	}
}

func TestCompanionFailureIsRecordedAndRetried(t *testing.T) {
	db := newTestDB(t)
	a := writeCapture(t, work, "video", "description")
	tr := writeArtifact(t, a, "transcript.md", "what was said")
	rec := &recorder{fail: map[string]error{tr: errors.New("boom")}}
	ix := &captureindex.Indexer{Ingest: rec.ingest, Companions: func(context.Context) bool { return true }}

	rep, _ := ix.IndexProfile(context.Background(), db, work, captureindex.Options{})
	if rep.Failed != 1 {
		t.Fatalf("want the transcript's failure reported: %+v", rep)
	}
	if d := docByPath(t, db, work, a); d.IndexError == "" {
		t.Fatalf("the transcript's failure must be on the row: %+v", d)
	}
	rec.fail = nil
	rep, _ = ix.IndexProfile(context.Background(), db, work, captureindex.Options{RetryFailed: true})
	if rep.Indexed != 1 || rec.calls[len(rec.calls)-1][1] != tr {
		t.Fatalf("retry must re-ingest the transcript: %+v %v", rep, rec.calls)
	}
	if d := docByPath(t, db, work, a); !d.Indexed || d.IndexError != "" {
		t.Fatalf("row after retry: %+v", d)
	}
}
