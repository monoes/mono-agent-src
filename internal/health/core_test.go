package health

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func noop(string) {}

func TestHomeCheckAndFix(t *testing.T) {
	env := &Env{DataDir: filepath.Join(t.TempDir(), ".monoagent")}
	ctx := context.Background()

	res := checkHome(ctx, env)
	if res.Status != StatusFail || res.FixID != FixHomeCreate {
		t.Fatalf("missing dir: %+v", res)
	}
	if err := fixHomeCreate(ctx, env, noop); err != nil {
		t.Fatal(err)
	}
	if res := checkHome(ctx, env); res.Status != StatusOK {
		t.Fatalf("after fix: %+v", res)
	}
	entries, _ := os.ReadDir(env.DataDir)
	if len(entries) != 0 {
		t.Fatalf("writability probe left files behind: %v", entries)
	}
}

func TestDBCheck(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		env  Env
		want Status
		fix  string
	}{
		{"open error", Env{DBErr: errors.New("locked")}, StatusFail, ""},
		{"missing", Env{}, StatusFail, FixDBMigrate},
		{"damaged", Env{DB: fakeDB(t), QuickCheck: func(context.Context) (string, error) { return "page 3 corrupt", nil }}, StatusFail, ""},
		{"pending", Env{DB: fakeDB(t), PendingMigrations: func(context.Context) ([]string, error) { return []string{"099_x.sql"}, nil }}, StatusWarn, FixDBMigrate},
		{"current", Env{DB: fakeDB(t), PendingMigrations: func(context.Context) ([]string, error) { return nil, nil }}, StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := checkDB(ctx, &tc.env)
			if res.Status != tc.want || res.FixID != tc.fix {
				t.Fatalf("got %q/%q, want %q/%q (%s)", res.Status, res.FixID, tc.want, tc.fix, res.Summary)
			}
		})
	}
}

func TestProfileCheck(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "p1")
	env := &Env{ProfileID: "p1", ProfileRoot: func(string) string { return root },
		EnsureProfile: func(string) error { return os.MkdirAll(root, 0o700) }}

	if res := checkProfile(ctx, env); res.Status != StatusWarn || res.FixID != FixProfileLayout {
		t.Fatalf("missing folder: %+v", res)
	}
	if err := fixProfileLayout(ctx, env, noop); err != nil {
		t.Fatal(err)
	}
	if res := checkProfile(ctx, env); res.Status != StatusOK {
		t.Fatalf("after fix: %+v", res)
	}

	env.ProfileRoot = func(string) string { return "" }
	if res := checkProfile(ctx, env); res.Status != StatusFail {
		t.Fatalf("invalid id: %+v", res)
	}
}

func TestVaultCheckMapsStates(t *testing.T) {
	want := map[string]Status{
		"ok": StatusOK, "uninitialized": StatusInfo, "file-keyring": StatusWarn,
		"keyring-unavailable": StatusFail, "key-missing": StatusFail, "key-mismatch": StatusFail, "": StatusFail,
	}
	for state, st := range want {
		env := &Env{ProfileID: "p1", VaultState: func(context.Context, string) (string, error) { return state, nil }}
		if res := checkVault(context.Background(), env); res.Status != st {
			t.Errorf("state %q: %q, want %q", state, res.Status, st)
		}
	}
	if res := checkVault(context.Background(), &Env{}); res.Status != StatusSkip {
		t.Errorf("no hook: %q, want skip", res.Status)
	}
}

func TestDiskCheck(t *testing.T) {
	low := &Env{FreeBytes: func(string) (uint64, error) { return 10 << 20, nil }}
	if res := checkDisk(context.Background(), low); res.Status != StatusWarn {
		t.Errorf("low disk: %+v", res)
	}
	ok := &Env{FreeBytes: func(string) (uint64, error) { return 50 << 30, nil }}
	if res := checkDisk(context.Background(), ok); res.Status != StatusOK || res.Summary != "50.0 GiB free" {
		t.Errorf("plenty: %+v", res)
	}
	if free, err := FreeBytes(t.TempDir()); err != nil || free == 0 {
		t.Errorf("FreeBytes on a real dir = %d, %v", free, err)
	}
}

func TestUpdateCheck(t *testing.T) {
	latest := func(v string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return v, nil }
	}
	cases := []struct {
		cur, latest string
		want        Status
	}{
		{"v1.2.0", "v1.2.0", StatusOK},
		{"v1.2.0", "v1.3.0", StatusWarn},
		{"dev", "v1.3.0", StatusSkip},
		{"v1.2.0-4-gabcdef", "v1.3.0", StatusSkip},
	}
	for _, tc := range cases {
		res := checkUpdate(context.Background(), &Env{Version: tc.cur, LatestVersion: latest(tc.latest)})
		if res.Status != tc.want {
			t.Errorf("%s→%s: %q, want %q", tc.cur, tc.latest, res.Status, tc.want)
		}
	}
}

func TestRunStreaming(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	var lines []string
	if err := RunStreaming(context.Background(), func(l string) { lines = append(lines, l) }, sh, "-c", "echo one; echo two >&2"); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	err = RunStreaming(context.Background(), noop, sh, "-c", "echo why; exit 3")
	if err == nil || !strings.Contains(err.Error(), "why") {
		t.Fatalf("failure should carry output tail: %v", err)
	}
}

func fakeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func TestProfileCheckDoesNotPassWhenProfilesCannotBeRead(t *testing.T) {
	ctx := context.Background()
	db := fakeDB(t) // no migrations: there is no profiles table
	if res := checkProfile(ctx, &Env{ProfileID: "p1", DB: db}); res.Status != StatusWarn {
		t.Fatalf("a failed profile query: %+v, want warn", res)
	}
	if _, err := db.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if res := checkProfile(ctx, &Env{ProfileID: "p1", DB: db}); res.Status != StatusFail {
		t.Fatalf("no profiles at all: %+v, want fail", res)
	}
}
