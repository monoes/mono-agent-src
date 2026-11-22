package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadJSONFileOnlyReadsPickedPaths(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"k":"v"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadJSONFile(secret); err == nil || !strings.Contains(err.Error(), "not picked") {
		t.Fatalf("an unpicked path was read: %v", err)
	}
	a.notePickedJSON(secret)
	got, err := a.ReadJSONFile(secret)
	if err != nil || got["k"] != "v" {
		t.Fatalf("picked path: %v, %v", got, err)
	}
}

func TestReadJSONFileCapsSize(t *testing.T) {
	a := newTestApp(t)
	big := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(big, []byte(`{"k":"`+strings.Repeat("a", maxJSONFileBytes)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.notePickedJSON(big)
	if _, err := a.ReadJSONFile(big); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("an oversize file was read: %v", err)
	}
}
