package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/secrets"
)

// resolveSecret reads one vault secret; a var so tests can use a fake vault.
var resolveSecret = func(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	return secrets.Resolve(ctx, db, profileID, "@secret:"+name)
}

// secretNamespace is the vault name prefix of a package's own secrets:
// {{secret:api_key}} in automation acme-crm reads "automation:acme-crm/api_key".
const secretNamespace = "automation:"

// vaultSecret backs {{secret:<name>}} with the profile vault (contract §8).
// It tries "automation:<id>/<name>" first. Only automations whose trust is
// positively builtin or local fall back to the bare "<name>"; recorded,
// imported and unknown ones (fail closed) read nothing but the secrets filed
// under their own id, so a third-party package can't pull, say, a GitHub
// token into a page it controls. Values are never logged. A lookup the run
// may not do (secrets.RefuseResolving: an unapproved workflow, an AI's node
// run) reads nothing, and refused, when set, gets why: the secret named,
// never a value.
func vaultSecret(ctx context.Context, db *sql.DB, profileID, automationID, name string, refused func(error)) (string, bool) {
	automationID = strings.ToLower(strings.TrimSpace(automationID))
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	candidates := []string{secretNamespace + automationID + "/" + name}
	if automationID != "" && trustAllowsBareSecrets(automationTrust(automationID)) {
		candidates = append(candidates, name)
	}
	for _, c := range candidates {
		v, err := resolveSecret(ctx, db, profileID, c)
		if err == nil {
			return v, true
		}
		if errors.Is(err, secrets.ErrResolveRefused) {
			if refused != nil {
				refused(fmt.Errorf("{{secret:%s}}: %w", name, err))
			}
			return "", false
		}
	}
	return "", false
}

// warnRefused puts why a lookup was refused on stderr, for a caller whose
// step has no error of its own to carry it.
func warnRefused(err error) { fmt.Fprintln(os.Stderr, "Warning:", err) }

// secretRefusals keeps the first lookup of a run that was refused, for the
// step's error.
type secretRefusals struct {
	mu  sync.Mutex
	err error
}

func (r *secretRefusals) note(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

func (r *secretRefusals) first() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func trustAllowsBareSecrets(trust string) bool {
	return trust == automation.SourceBuiltin || trust == automation.SourceLocal
}

// automationTrust is the installed trust tier of an automation: the package
// context's Trust() when it has one, else the registry index; "" when
// unknown.
func automationTrust(id string) string {
	if src := action.CurrentDefSource(); src != nil {
		if t, ok := src.Package(id).(interface{ Trust() string }); ok {
			return t.Trust()
		}
	}
	if info, ok := bootedInfo(id); ok {
		return info.Trust
	}
	return ""
}

// secretLookup binds vaultSecret to one automation for the executor.
func secretLookup(ctx context.Context, db *sql.DB, profileID, automationID string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		return vaultSecret(ctx, db, profileID, automationID, name, warnRefused)
	}
}

// SecretLookup is the vault lookup for {{secret:<name>}} bound to one
// automation, for callers that run a single package (e.g. `record verify`).
// Trust comes from the booted registry (the CLI root boots it); an
// automation it doesn't know, such as a new draft, gets its namespaced
// secrets only.
func SecretLookup(ctx context.Context, db *sql.DB, profileID, automationID string) func(string) (string, bool) {
	return secretLookup(ctx, db, profileID, automationID)
}

// ScopedSecretLookup is the executor's SetSecretLookup function: the
// executor passes the automation the running step belongs to (re-scoped
// inside call_action). fallbackID stands in when it passes none (a legacy
// action without a package). refused gets why a lookup was refused (see
// vaultSecret).
func ScopedSecretLookup(ctx context.Context, db *sql.DB, profileID, fallbackID string, refused func(error)) func(automationID, name string) (string, bool) {
	return func(automationID, name string) (string, bool) {
		if strings.TrimSpace(automationID) == "" {
			automationID = fallbackID
		}
		return vaultSecret(ctx, db, profileID, automationID, name, refused)
	}
}

// bootedInfo returns the registry's installed info for an automation.
func bootedInfo(id string) (*automation.InstalledInfo, bool) {
	bootMu.Lock()
	reg := bootedReg
	bootMu.Unlock()
	if reg == nil {
		return nil, false
	}
	info, err := reg.Info(id)
	if err != nil || info == nil {
		return nil, false
	}
	return info, true
}
