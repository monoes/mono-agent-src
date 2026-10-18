package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/secrets"
)

// The TypeSafe key belongs to the machine, not to a profile: it is stored once,
// encrypted, in the vault under the reserved scope VaultScope. (The settings
// table is plaintext, so it is not used for the key.) jev_usage and
// jev_suggestions stay per-profile. A key that an existing install keeps in a
// profile's vault still works as a read-only fallback; `jev key adopt` copies
// it to the machine vault. Nothing in this file writes on a read path.

// VaultScope is the reserved vault profile id that holds machine-wide secrets.
// No profile may use it (profiledir.ValidProfileID, `profile create` and
// --profile reject it); it never appears in a profile's own vault.
const VaultScope = profiledir.MachineProfileID

// SecretName is the vault entry the resolver looks up.
const SecretName = "typesafe"

// Key sources reported by ResolveKey and KeySource.
const (
	SourceConfig = "config"
	// SourceVault is the machine vault.
	SourceVault = "vault"
	// SourceProfileVault is the caller's profile vault: the read-only
	// fallback for a key not yet copied to the machine vault.
	SourceProfileVault = "profile-vault"
	SourceEnv          = "env"
)

// ErrKeyUnreadable means a key entry exists but could not be read (a locked
// keyring, a failed decryption). It is not jev.ErrNoAPIKey: storing a new key
// is the wrong hint for it.
var ErrKeyUnreadable = errors.New("jev: the TypeSafe key is stored but cannot be read")

// ResolveKey implements plan D3 for a caller working as profileID ("" = no
// profile, machine only):
//
//   - an explicit value that is not an @secret: ref is used as is;
//   - an explicit @secret:<name> ref → the profile's vault, then the machine vault;
//   - no explicit value → the machine vault entry "typesafe", then the
//     profile's own "typesafe" entry (fallback), then TYPESAFE_API_KEY.
//
// A profile's own entry wins an explicit ref because the ref is the node's own
// configuration, written against that profile's vault; the machine vault is
// what a ref falls back to. The environment variable is the last resort in every case. With nothing
// found the error wraps jev.ErrNoAPIKey; if an entry exists but cannot be
// read it wraps ErrKeyUnreadable instead. A literal "@secret:…" is never
// returned.
func ResolveKey(ctx context.Context, db *sql.DB, profileID, explicit string) (key, source string, err error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" && !strings.HasPrefix(explicit, "@secret:") {
		return explicit, SourceConfig, nil
	}
	type candidate struct{ scope, name, source string }
	var order []candidate
	if db != nil {
		if explicit != "" {
			// The node's own reference, read as Resolve would: not in a run
			// the user hasn't approved.
			if err := secrets.RefusedReference(ctx, explicit); err != nil {
				return "", "", err
			}
			name := strings.TrimPrefix(explicit, "@secret:")
			if usableProfile(profileID) {
				order = append(order, candidate{profileID, name, SourceProfileVault})
			}
			order = append(order, candidate{VaultScope, name, SourceVault})
		} else {
			if name, ok := vaultKeyName(ctx, db, VaultScope); ok {
				order = append(order, candidate{VaultScope, name, SourceVault})
			}
			if usableProfile(profileID) {
				if name, ok := vaultKeyName(ctx, db, profileID); ok {
					order = append(order, candidate{profileID, name, SourceProfileVault})
				}
			}
		}
	}
	var unreadable error
	for _, c := range order {
		v, found, rerr := entryValue(ctx, db, c.scope, c.name)
		switch {
		case rerr != nil:
			if unreadable == nil {
				unreadable = rerr
			}
		case found && v != "" && !strings.HasPrefix(v, "@secret:"):
			return v, c.source, nil
		}
	}
	if v := strings.TrimSpace(envKey()); v != "" {
		return v, SourceEnv, nil
	}
	if unreadable != nil {
		return "", "", fmt.Errorf("%w: %v", ErrKeyUnreadable, unreadable)
	}
	return "", "", jev.ErrNoAPIKey
}

// KeySource reports where ResolveKey would look for the key, in the same
// order, without decrypting anything (health checks never trigger a keyring or
// passphrase prompt): the machine vault, else the profile's vault
// (SourceProfileVault), else env. It lists the vault of at most two scopes.
func KeySource(ctx context.Context, db *sql.DB, profileID string) (string, error) {
	if db != nil {
		if _, ok := vaultKeyName(ctx, db, VaultScope); ok {
			return SourceVault, nil
		}
		if usableProfile(profileID) {
			if _, ok := vaultKeyName(ctx, db, profileID); ok {
				return SourceProfileVault, nil
			}
		}
	}
	if strings.TrimSpace(envKey()) != "" {
		return SourceEnv, nil
	}
	return "", jev.ErrNoAPIKey
}

// VaultKeyName is the machine vault entry the key resolves from, if any.
func VaultKeyName(ctx context.Context, db *sql.DB) (string, bool) {
	return vaultKeyName(ctx, db, VaultScope)
}

func usableProfile(profileID string) bool { return profileID != "" && profileID != VaultScope }

