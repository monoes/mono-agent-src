package extensioncli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
)

// vaultEnv is a migrated database with two live profiles whose folders are
// under a scratch HOME, so no save can reach the real one.
func vaultEnv(t *testing.T) (path, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path = testdb.Path(t)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"p-work", "p-home"} {
		if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return path, home
}

func vaultFile(profile, clientID string) extension.VaultFile {
	data := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 100)...)
	return extension.VaultFile{ProfileID: profile, ClientID: clientID, Name: "invoice.pdf", MIME: "application/pdf", Ext: ".pdf",
		Data: data, SourceURL: "https://example.test/invoice.pdf"}
}

type vaultRow struct {
	id, path, filename, source, label, clientID, sourceURL string
	size                                                   int
}

func vaultRows(t *testing.T, path string) []vaultRow {
	t.Helper()
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.DB.Query(`SELECT id, path, filename, source, COALESCE(label,''), COALESCE(client_id,''), COALESCE(source_url,''), size_bytes
		FROM vault_images ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []vaultRow
	for rows.Next() {
		var r vaultRow
		if err := rows.Scan(&r.id, &r.path, &r.filename, &r.source, &r.label, &r.clientID, &r.sourceURL, &r.size); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// filesIn lists the regular files under dir (none when it does not exist).
func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// One save is one file and one row in the profile's media/file vault, the
// vault vault_item_list and vault_item_get_path read; a retry of the same
// client id writes nothing and returns the same item.
func TestVaultSinkSavesOnceIntoTheProfilesVault(t *testing.T) {
	path, home := vaultEnv(t)
	f := vaultFile("p-work", "v-1")
	got, err := extensionVaultSink(path).SaveToVault(context.Background(), f)
	if err != nil || !got.Created || got.ID == "" || got.Name != "invoice.pdf" || got.MIME != "application/pdf" || got.SizeBytes != len(f.Data) {
		t.Fatalf("save: %+v, %v", got, err)
	}
	rows := vaultRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	vaultDir := filepath.Join(home, ".monoagent", "profiles", "p-work", ".monoagent", "vault")
	if r.id != got.ID || r.path != filepath.Join(vaultDir, got.ID+".pdf") || r.filename != got.ID+".pdf" || r.source != "extension" ||
		r.label != "invoice.pdf" || r.clientID != "v-1" || r.sourceURL != "https://example.test/invoice.pdf" || r.size != len(f.Data) {
		t.Errorf("row %+v", r)
	}
	if b, err := os.ReadFile(r.path); err != nil || !bytes.Equal(b, f.Data) {
		t.Errorf("stored file: %v", err)
	}
	if fi, err := os.Stat(r.path); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("stored file mode %v, want 0600", fi.Mode().Perm())
	}

	// vault_item_get_path resolves the id the reply named.
	db, _ := storage.NewDatabase(path)
	resolved, err := vault.Resolve(vault.ContextWithProfileID(context.Background(), "p-work"), db.DB, "@"+got.ID)
	db.Close()
	if err != nil || resolved != r.path {
		t.Errorf("vault.Resolve = %q, %v", resolved, err)
	}

	again, err := extensionVaultSink(path).SaveToVault(context.Background(), f)
	if err != nil || again.Created || again.ID != got.ID {
		t.Errorf("retry: %+v, %v", again, err)
	}
	if n := len(vaultRows(t, path)); n != 1 {
		t.Errorf("rows after the retry = %d, want 1", n)
	}
	if files := filesIn(t, vaultDir); len(files) != 1 {
		t.Errorf("files after the retry = %v, want one", files)
	}

	// The client id is per profile: another profile's save is its own item.
	other, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-home", "v-1"))
	if err != nil || !other.Created || other.ID == got.ID {
		t.Errorf("another profile, same client id: %+v, %v", other, err)
	}
}

// Many sends of one client id at once still make one item.
func TestVaultSinkIsIdempotentUnderConcurrency(t *testing.T) {
	path, home := vaultEnv(t)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-same"))
			if err != nil {
				t.Errorf("save %d: %v", i, err)
			}
			ids[i] = got.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Errorf("ids = %v, want one id", ids)
			break
		}
	}
	if n := len(vaultRows(t, path)); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	if files := filesIn(t, filepath.Join(home, ".monoagent", "profiles", "p-work")); len(files) != 1 {
		t.Errorf("files = %v, want one", files)
	}
}

// A profile that is not in the database (deleted since the panel listed it, or
// never there) is refused, and gets no folder and no row.
func TestVaultSinkRefusesADeadProfile(t *testing.T) {
	path, home := vaultEnv(t)
	_, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-gone", "v-1"))
	if err == nil || sinkCode(t, err) != extension.CodeInvalidInput {
		t.Fatalf("dead profile: %v, want invalid_input", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "profiles", "p-gone")); !os.IsNotExist(err) {
		t.Errorf("the dead profile's folder was created: %v", err)
	}
	if n := len(vaultRows(t, path)); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

// No database, or one from before vault.save: unavailable, nothing written,
// and the bridge never migrates or creates the database.
func TestVaultSinkWithoutAReadyDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "none.db")
	if _, err := extensionVaultSink(missing).SaveToVault(context.Background(), vaultFile("p-work", "v-1")); err == nil || sinkCode(t, err) != extension.CodeUnavailable {
		t.Errorf("no database: %v, want unavailable", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("the database was created: %v", err)
	}

	path, _ := vaultEnv(t)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP INDEX idx_vault_images_client`, `ALTER TABLE vault_images DROP COLUMN client_id`} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-1")); err == nil || sinkCode(t, err) != extension.CodeUnavailable {
		t.Errorf("an old database: %v, want unavailable", err)
	}
}

