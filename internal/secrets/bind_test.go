package secrets

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

func openAt(t *testing.T, path string) *storage.Database {
	t.Helper()
	testdb.MustSeed(path)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

// copyDB copies a closed-over database file to dst (a copy made by an agent, a script, a backup).
func copyDB(t *testing.T, src *storage.Database, srcPath, dst string) {
	t.Helper()
	if _, err := src.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A copy of the database in another place does not open the vault, though the OS key store gives
// the same key to both.
func TestBind_CopyElsewhereDoesNotUnwrap(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	orig := filepath.Join(dir, "a", "monoagent.db")
	os.MkdirAll(filepath.Dir(orig), 0o700)
	db := openAt(t, orig)
	dek, err := getOrCreateDEK(ctx, db.DB, "p1")
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "b", "monoagent.db")
	copyDB(t, db, orig, cp)
	db2 := openAt(t, cp)
	if _, err := getOrCreateDEK(ctx, db2.DB, "p1"); err == nil {
		t.Fatal("the copy unwrapped the DEK")
	}
	st, _ := CheckVault(ctx, db2.DB, "p1")
	if st == VaultOK {
		t.Fatalf("CheckVault on the copy: %s", st)
	}
	again, err := getOrCreateDEK(ctx, db.DB, "p1")
	if err != nil || string(again) != string(dek) {
		t.Fatalf("the original: %v", err)
	}
}

// One install reached through a symlink is one install.
func TestBind_SymlinkIsTheSameInstall(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.MkdirAll(real, 0o700)
	db := openAt(t, filepath.Join(real, "monoagent.db"))
	dek, err := getOrCreateDEK(ctx, db.DB, "p1")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	db2 := openAt(t, filepath.Join(link, "monoagent.db"))
	got, err := getOrCreateDEK(ctx, db2.DB, "p1")
	if err != nil || string(got) != string(dek) {
		t.Fatalf("through the symlink: %v", err)
	}
}

// A database wrapped by an earlier release (no binding) is rewrapped at the user's own install,
// and refused in a copy elsewhere.
func TestBind_LegacyRewrapOwnInstallOnly(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	ownPath := filepath.Join(dir, "home", ".monoagent", "monoagent.db")
	os.MkdirAll(filepath.Dir(ownPath), 0o700)
	prev := ownInstallDB
	ownInstallDB = func() string { return ownPath }
	t.Cleanup(func() { ownInstallDB = prev })

	db := openAt(t, ownPath)
	kek, err := getOrCreateKEK("p1")
	if err != nil {
		t.Fatal(err)
	}
	dek := make([]byte, 32)
	dek[0] = 7
	w, n, _ := Encrypt(kek, dek) // wrapped as before the binding
	if _, err := db.DB.Exec(`INSERT INTO vault_keys (profile_id, wrapped_dek, wrapped_nonce, created_at) VALUES ('p1', ?, ?, 'x')`, w, n); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "scratch", "monoagent.db")
	copyDB(t, db, ownPath, cp)
	db2 := openAt(t, cp)
	if _, err := getOrCreateDEK(ctx, db2.DB, "p1"); !errors.Is(err, ErrNotBound) {
		t.Fatalf("the legacy copy: %v", err)
	}

	got, err := getOrCreateDEK(ctx, db.DB, "p1")
	if err != nil || string(got) != string(dek) {
		t.Fatalf("own install: %v", err)
	}
	var w2 []byte
	db.DB.QueryRow(`SELECT wrapped_dek FROM vault_keys WHERE profile_id='p1'`).Scan(&w2)
	if string(w2) == string(w) {
		t.Fatal("the row was not rewrapped")
	}
	// A copy made after the rewrap holds a bound row: still refused, with no legacy fallback.
	cp2 := filepath.Join(dir, "scratch2", "monoagent.db")
	copyDB(t, db, ownPath, cp2)
	db3 := openAt(t, cp2)
	if _, err := getOrCreateDEK(ctx, db3.DB, "p1"); err == nil {
		t.Fatal("a copy of the bound row unwrapped")
	}
}

// A database restored or moved on purpose is adopted by Rebind, from the old path.
func TestBind_RebindAfterMove(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	old := filepath.Join(dir, "old", "monoagent.db")
	os.MkdirAll(filepath.Dir(old), 0o700)
	db := openAt(t, old)
	dek, err := getOrCreateDEK(ctx, db.DB, "p1")
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "new", "monoagent.db")
	copyDB(t, db, old, moved)
	db2 := openAt(t, moved)
	if _, err := Rebind(ctx, db2.DB, "p1", ""); err == nil {
		t.Fatal("rebind without the old path adopted a bound key")
	}
	if _, err := Rebind(ctx, db2.DB, "p1", filepath.Join(dir, "elsewhere", "monoagent.db")); err == nil {
		t.Fatal("rebind from a wrong path")
	}
	if _, err := Rebind(ctx, db2.DB, "p1", old); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	got, err := getOrCreateDEK(ctx, db2.DB, "p1")
	if err != nil || string(got) != string(dek) {
		t.Fatalf("after rebind: %v", err)
	}
}

