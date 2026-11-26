package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/monoes/mono-agent/internal/secrets"
)

// The rules that tie a provider's key to its host, checked when a record is
// saved and again every time its client is built. The records live in the
// plaintext settings table, so the use-time checks re-run what SaveProvider
// checked. Against a process that can write the SQLite file directly they are
// defence in depth, not a guarantee: such a process can also rewrite the
// vault rows themselves.

// ErrKeylessRemote is wrapped by the refusal of a provider without a key on
// another host: only a server on this machine may be keyless.
var ErrKeylessRemote = errors.New("jev: a provider on another host needs a key")

// originField is the field of a provider key's machine-vault entry that
// records the origin (scheme://host[:port]) the key was stored for. It sits
// in the entry's encrypted field map next to "secret", which is what every
// reader of the entry's value picks.
const originField = "origin"

// Loopback reports whether base points at this machine: its host is exactly
// "localhost" or a loopback IP literal (the rule BaseURL applies to http).
func Loopback(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || isLoopbackIP(h)
}

// keylessRemote refuses a provider with no key on another host.
func keylessRemote(p Provider) error {
	if p.SecretName != "" || Loopback(p.BaseURL) {
		return nil
	}
	return fmt.Errorf("%w: provider %q at %s has none; add it with `monoagentcli jev provider add %s --kind %s --base-url %s --key-stdin` "+
		"(only a server on this machine, localhost, 127.0.0.1 or [::1], may be keyless)",
		ErrKeylessRemote, p.Name, origin(p.BaseURL), p.Name, p.Kind, p.BaseURL)
}

// ErrKeyNeeded is wrapped by the refusal of a provider without a key whose
// kind always needs one (KindSpec.NeedsKey).
var ErrKeyNeeded = errors.New("jev: a provider of this kind needs a key")

// keylessKind refuses a provider with no key whose kind needs one.
func keylessKind(p Provider) error {
	if p.SecretName != "" || !specFor(p.Kind).NeedsKey {
		return nil
	}
	return fmt.Errorf("%w: provider %q (%s) has none; add it with `monoagentcli jev provider add %s --kind %s --base-url %s --key-stdin`",
		ErrKeyNeeded, p.Name, p.Kind, p.Name, p.Kind, p.BaseURL)
}

// sharedKeyElsewhere refuses p when another record names p's vault entry at
// another origin, or when the records hold p's name twice: one vault entry,
// one host.
func sharedKeyElsewhere(p Provider, saved []Provider) error {
	same := 0
	for _, q := range saved {
		if q.Name == p.Name {
			if same++; same > 1 {
				return fmt.Errorf("jev: the provider records hold %q more than once: remove it and add it again", p.Name)
			}
			continue
		}
		if p.SecretName != "" && q.SecretName == p.SecretName && origin(q.BaseURL) != origin(p.BaseURL) {
			return fmt.Errorf("jev: the vault entry %q is the key of provider %q at %s: a key goes only to its own host",
				p.SecretName, q.Name, origin(q.BaseURL))
		}
	}
	return nil
}

// PutProviderKey stores key in p's machine-vault entry p.SecretName, bound to
// p's origin (the entry's "origin" field), replacing the entry's fields when
// it exists. With key "" it keeps the stored key and moves its binding to p's
// origin: for the operator who confirmed a provider's new host; an entry that
// records no origin (one the user made, or one from before the binding) is
// left as it is. Operator only.
func PutProviderKey(ctx context.Context, db *sql.DB, p Provider, key, notes string) (replaced bool, err error) {
	if !MayNameHost(ctx) {
		return false, ErrOperatorOnly
	}
	p = WithKindDefaults(p)
	if p.SecretName == "" {
		return false, fmt.Errorf("jev: provider %q names no vault entry for its key", p.Name)
	}
	id, found, err := machineEntry(ctx, db, p.SecretName)
	if err != nil {
		return false, err
	}
	if key == "" {
		if !found {
			return false, nil
		}
		fields, _, err := secrets.DecryptFields(ctx, db, VaultScope, id)
		if err != nil {
			return false, err
		}
		if _, bound := fields[originField]; !bound || fields[originField] == origin(p.BaseURL) {
			return false, nil
		}
		fields[originField] = origin(p.BaseURL)
		return true, secrets.Update(ctx, db, VaultScope, id, nil, nil, nil, nil, fields)
	}
	fields := map[string]string{"secret": key, originField: origin(p.BaseURL)}
	if found {
		if err := secrets.Update(ctx, db, VaultScope, id, nil, nil, nil, nil, fields); err != nil {
			return false, fmt.Errorf("updating vault entry %q: %w", p.SecretName, err)
		}
		return true, nil
	}
	if _, err := secrets.Add(ctx, db, VaultScope, "secret", p.SecretName, fields, "", "", notes); err != nil {
		return false, fmt.Errorf("storing the key in the machine vault: %w", err)
	}
	return false, nil
}

// boundOriginRefusal refuses p's key when its vault entry records another
// origin than p's (the record was re-pointed after the key was stored). An
// entry that records none is not checked.
func boundOriginRefusal(ctx context.Context, db *sql.DB, p Provider) error {
	id, found, err := machineEntry(ctx, db, p.SecretName)
	if err != nil || !found {
		return err
	}
	fields, _, err := secrets.DecryptFields(ctx, db, VaultScope, id)
	if err != nil {
		return fmt.Errorf("jev: provider %q: the machine vault entry %q cannot be read: %v", p.Name, p.SecretName, err)
	}
	if bound, ok := fields[originField]; ok && bound != origin(p.BaseURL) {
		return fmt.Errorf("jev: provider %q: its key (vault entry %q) was stored for %s, not %s: a key goes only to the host it was added for; "+
			"to use this host, add the key again (`monoagentcli jev provider add %s --kind %s --base-url %s --key-stdin`)",
			p.Name, p.SecretName, bound, origin(p.BaseURL), p.Name, p.Kind, p.BaseURL)
	}
	return nil
}

// machineEntry finds the machine-vault entry name by listing names.
func machineEntry(ctx context.Context, db *sql.DB, name string) (id string, found bool, err error) {
	entries, err := secrets.List(ctx, db, VaultScope)
	if err != nil {
		return "", false, err
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, true, nil
		}
	}
	return "", false, nil
}
