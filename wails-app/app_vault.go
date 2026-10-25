package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── Secrets Vault ────────────────────────────────────────────────────────────

// Every method below shells out to `monoagentcli secret ...` instead of
// calling internal/secrets directly — the CLI is the single implementation
// surface for vault operations. This file intentionally does not import
// monoagent/internal/secrets.

// VaultEntry mirrors `secret list --json`'s per-entry shape without
// importing internal/secrets — the CLI's --json output is the contract,
// not its Go types.
type VaultEntry struct {
	ID         string `json:"id"`
	ProfileID  string `json:"profile_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	URL        string `json:"url,omitempty"`
	FieldCount int    `json:"field_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// VaultFieldsAndNotes mirrors the CLI reveal command's --json output shape.
type VaultFieldsAndNotes struct {
	Fields map[string]string `json:"fields"`
	Notes  string            `json:"notes"`
}

// VaultExportResult mirrors the CLI export command's --json output shape,
// plus a GUI-only Cancelled flag set when the user dismisses the save
// dialog. Skipped counts entries that failed to decrypt (e.g. an
// unmigrated legacy row) and were left out of the export — see
// secrets.Export.
type VaultExportResult struct {
	Path       string `json:"path"`
	Passphrase string `json:"passphrase"`
	Exported   int    `json:"exported"`
	Skipped    int    `json:"skipped"`
	Cancelled  bool   `json:"cancelled,omitempty"`
}

// VaultImportResult mirrors the CLI import command's --json output shape.
type VaultImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// runVaultCLI runs `monoagentcli --profile <active> --json secret <args...>`,
// optionally piping stdin, and JSON-unmarshals stdout into result (skipped
// if result is nil). On a non-zero exit, the subprocess's stderr becomes
// the returned error — mirrors ExportData's existing exec.ExitError
// handling.
func (a *App) runVaultCLI(stdin string, result interface{}, args ...string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	fullArgs, err := a.scopedArgs(append([]string{"secret"}, args...)...)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(a.ctx, cliBin, fullArgs...)
	suppressConsole(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && cliStderr(ee.Stderr) != "" {
			return fmt.Errorf("%s", cliStderr(ee.Stderr))
		}
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("unexpected vault command output: %w", err)
	}
	return nil
}

func (a *App) ListSecrets() ([]VaultEntry, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var entries []VaultEntry
	if err := a.runVaultCLI("", &entries, "list"); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []VaultEntry{}
	}
	return entries, nil
}

// GetVaultAgentAccess reads the active profile's vault policy through the CLI.
func (a *App) GetVaultAgentAccess() (map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := a.runVaultCLI("", &result, "agent-access"); err != nil {
		return nil, err
	}
	return result, nil
}

// SetVaultAgentAccess is called after the Vault page confirms access with the
// user. The CLI remains responsible for operator checks and profile scoping.
func (a *App) SetVaultAgentAccess(level string) (map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	if level != "strict" && level != "use" && level != "reveal" {
		return nil, fmt.Errorf("invalid vault agent access level %q", level)
	}
	args := []string{"agent-access", level}
	if level != "strict" {
		args = append(args, "--yes")
	}
	var result map[string]interface{}
	if err := a.runVaultCLI("", &result, args...); err != nil {
		return nil, err
	}
	return result, nil
}

// secretStdinPayload is the JSON object piped to `monoagentcli secret
// add/update --stdin-json`: {"value": "...", "fields": {...}}. Secret
// material travels over the pipe, never in argv.
type secretStdinPayload struct {
	Value  string            `json:"value,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

func (a *App) AddSecret(kind, name, username, url, notes string, fields map[string]string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	args := []string{"add", "--stdin-json", "--kind", kind, "--name", name}
	if username != "" {
		args = append(args, "--username", username)
	}
	if url != "" {
		args = append(args, "--url", url)
	}
	if notes != "" {
		args = append(args, "--notes", notes)
	}
	payload, err := json.Marshal(secretStdinPayload{Fields: fields})
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := a.runVaultCLI(string(payload), &result, args...); err != nil {
		return "", err
	}
	return result.ID, nil
}

func (a *App) GetSecretFields(name string) (*VaultFieldsAndNotes, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var result VaultFieldsAndNotes
	if err := a.runVaultCLI("", &result, "reveal", name, "--reveal"); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *App) UpdateSecret(name, newName, username, url, notes string, fields map[string]string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	args := []string{"update", name, "--name", newName, "--username", username, "--url", url, "--notes", notes}
	// Only pipe a field set when there is one — an update touching just
	// metadata must keep the CLI's "don't touch fields" semantics.
	stdin := ""
	if len(fields) > 0 {
		payload, err := json.Marshal(secretStdinPayload{Fields: fields})
		if err != nil {
			return err
		}
		args = append(args, "--stdin-json")
		stdin = string(payload)
	}
	return a.runVaultCLI(stdin, nil, args...)
}

func (a *App) DeleteSecret(name string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runVaultCLI("", nil, "rm", name)
}

// ExportVaultAll prompts for a save location, then exports the active
// profile's vault there. The returned passphrase is shown exactly once by
// the caller — it is never persisted.
func (a *App) ExportVaultAll() (*VaultExportResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Vault",
		DefaultFilename: "vault-export.json.enc",
		// Single extension only: Wails' macOS dialog resolves each ";"-separated
		// Pattern entry via UTType typeWithFilenameExtension:, which returns nil
		// for a compound extension like "json.enc" (embedded dot) — and Wails
		// inserts that nil into an NSMutableArray unguarded, crashing the app
		// (NSInvalidArgumentException: object cannot be nil). "*.enc" alone still
		// matches "vault-export.json.enc" since macOS resolves UTType from the
		// final extension.
		Filters: []runtime.FileFilter{
			{DisplayName: "Vault export", Pattern: "*.enc"},
		},
	})
	if err != nil {
		return nil, err
	}
	if dest == "" {
		return &VaultExportResult{Cancelled: true}, nil
	}
	var result VaultExportResult
	if err := a.runVaultCLI("", &result, "export", "--output", dest); err != nil {
		return nil, err
	}
	return &result, nil
}

// OpenVaultImportFilePicker opens a native file picker for a vault export
// file and returns the selected path (empty if cancelled).
func (a *App) OpenVaultImportFilePicker() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Vault",
		// Single extension only — see the matching comment on ExportVaultAll's
		// SaveFileDialog Filters above; OpenFileDialog has the identical
		// unguarded nil-UTType crash for compound extensions.
		Filters: []runtime.FileFilter{
			{DisplayName: "Vault export", Pattern: "*.enc"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// ImportVaultAll decrypts path with passphrase (piped to the CLI's stdin,
// per the design spec — never a flag) and imports every entry into the
// active profile.
func (a *App) ImportVaultAll(path, passphrase string) (*VaultImportResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var result VaultImportResult
	if err := a.runVaultCLI(passphrase+"\n", &result, "import", path); err != nil {
		return nil, err
	}
	return &result, nil
}
