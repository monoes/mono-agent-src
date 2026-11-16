package orgenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// profileEnv is an Env on a migrated database of its own whose profile
// "work" has its root at a temp dir.
func profileEnv(t *testing.T) (*Env, *storage.Database, string) {
	t.Helper()
	db := testdb.Open(t)
	root := t.TempDir()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, root_dir) VALUES ('work', 'Work', ?)`, root); err != nil {
		t.Fatal(err)
	}
	opened := 0
	e := New(Deps{
		OpenDB:    func() (*storage.Database, error) { opened++; return db, nil },
		ProfileID: func() string { return "work" },
		Project:   func() string { return "" },
	})
	t.Cleanup(func() {
		if opened > 1 {
			t.Errorf("OpenDB called %d times, want at most once", opened)
		}
	})
	return e, db, root
}

func TestRootIsTheProfileRoot(t *testing.T) {
	t.Parallel()
	e, db, root := profileEnv(t)
	if got, err := e.RootErr(); err != nil || got != root {
		t.Fatalf("RootErr() = %q, %v, want %q", got, err, root)
	}
	gotDB, pid, gotRoot, err := e.Profile()
	if err != nil || gotDB != db || pid != "work" || gotRoot != root {
		t.Fatalf("Profile() = %v, %q, %q, %v", gotDB, pid, gotRoot, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".monomind")); err != nil {
		t.Fatalf("the profile layout was not made: %v", err)
	}
}

func TestProjectFlagWinsWithoutOpeningTheDB(t *testing.T) {
	t.Parallel()
	e := New(Deps{
		OpenDB:  func() (*storage.Database, error) { t.Fatal("OpenDB called"); return nil, nil },
		Project: func() string { return "/some/project" },
	})
	if got, err := e.RootErr(); err != nil || got != "/some/project" {
		t.Fatalf("RootErr() = %q, %v", got, err)
	}
	e.Close()
}

func TestProfileRefusesAnotherProject(t *testing.T) {
	t.Parallel()
	e, _, root := profileEnv(t)
	project := root
	e.Project = func() string { return project }
	if _, _, _, err := e.Profile(); err != nil {
		t.Fatalf("--project at the profile root: %v", err)
	}
	project = t.TempDir()
	_, _, _, err := e.Profile()
	if err == nil || !strings.Contains(err.Error(), `is not the org root of profile "work"`) {
		t.Fatalf("--project elsewhere: %v", err)
	}
}

// A database that cannot be opened gives no root: the profile is never guessed, "default" included.
func TestRootErrsWhenTheDBFails(t *testing.T) {
	t.Parallel()
	for _, pid := range []string{"", "other"} {
		e := New(Deps{
			OpenDB:    func() (*storage.Database, error) { return nil, errors.New("no db") },
			ProfileID: func() string { return pid },
			Project:   func() string { return "" },
		})
		if got, err := e.RootErr(); got != "" || err == nil || err.Error() != "no db" {
			t.Fatalf("profile %q: RootErr() = %q, %v", pid, got, err)
		}
		if _, _, _, err := e.Profile(); err == nil || err.Error() != "no db" {
			t.Fatalf("profile %q: Profile() err = %v", pid, err)
		}
	}
}

func TestRootErrWithAProject(t *testing.T) {
	t.Parallel()
	e := New(Deps{Project: func() string { return "/some/project" }})
	if got, err := e.RootErr(); err != nil || got != "/some/project" {
		t.Fatalf("RootErr() = %q, %v", got, err)
	}
}

func TestBadProfileIDIsRefused(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	e := New(Deps{
		OpenDB:    func() (*storage.Database, error) { return db, nil },
		ProfileID: func() string { return "../x" },
		Project:   func() string { return "" },
	})
	_, _, _, err := e.Profile()
	if err == nil || !strings.HasPrefix(err.Error(), `preparing profile folder for "../x": `) {
		t.Fatalf("Profile() err = %v", err)
	}
}

func TestAPIAddrOrder(t *testing.T) {
	db := testdb.Open(t)
	t.Setenv("MONOAGENT_API_ADDR", "")
	if got := APIAddr(nil); got != orggrant.DefaultAPIAddr {
		t.Fatalf("no db: %q", got)
	}
	if _, err := db.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, '127.0.0.1:9999')`, DaemonAPIAddrSetting); err != nil {
		t.Fatal(err)
	}
	if got := APIAddr(db); got != "127.0.0.1:9999" {
		t.Fatalf("recorded: %q", got)
	}
	t.Setenv("MONOAGENT_API_ADDR", "10.0.0.1:1")
	if got := APIAddr(db); got != "10.0.0.1:1" {
		t.Fatalf("env: %q", got)
	}
}

func TestSelfExecutableIsAbsolute(t *testing.T) {
	t.Parallel()
	if got := SelfExecutable(); !filepath.IsAbs(got) {
		t.Fatalf("SelfExecutable() = %q", got)
	}
}

func TestLockRowsWaitsForTheHolder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".monomind"), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := LockRows(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		r, err := LockRows(root)
		if err == nil {
			r()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("second LockRows returned while the lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-got; err != nil {
		t.Fatalf("second LockRows after release: %v", err)
	}
}

func TestWithSignature(t *testing.T) {
	prevOn, prevAll := SigningOn, SignCheckAll
	t.Cleanup(func() { SigningOn, SignCheckAll = prevOn, prevAll })
	payload := []byte(`{"v":1,"items":[{"name":"growth"},{"name":"../bad"}]}`)

	SigningOn = func(context.Context, string) bool { return false }
	if got := WithSignature(context.Background(), t.TempDir(), payload); string(got) != string(payload) {
		t.Fatalf("not enforced: %s", got)
	}

	SigningOn = func(context.Context, string) bool { return true }
	SignCheckAll = func(context.Context, string) (map[string]orgsign.Status, bool) {
		return map[string]orgsign.Status{"growth": {State: "signed"}}, true
	}
	got := string(WithSignature(context.Background(), t.TempDir(), payload))
	if !strings.Contains(got, `{"name":"growth","signature":{"state":"signed"`) || strings.Contains(got, `"name":"../bad","signature"`) {
		t.Fatalf("enforced: %s", got)
	}
	if got := WithSignature(context.Background(), t.TempDir(), []byte("not json")); string(got) != "not json" {
		t.Fatalf("bad payload: %s", got)
	}
}

func TestNoProfileIsAnErrorNotTheDefaultRoot(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	e := New(Deps{
		OpenDB:    func() (*storage.Database, error) { return db, nil },
		ProfileID: func() string { return "" },
		Project:   func() string { return "" },
	})
	if _, err := e.RootErr(); !errors.Is(err, ErrProfileRequired) {
		t.Fatalf("RootErr() err = %v, want ErrProfileRequired", err)
	}
	if _, _, _, err := e.Profile(); !errors.Is(err, ErrProfileRequired) {
		t.Fatalf("Profile() err = %v, want ErrProfileRequired", err)
	}
}

// Replacing an org by a whole document drops the signature the operator gave
// the old one: the new bytes are not what was reviewed.
func TestDropSignatureWithdrawsTheOrgSignature(t *testing.T) {
	root := t.TempDir()
	path, err := orgsign.SignaturePath(root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := strings.TrimSuffix(path, ".json") + ".projection.json"
	for _, p := range []string{path, proj} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	DropSignature(root, "growth")
	for _, p := range []string{path, proj} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", p, err)
		}
	}
}
