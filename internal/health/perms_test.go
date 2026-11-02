package health

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPermsCheckAndFix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes do not apply on Windows")
	}
	ctx := context.Background()
	home := t.TempDir()
	env := &Env{Home: home, DataDir: filepath.Join(home, ".monoagent")}
	env.DBPath = filepath.Join(env.DataDir, "monoagent.db")

	// No data folder yet: nothing to expose.
	if res := checkPerms(ctx, env); res.Status != StatusOK {
		t.Fatalf("missing folder: %+v", res)
	}

	if err := os.MkdirAll(env.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{env.DBPath, env.DBPath + "-shm", filepath.Join(env.DataDir, "workflow-imports.json")} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if res := checkPerms(ctx, env); res.Status != StatusOK || res.FixID != "" {
		t.Fatalf("private install: %+v", res)
	}

	// An install from before the fix: folder 0755, database 0644.
	if err := os.Chmod(env.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(env.DBPath, 0o644); err != nil {
		t.Fatal(err)
	}
	res := checkPerms(ctx, env)
	if res.Status != StatusWarn || res.FixID != FixPerms {
		t.Fatalf("exposed install: %+v", res)
	}
	if !strings.Contains(res.Detail, env.DBPath) || !strings.Contains(res.Detail, env.DataDir) {
		t.Fatalf("detail should name the folder and the database: %q", res.Detail)
	}

	var lines []string
	if err := fixPerms(ctx, env, func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("progress %q, want the folder and the database", lines)
	}
	if res := checkPerms(ctx, env); res.Status != StatusOK {
		t.Fatalf("after fix: %+v", res)
	}
	for p, want := range map[string]os.FileMode{env.DataDir: 0o700, env.DBPath: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != want { // Windows has no Unix modes
			t.Errorf("%s: %v, want %v", p, info.Mode().Perm(), want)
		}
	}
}

func TestPermsCheckIsRegistered(t *testing.T) {
	reg := Default()
	var check bool
	for _, c := range reg.Checks() {
		check = check || c.ID == CheckPerms
	}
	_, fix := reg.Fix(FixPerms)
	if !check || !fix {
		t.Fatalf("check registered %v, fix registered %v", check, fix)
	}
}
