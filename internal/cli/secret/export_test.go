package secret

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretExportImport_RoundTrip(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "openai-key", "--value", "v-test1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	exportOut, err := runSecretCmd(t, dbPath, "export", "--output", exportPath)
	if err != nil {
		t.Fatalf("secret export: %v (%s)", err, exportOut)
	}
	var exportResult struct {
		Path       string `json:"path"`
		Passphrase string `json:"passphrase"`
		Exported   int    `json:"exported"`
		Skipped    int    `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(exportOut), &exportResult); err != nil {
		t.Fatalf("unmarshal export output: %v", err)
	}
	if exportResult.Passphrase == "" {
		t.Fatal("expected a non-empty generated passphrase")
	}
	if exportResult.Exported != 1 || exportResult.Skipped != 0 {
		t.Fatalf("expected 1 exported, 0 skipped, got %+v", exportResult)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("expected export file to exist: %v", err)
	}
	exportBytes, _ := os.ReadFile(exportPath)
	if strings.Contains(string(exportBytes), "v-test1") {
		t.Fatal("export file must not contain plaintext")
	}

	// Import into a fresh, empty vault.
	importDBPath := newSecretCLITestDB(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString(exportResult.Passphrase + "\n")
		w.Close()
	}()
	importOut, err := runSecretCmd(t, importDBPath, "import", exportPath)
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret import: %v (%s)", err, importOut)
	}
	var importResult struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(importOut), &importResult); err != nil {
		t.Fatalf("unmarshal import output: %v", err)
	}
	if importResult.Imported != 1 || importResult.Skipped != 0 {
		t.Fatalf("expected 1 imported, 0 skipped, got %+v", importResult)
	}

	revealOut, err := runSecretCmd(t, importDBPath, "reveal", "openai-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal after import: %v", err)
	}
	if !strings.Contains(revealOut, "v-test1") {
		t.Fatalf("expected imported entry to decrypt correctly, got: %s", revealOut)
	}
}

func TestSecretImport_WrongPassphraseFails(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	if _, err := runSecretCmd(t, dbPath, "export", "--output", exportPath); err != nil {
		t.Fatalf("secret export: %v", err)
	}

	importDBPath := newSecretCLITestDB(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString("pw-incorrect1\n")
		w.Close()
	}()
	_, err = runSecretCmd(t, importDBPath, "import", exportPath)
	os.Stdin = orig
	if err == nil {
		t.Fatal("expected import with an incorrect passphrase to fail")
	}
}

func TestSecretImport_SkipsDuplicateNames(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "shared-key", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	exportOut, err := runSecretCmd(t, dbPath, "export", "--output", exportPath)
	if err != nil {
		t.Fatalf("secret export: %v", err)
	}
	var exportResult struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal([]byte(exportOut), &exportResult); err != nil {
		t.Fatalf("unmarshal export output: %v", err)
	}

	// Import into the SAME vault, which already has "shared-key" — it must be skipped.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString(exportResult.Passphrase + "\n")
		w.Close()
	}()
	importOut, err := runSecretCmd(t, dbPath, "import", exportPath)
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret import: %v (%s)", err, importOut)
	}
	var importResult struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(importOut), &importResult); err != nil {
		t.Fatalf("unmarshal import output: %v", err)
	}
	if importResult.Imported != 0 || importResult.Skipped != 1 {
		t.Fatalf("expected 0 imported, 1 skipped, got %+v", importResult)
	}
}

func TestSecretExport_DefaultsOutputFilename(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(origWD)

	out, err := runSecretCmd(t, dbPath, "export")
	if err != nil {
		t.Fatalf("secret export: %v (%s)", err, out)
	}
	var result struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(result.Path, "vault-export-") || !strings.HasSuffix(result.Path, ".json.enc") {
		t.Fatalf("expected default filename pattern vault-export-*.json.enc, got %q", result.Path)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("expected default-named export file to exist: %v", err)
	}
}
