package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

// The configured passphrase file: a well-known, fixed location
// (~/.monoagent/keyring-passphrase) that `monoagentcli secret keyring
// set-passphrase` writes, so a desktop user can make the file keyring
// usable without exporting MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE into the
// app's environment. Its presence IS the configuration: the file keyring
// itself always lives under ~/.monoagent (defaultVaultDir), so there is no
// second place (a settings row, a config file) that could drift from it.
//
// Resolution order (promptFilePassphrase): the env var's file → this file →
// stdin → /dev/tty → a clear error.
const configuredPassphraseFilename = "keyring-passphrase"

// ConfiguredPassphrasePath is where `secret keyring set-passphrase` stores
// the file-keyring passphrase.
func ConfiguredPassphrasePath() string {
	return filepath.Join(filepath.Dir(defaultVaultDir()), configuredPassphraseFilename)
}

// keyringProbeAccount is looked up (never written) to learn whether the OS
// keyring answers at all. It never exists, so a working keyring returns
// ErrNotFound without handing back any secret.
const keyringProbeAccount = "availability-probe"

// Keyring backends reported by KeyringStatus.
const (
	KeyringBackendOS          = "os"
	KeyringBackendFile        = "file"
	KeyringBackendUnavailable = "unavailable"
)

// PassphraseFileStatus describes the passphrase file the file keyring would
// read without prompting. The passphrase itself is never part of it.
type PassphraseFileStatus struct {
	// Source is "env" (MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE), "configured"
	// (~/.monoagent/keyring-passphrase) or "none".
	Source     string `json:"source"`
	Configured bool   `json:"configured"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Mode       string `json:"mode,omitempty"`
	// OK: the file exists, only its owner can read it, and it is not empty.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// KeyringStatusInfo is the JSON shape of `secret keyring status`.
type KeyringStatusInfo struct {
	// Backend: "os" (an OS keychain answers), "file" (none does, and the
	// file keyring is allowed), "unavailable" (none does, file keyring not
	// allowed — vault writes fail closed).
	Backend            string               `json:"backend"`
	OSKeyringError     string               `json:"os_keyring_error,omitempty"`
	FileKeyringAllowed bool                 `json:"file_keyring_allowed"`
	FileKeyrings       []string             `json:"file_keyrings"` // ids with a file keyring: profile ids, and the account key's (monoes..account)
	PassphraseFile     PassphraseFileStatus `json:"passphrase_file"`
	// Notice is set when the file keyring's wrap protects nothing: its passphrase file sits in the
	// same folder (and the same backups) as the wrapped key.
	Notice string `json:"notice,omitempty"`
}

// passphraseBesideKeyNotice is KeyringStatusInfo.Notice for a passphrase file kept in the folder of
// the keyrings it unlocks.
const passphraseBesideKeyNotice = "the passphrase file is stored next to the wrapped key (same folder, same backups), " +
	"so the passphrase wrap is cosmetic: anyone who can read ~/.monoagent can unlock the vault. " +
	"Remove the passphrase file and type the passphrase at each prompt to make the wrap count."

// KeyringStatus reports which key store the vault uses on this host and
// whether a passphrase file is set up for the file keyring. It never
// unlocks anything and never prompts.
func KeyringStatus() KeyringStatusInfo {
	st := KeyringStatusInfo{
		FileKeyringAllowed: fileKeyringEnabled(),
		FileKeyrings:       existingFileKeyrings(),
		PassphraseFile:     passphraseFileStatus(),
	}
	if st.PassphraseFile.Exists && len(st.FileKeyrings) > 0 && filepath.Dir(st.PassphraseFile.Path) == filepath.Dir(defaultVaultDir()) {
		st.Notice = passphraseBesideKeyNotice
	}
	keyringIOMu.Lock()
	_, err := keyringGet(keyringService, keyringProbeAccount)
	keyringIOMu.Unlock()
	switch {
	case err == nil || errors.Is(err, keyring.ErrNotFound):
		st.Backend = KeyringBackendOS
	case st.FileKeyringAllowed:
		st.Backend = KeyringBackendFile
		st.OSKeyringError = err.Error()
	default:
		st.Backend = KeyringBackendUnavailable
		st.OSKeyringError = err.Error()
	}
	return st
}

// existingFileKeyrings lists the ids that have a file keyring: the profile ids,
// and the monoes.me account key's (accountKEKID) once a sign-in made one there.
func existingFileKeyrings() []string {
	out := []string{}
	entries, err := os.ReadDir(defaultVaultDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if id, ok := strings.CutPrefix(e.Name(), fileKeyringFilename); ok && id != "" && e.Type().IsRegular() {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func passphraseFileStatus() PassphraseFileStatus {
	st := PassphraseFileStatus{Source: "none", Path: ConfiguredPassphrasePath()}
	if p := os.Getenv(filePassphraseFileEnv); p != "" {
		st.Source, st.Path = "env", p
	}
	info, err := os.Stat(st.Path)
	if err != nil {
		if st.Source == "env" {
			st.Configured = true
			st.Error = err.Error()
		}
		return st
	}
	if st.Source == "none" {
		st.Source = "configured"
	}
	st.Configured = true
	st.Exists = true
	st.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
	if _, err := readPassphraseFileAs(st.Path, passphraseFileLabel(st.Source)); err != nil {
		st.Error = err.Error()
	} else {
		st.OK = true
	}
	return st
}

func passphraseFileLabel(source string) string {
	if source == "env" {
		return filePassphraseFileEnv
	}
	return "configured passphrase file"
}

// ReadPassphraseInput reads one passphrase line from r (echo off when r is
// a terminal, prompting on prompt). For `secret keyring set-passphrase`.
func ReadPassphraseInput(r io.Reader, prompt io.Writer) (string, error) {
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(prompt, "File-keyring passphrase: ")
	}
	return readPassphraseLine(r, prompt)
}

// SetConfiguredPassphrase stores pass in ~/.monoagent/keyring-passphrase
// (mode 0600, parent dir 0700) after checking it unlocks every existing file
// keyring — a passphrase that doesn't would only turn every later vault
// access into an "incorrect passphrase" error. Returns the file's path.
func SetConfiguredPassphrase(pass string) (string, error) {
	pass = strings.TrimRight(pass, "\r\n")
	if pass == "" {
		return "", errors.New("secrets: empty passphrase")
	}
	if strings.ContainsAny(pass, "\r\n") {
		return "", errors.New("secrets: the passphrase must be a single line")
	}
	for _, id := range existingFileKeyrings() {
		if err := verifyFileKeyringPassphrase(id, pass); err != nil {
			return "", err
		}
	}

	path := ConfiguredPassphrasePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("secrets: creating %s: %w", dir, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("secrets: chmod 700 %s: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".keyring-passphrase-*")
	if err != nil {
		return "", fmt.Errorf("secrets: writing passphrase file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("secrets: writing passphrase file: %w", err)
	}
	if _, err := tmp.WriteString(pass + "\n"); err != nil {
		tmp.Close()
		return "", fmt.Errorf("secrets: writing passphrase file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("secrets: writing passphrase file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("secrets: writing passphrase file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("secrets: writing passphrase file: %w", err)
	}
	return path, nil
}

// ClearConfiguredPassphrase deletes ~/.monoagent/keyring-passphrase.
// removed is false when there was none.
func ClearConfiguredPassphrase() (path string, removed bool, err error) {
	path = ConfiguredPassphrasePath()
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, false, nil
		}
		return path, false, fmt.Errorf("secrets: removing %s: %w", path, err)
	}
	return path, true, nil
}

// verifyFileKeyringPassphrase checks that pass unwraps profileID's file
// keyring. A pre-hardening raw-KEK file has no passphrase yet (the next use
// wraps it under whichever passphrase is supplied), so it accepts any.
func verifyFileKeyringPassphrase(profileID, pass string) error {
	data, err := os.ReadFile(fileKeyringPath(profileID))
	if err != nil {
		return fmt.Errorf("secrets: reading file keyring of profile %q: %w", profileID, err)
	}
	var env fileKEKEnvelope
	if jsonErr := json.Unmarshal(data, &env); jsonErr != nil {
		if len(data) == 32 {
			return nil
		}
		return fmt.Errorf("secrets: file keyring of profile %q is not a recognized format", profileID)
	}
	if env.Format != fileKEKFormat || env.Version != fileKEKVersion || env.KDF != "argon2id" {
		return fmt.Errorf("secrets: file keyring of profile %q has unsupported format %q/v%d/%q", profileID, env.Format, env.Version, env.KDF)
	}
	if _, err := unwrapFileKEK(env, pass); err != nil {
		return fmt.Errorf("secrets: this passphrase does not unlock the existing file keyring of profile %q — enter the passphrase it was created with", profileID)
	}
	return nil
}
