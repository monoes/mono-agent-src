package extensioncli

import (
	"context"
	"errors"
	"fmt"
	"slices"

	capturecli "github.com/monoes/mono-agent/internal/cli/capture"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"
)

// The vault's half of the extension bridge (#451): a vault.save from the side
// panel's confirm card becomes an item in the media/file vault of the profile
// it names. newExtensionServer installs it on every bridge this binary builds.
// The extension package has already checked the request (size, type, name,
// client id); this checks that the profile is live and writes, once per client
// id (vault.SaveBytes). It never reads a stored file back.

// vaultSaveSource is the source every extension save is recorded with.
const vaultSaveSource = "extension"

// vaultSaveQuota caps what one profile's vault holds from vault.save (a var
// so tests can lower it): 2 GiB and 5,000 items, counted inside the save's
// own transaction (vault.SaveBytes).
var vaultSaveQuota = struct {
	Bytes int64
	Items int
}{Bytes: 2 << 30, Items: 5000}

// vaultSink saves vault.save requests into the vault of the database at path,
// opening it per request as boardSink does.
type vaultSink struct{ path string }

// extensionVaultSink is the sink newExtensionServer installs.
func extensionVaultSink(path string) extension.VaultSink { return vaultSink{path: path} }

// LiveProfiles implements extension.VaultSink: the ids of the profiles in the
// database now. The bridge asks at most once a second.
func (v vaultSink) LiveProfiles(ctx context.Context) ([]string, error) {
	db, err := capturecli.OpenProfileDB(v.path)
	if err != nil {
		return nil, extension.Unavailable("MonoAgent has no database yet: start MonoAgent or run any monoagentcli command")
	}
	defer db.Close()
	profiles, err := profiledir.List(ctx, db.DB)
	if err != nil {
		return nil, extension.Unavailable("MonoAgent could not read its profiles; try again")
	}
	ids := make([]string, len(profiles))
	for i, p := range profiles {
		ids[i] = p.ID
	}
	return ids, nil
}

// SaveToVault implements extension.VaultSink.
func (v vaultSink) SaveToVault(ctx context.Context, f extension.VaultFile) (extension.VaultSaved, error) {
	db, err := capturecli.OpenProfileDB(v.path)
	if err != nil {
		// A fixed message, as boardSink's: the reason may be shown in a page.
		return extension.VaultSaved{}, extension.Unavailable("MonoAgent has no database yet: start MonoAgent or run any monoagentcli command")
	}
	defer db.Close()
	// `extension serve` never migrates: a database from before vault.save has
	// no client_id column until some command has run. The bridge does not
	// migrate it either.
	var cols int
	err = db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('vault_images') WHERE name = 'client_id'`).Scan(&cols)
	if err != nil {
		return extension.VaultSaved{}, extension.Unavailable("MonoAgent could not read its database; try again")
	}
	if cols == 0 {
		return extension.VaultSaved{}, extension.Unavailable("the monoagent database is too old for saves from the browser: run any monoagentcli command once to upgrade it")
	}
	// Only a live profile: one deleted since the panel listed it must not get
	// its vault folder back (the rule captures follow).
	profiles, err := profiledir.List(ctx, db.DB)
	if err != nil {
		return extension.VaultSaved{}, extension.Unavailable("MonoAgent could not read its profiles; try again")
	}
	if !slices.ContainsFunc(profiles, func(p profiledir.Profile) bool { return p.ID == f.ProfileID }) {
		return extension.VaultSaved{}, &extension.RequestError{Code: extension.CodeInvalidInput,
			Err: fmt.Errorf("profile %.64q does not exist (any more): choose another in the side panel", f.ProfileID)}
	}
	item, err := vault.SaveBytes(ctx, db.DB, f.ProfileID, vault.SaveInput{
		Data: f.Data, Ext: f.Ext, Label: f.Name, Source: vaultSaveSource, ClientID: f.ClientID, SourceURL: f.SourceURL,
		MaxItems: vaultSaveQuota.Items, MaxBytes: vaultSaveQuota.Bytes,
	})
	if err != nil {
		return extension.VaultSaved{}, vaultSaveErr(err)
	}
	// The reply is the item as stored: a retry of a client id gets the item
	// the first save made, whatever name or bytes this request carried, and
	// its file name when its label has since been cleared.
	name := item.Label
	if name == "" {
		name = item.Filename
	}
	return extension.VaultSaved{ID: item.ID, Created: item.Created, Name: name, MIME: extension.VaultSaveMIME(item.Filename),
		SizeBytes: int(item.SizeBytes)}, nil
}

// vaultSaveErr gives a failed save the code the extension acts on, under a
// fixed message (a database error can name files): quota_exceeded and
// storage_conflict are final until the person acts, internal may be tried
// again.
func vaultSaveErr(err error) error {
	var quota *vault.QuotaError
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &quota) && quota.Limit == "items":
		return &extension.RequestError{Code: extension.CodeQuotaExceeded, Data: map[string]any{"limit": "items", "max": quota.Max},
			Err: fmt.Errorf("this profile already holds %d files saved from the browser, the most it takes: delete some in the Media Vault to save more", quota.Max)}
	case errors.As(err, &quota):
		return &extension.RequestError{Code: extension.CodeQuotaExceeded, Data: map[string]any{"limit": "bytes", "max": quota.Max},
			Err: fmt.Errorf("files saved from the browser would pass this profile's %s limit: delete some in the Media Vault to save more", sizeText(quota.Max))}
	case errors.Is(err, vault.ErrStorageConflict):
		return &extension.RequestError{Code: extension.CodeStorageConflict,
			Err: errors.New("MonoAgent could not save the file: files it did not make sit where its next vault items go; check the profile's vault folder (.monoagent/vault in the profile's folder)")}
	}
	return &extension.RequestError{Code: extension.CodeInternal, Err: errors.New("MonoAgent could not save the file; try again")}
}

// sizeText is n bytes in GiB or MiB, or bytes when smaller.
func sizeText(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