// entryValue decrypts the named entry of scope: found is false when there is
// no such entry, err non-nil when it exists but cannot be read.
func entryValue(ctx context.Context, db *sql.DB, scope, name string) (value string, found bool, err error) {
	entries, err := secrets.List(ctx, db, scope)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		fields, _, err := secrets.DecryptFields(ctx, db, scope, e.ID)
		if err != nil {
			return "", true, err
		}
		if v, ok := fields["secret"]; ok {
			return strings.TrimSpace(v), true, nil
		}
		if len(fields) == 1 {
			for _, v := range fields {
				return strings.TrimSpace(v), true, nil
			}
		}
		return "", true, fmt.Errorf("entry %q holds several fields and none is keyed \"secret\"", name)
	}
	return "", false, nil
}

// keyNameAliases are vault entry names, normalised (lower case, no spaces,
// dashes or underscores), accepted for the key when no entry is named
// exactly "typesafe" — people name the secret after the product or the model.
var keyNameAliases = map[string]bool{
	"typesafe": true, "typesafeapikey": true, "typesafekey": true, "typesafeapi": true,
	"jev": true, "jevapikey": true, "jevkey": true, "jevapi": true,
}

func normaliseKeyName(n string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(n)))
}

// vaultKeyName returns the entry of scope holding the TypeSafe key: exactly
// "typesafe" if present, else the single entry whose normalised name is a
// known alias. Two or more alias matches are ambiguous and ignored.
func vaultKeyName(ctx context.Context, db *sql.DB, scope string) (string, bool) {
	e, ok := vaultKeyEntry(ctx, db, scope)
	return e.Name, ok
}

func vaultKeyEntry(ctx context.Context, db *sql.DB, scope string) (secrets.Entry, bool) {
	entries, err := secrets.List(ctx, db, scope)
	if err != nil {
		return secrets.Entry{}, false
	}
	var alias []secrets.Entry
	for _, e := range entries {
		if e.Name == SecretName {
			return e, true
		}
		if keyNameAliases[normaliseKeyName(e.Name)] {
			alias = append(alias, e)
		}
	}
	if len(alias) == 1 {
		return alias[0], true
	}
	return secrets.Entry{}, false
}

// ErrMachineKeyExists is returned by Adopt when the machine vault already has a key.
var ErrMachineKeyExists = errors.New("jev: the machine vault already holds a TypeSafe key")

// Adopted is the result of Adopt.
type Adopted struct {
	FromProfile string `json:"from_profile"`
	Entry       string `json:"entry"`
	Candidates  int    `json:"candidates"`
}

// Adopt copies a TypeSafe key from a profile's vault to the machine vault with
// one write; the profile's entry is left in place. from names the profile; ""
// picks the most recently updated readable key among all profiles. It fails
// with ErrMachineKeyExists rather than replacing a machine key (`jev key set`
// does that), and with jev.ErrNoAPIKey when no profile holds a key.
func Adopt(ctx context.Context, db *sql.DB, from string) (Adopted, error) {
	if _, ok := vaultKeyName(ctx, db, VaultScope); ok {
		return Adopted{}, ErrMachineKeyExists
	}
	var cands []secrets.Entry
	if from != "" {
		if !usableProfile(from) {
			return Adopted{}, fmt.Errorf("jev: %q is not a profile", from)
		}
		if e, ok := vaultKeyEntry(ctx, db, from); ok {
			cands = append(cands, e)
		}
	} else {
		cands = profileKeyCandidates(ctx, db)
	}
	if len(cands) == 0 {
		return Adopted{}, jev.ErrNoAPIKey
	}
	var lastErr error
	for _, c := range cands {
		key, _, err := entryValue(ctx, db, c.ProfileID, c.Name)
		if err != nil {
			lastErr = fmt.Errorf("profile %q key entry %q cannot be read: %v", c.ProfileID, c.Name, err)
			continue
		}
		if key == "" {
			lastErr = fmt.Errorf("profile %q key entry %q is empty", c.ProfileID, c.Name)
			continue
		}
		if _, err := secrets.Add(ctx, db, VaultScope, "secret", SecretName, map[string]string{"secret": key}, "", "", "TypeSafe Jev API key"); err != nil {
			// A concurrent `jev key set` or GUI save won the unique (profile, name) key.
			if _, ok := vaultKeyName(ctx, db, VaultScope); ok {
				return Adopted{}, ErrMachineKeyExists
			}
			return Adopted{}, fmt.Errorf("storing the TypeSafe key in the machine vault: %w", err)
		}
		return Adopted{FromProfile: c.ProfileID, Entry: SecretName, Candidates: len(cands)}, nil
	}
	return Adopted{}, fmt.Errorf("%w: %v", ErrKeyUnreadable, lastErr)
}

// profileKeyCandidates lists the key entries held by profile vaults (never the
// machine scope) that have stored fields, most recently updated first; ties
// break on profile id. Only the explicit `jev key adopt` calls it.
func profileKeyCandidates(ctx context.Context, db *sql.DB) []secrets.Entry {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT profile_id FROM vault_secrets WHERE profile_id <> ?`, VaultScope)
	if err != nil {
		return nil
	}
	var scopes []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			scopes = append(scopes, p)
		}
	}
	rows.Close()
	var out []secrets.Entry
	for _, p := range scopes {
		if e, ok := vaultKeyEntry(ctx, db, p); ok && e.FieldCount > 0 {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ProfileID < out[j].ProfileID
	})
	return out
}