// Every bridge this binary builds answers vault.save, and none has a method
// that reads the vault.
func TestEveryBridgeAnswersVaultSaveAndNothingReadsTheVault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newExtensionServer(zerolog.Nop(), "test")
	found := false
	for _, m := range srv.RequestMethods() {
		switch {
		case m == extension.MethodVaultSave:
			found = true
		case len(m) > 6 && m[:6] == "vault.":
			t.Errorf("the bridge has another vault method: %s", m)
		}
	}
	if !found {
		t.Fatalf("%s is not advertised by the bridge newExtensionServer builds", extension.MethodVaultSave)
	}
}

// A file or symlink already at the next img-NNN<ext> (a crash between the
// write and the commit left it, with no row) is left alone, and the save takes
// the next free seq, up to 5 times; past that a final storage_conflict says to
// check the vault folder, so the extension does not retry for ever.
func TestVaultSinkSkipsStrayFilesThenReportsAConflict(t *testing.T) {
	path, home := vaultEnv(t)
	vaultDir := filepath.Join(home, ".monoagent", "profiles", "p-work", ".monoagent", "vault")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(vaultDir, "img-001.pdf")
	if err := os.WriteFile(orphan, []byte("someone's data"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "elsewhere.pdf")
	if err := os.WriteFile(target, []byte("not the vault's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(vaultDir, "img-002.pdf")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	got, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-1"))
	if err != nil || !got.Created || got.ID != "img-003" {
		t.Fatalf("save past an orphan and a symlink: %+v, %v", got, err)
	}
	if b, _ := os.ReadFile(orphan); string(b) != "someone's data" {
		t.Errorf("the orphan was overwritten: %q", b)
	}
	if b, _ := os.ReadFile(target); string(b) != "not the vault's" {
		t.Errorf("the symlink's target was written: %q", b)
	}
	if rows := vaultRows(t, path); len(rows) != 1 || rows[0].path != filepath.Join(vaultDir, "img-003.pdf") {
		t.Errorf("rows = %+v", rows)
	}
	// Five strays in a row are skipped...
	for seq := 4; seq <= 8; seq++ {
		_ = os.WriteFile(filepath.Join(vaultDir, fmt.Sprintf("img-%03d.pdf", seq)), []byte("x"), 0o600)
	}
	if got, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-2")); err != nil || got.ID != "img-009" {
		t.Fatalf("past five strays: %+v, %v", got, err)
	}
	// ...six are a conflict, final, and the message says where to look.
	for seq := 10; seq <= 15; seq++ {
		_ = os.WriteFile(filepath.Join(vaultDir, fmt.Sprintf("img-%03d.pdf", seq)), []byte("x"), 0o600)
	}
	_, err = extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-3"))
	if err == nil || sinkCode(t, err) != extension.CodeStorageConflict || !strings.Contains(err.Error(), "vault folder") {
		t.Errorf("past the skip limit: %v, want storage_conflict naming the vault folder", err)
	}
}