// A bound DEK opened at another path is reported as not bound (never the generic key mismatch),
// with the path it was made for, which `doctor` and `secret rebind` use.
func TestBind_CopyElsewhereIsNotBoundWithHint(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	orig := filepath.Join(dir, "a", "monoagent.db")
	os.MkdirAll(filepath.Dir(orig), 0o700)
	db := openAt(t, orig)
	if _, err := getOrCreateDEK(ctx, db.DB, "p1"); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "b", "monoagent.db")
	copyDB(t, db, orig, cp)
	db2 := openAt(t, cp)
	_, err := getOrCreateDEK(ctx, db2.DB, "p1")
	if !errors.Is(err, ErrNotBound) || !strings.Contains(err.Error(), canonicalPath(orig)) {
		t.Fatalf("copy: %v", err)
	}
	st, err := CheckVault(ctx, db2.DB, "p1")
	if st != VaultNotBound || err == nil || !strings.Contains(err.Error(), "--from "+canonicalPath(orig)) {
		t.Fatalf("CheckVault = %s, %v", st, err)
	}
	// A hint naming a path the key was not bound to is only a hint: no false not-bound.
	if _, err := db2.DB.Exec(`UPDATE vault_key_hints SET bound_path = ?`, filepath.Join(dir, "zzz", "monoagent.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := getOrCreateDEK(ctx, db2.DB, "p1"); errors.Is(err, ErrNotBound) {
		t.Fatalf("a wrong hint reported not bound: %v", err)
	}
}

// Rebind takes the old path relative to the working directory, and as typed when the
// directory it named is gone.
func TestBind_RebindFromRelativeAndGonePath(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	old := filepath.Join(dir, "old", "monoagent.db")
	os.MkdirAll(filepath.Dir(old), 0o700)
	db := openAt(t, old)
	dek, err := getOrCreateDEK(ctx, db.DB, "p1")
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "new", "monoagent.db")
	copyDB(t, db, old, moved)
	db.DB.Close()
	if err := os.RemoveAll(filepath.Dir(old)); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	db2 := openAt(t, moved)
	if _, err := Rebind(ctx, db2.DB, "p1", filepath.Join("old", "monoagent.db")); err != nil {
		t.Fatalf("rebind from a relative, gone path: %v", err)
	}
	got, err := getOrCreateDEK(ctx, db2.DB, "p1")
	if err != nil || string(got) != string(dek) {
		t.Fatalf("after rebind: %v", err)
	}
}

// The user's own database path comes from the system's user database, not $HOME or $USER.
func TestOwnInstallDB_IgnoresEnvironment(t *testing.T) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		t.Skip("no /etc/passwd")
	}
	defer f.Close()
	home := homeFromPasswd(f, strconv.Itoa(os.Getuid()))
	if home == "" {
		t.Skip("uid not in /etc/passwd")
	}
	testhome.SetHome(t, "/nonexistent-evil-home")
	t.Setenv("USER", "evil")
	t.Setenv("LOGNAME", "evil")
	if got, want := ownInstallDB(), filepath.Join(home, ".monoagent", "monoagent.db"); got != want {
		t.Fatalf("ownInstallDB = %q, want %q", got, want)
	}
}

func TestHomeFromPasswd(t *testing.T) {
	const passwd = "root:x:0:0:root:/root:/bin/bash\n# c\nbob:x:1000:1000:Bob:/home/bob:/bin/sh\nbad line\n"
	if got := homeFromPasswd(strings.NewReader(passwd), "1000"); got != "/home/bob" {
		t.Fatalf("got %q", got)
	}
	if got := homeFromPasswd(strings.NewReader(passwd), "5"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// Rebind without --from names a recorded old path only after the key opens with it, and shows it
// without control characters.
func TestBind_RebindHintIsVerified(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	old := filepath.Join(dir, "old", "monoagent.db")
	os.MkdirAll(filepath.Dir(old), 0o700)
	db := openAt(t, old)
	if _, err := getOrCreateDEK(ctx, db.DB, "p1"); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "new", "monoagent.db")
	copyDB(t, db, old, moved)
	db2 := openAt(t, moved)

	forged := "/etc/passwd\x1b[31m delete everything \x07"
	if _, err := db2.DB.Exec(`INSERT INTO vault_key_hints (profile_id, bound_path) VALUES ('p1', ?)
		ON CONFLICT(profile_id) DO UPDATE SET bound_path = excluded.bound_path`, forged); err != nil {
		t.Fatal(err)
	}
	_, err := Rebind(ctx, db2.DB, "p1", "")
	if err == nil || strings.Contains(err.Error(), "try --from") || strings.Contains(err.Error(), "passwd") {
		t.Fatalf("an unverified hint was shown: %v", err)
	}

	if _, err := db2.DB.Exec(`UPDATE vault_key_hints SET bound_path = ?`, canonicalPath(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = Rebind(ctx, db2.DB, "p1", ""); err == nil || !strings.Contains(err.Error(), "try --from "+canonicalPath(old)) {
		t.Fatalf("a verified hint was not shown: %v", err)
	}
}

func TestOwnedHome(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "me")
	os.Mkdir(home, 0o700)
	uid := os.Getuid()
	if !ownedHome(home, uid, parent) {
		t.Fatal("own directory refused")
	}
	if ownedHome(home, uid+1, parent) {
		t.Fatal("another uid's directory accepted")
	}
	if ownedHome(home, uid, filepath.Join(parent, "x")) {
		t.Fatal("a directory outside the expected parent accepted")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if ownedHome(link, uid, parent) {
		t.Fatal("a symlink accepted")
	}
	if ownedHome("", uid, parent) || ownedHome("relative", uid, parent) || ownedHome(filepath.Join(parent, "gone"), uid, parent) {
		t.Fatal("empty, relative or missing path accepted")
	}
}