// Saves from the browser are capped per profile, in items and in bytes, with a
// final quota_exceeded that names the cap; a retry of a saved client id is
// still answered, and another profile has its own caps.
func TestVaultSinkQuota(t *testing.T) {
	prev := vaultSaveQuota
	defer func() { vaultSaveQuota = prev }()

	path, _ := vaultEnv(t)
	vaultSaveQuota.Items, vaultSaveQuota.Bytes = 2, 0
	for _, c := range []string{"v-1", "v-2"} {
		if _, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", c)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-3"))
	if err == nil || sinkCode(t, err) != extension.CodeQuotaExceeded || !strings.Contains(err.Error(), "2 files") {
		t.Errorf("past the item cap: %v", err)
	}
	if got, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-1")); err != nil || got.Created {
		t.Errorf("a retry of a saved client id at the cap: %+v, %v", got, err)
	}
	if _, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-home", "v-1")); err != nil {
		t.Errorf("another profile: %v", err)
	}

	path, _ = vaultEnv(t)
	size := int64(len(vaultFile("p-work", "x").Data))
	vaultSaveQuota.Items, vaultSaveQuota.Bytes = 0, 2*size+size/2
	for _, c := range []string{"v-1", "v-2"} {
		if _, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", c)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-3"))
	var re *extension.RequestError
	if !errors.As(err, &re) || re.Code != extension.CodeQuotaExceeded || re.Data.(map[string]any)["limit"] != "bytes" {
		t.Errorf("past the byte cap: %v", err)
	}
}

// The cap is checked inside the save's transaction: saves at once cannot pass
// it together.
func TestVaultSinkQuotaHoldsUnderConcurrency(t *testing.T) {
	prev := vaultSaveQuota
	defer func() { vaultSaveQuota = prev }()
	vaultSaveQuota.Items, vaultSaveQuota.Bytes = 3, 0
	path, _ := vaultEnv(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, refused := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", fmt.Sprintf("v-%d", i)))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				created++
			} else if errors.As(err, new(*extension.RequestError)) && sinkCode(t, err) == extension.CodeQuotaExceeded {
				refused++
			} else {
				t.Errorf("save %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if created != 3 || refused != 5 || len(vaultRows(t, path)) != 3 {
		t.Errorf("created %d, refused %d, rows %d; want 3, 5, 3", created, refused, len(vaultRows(t, path)))
	}
}

// A retry of a client id with other bytes gets the item the first save made:
// its name, type and size, not the retry's.
func TestVaultSinkRetryRepliesWithTheStoredItem(t *testing.T) {
	path, _ := vaultEnv(t)
	first := vaultFile("p-work", "v-1")
	saved, err := extensionVaultSink(path).SaveToVault(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	retry := vaultFile("p-work", "v-1")
	retry.Name, retry.Data = "other.pdf", []byte("%PDF-1.4\nshort file of 35 bytes..")
	got, err := extensionVaultSink(path).SaveToVault(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	want := extension.VaultSaved{ID: saved.ID, Created: false, Name: "invoice.pdf", MIME: "application/pdf", SizeBytes: len(first.Data)}
	if got != want {
		t.Errorf("retry reply = %+v, want the stored item %+v", got, want)
	}
}

// A retry after the item's label was cleared names the item by its file.
func TestVaultSinkRetryNamesAnUnlabelledItemByItsFile(t *testing.T) {
	path, _ := vaultEnv(t)
	saved, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-1"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`UPDATE vault_images SET label = NULL WHERE id = ?`, saved.ID)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, err := extensionVaultSink(path).SaveToVault(context.Background(), vaultFile("p-work", "v-1"))
	if err != nil || got.Created || got.Name != saved.ID+".pdf" {
		t.Errorf("retry of an unlabelled item: %+v, %v; want the name %s.pdf", got, err, saved.ID)
	}
}

// The sink lists the profiles in the database for vault.save's live check.
func TestVaultSinkListsLiveProfiles(t *testing.T) {
	path, _ := vaultEnv(t)
	ids, err := extensionVaultSink(path).LiveProfiles(context.Background())
	if err != nil || !slices.Contains(ids, "p-work") || !slices.Contains(ids, "p-home") || slices.Contains(ids, "p-gone") {
		t.Errorf("LiveProfiles = %v, %v", ids, err)
	}
	if _, err := extensionVaultSink(filepath.Join(t.TempDir(), "none.db")).LiveProfiles(context.Background()); err == nil || sinkCode(t, err) != extension.CodeUnavailable {
		t.Errorf("no database: %v, want unavailable", err)
	}
}
