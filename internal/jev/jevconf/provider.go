package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/secrets"
)

// Decision providers (docs/mastermind/plans/2026-11-21-decision-providers.md
// §4.2). TypeSafe is built in and the default (D4); the operator may add
// others, each bound to its own host and its own machine-vault key (D5). A
// surface's provider is chosen per call, else per profile and surface, else
// the machine default, else TypeSafe (D9).
//
// Storage is the global settings table: "jev.providers" (the configured
// providers, JSON), "jev.provider" (the machine default) and
// "jev.<profile>.<surface>.provider" (a profile's choice for a surface).

// Provider is one decision provider.
type Provider struct {
	// Name identifies it: [a-z0-9-]{1,32}; "typesafe" is the built-in one.
	Name string `json:"name"`
	// Kind names its adapter (RegisterKind): openai, anthropic, hf-zeroshot, ….
	Kind string `json:"kind"`
	// BaseURL is the only host its key goes to: https, or http to this machine.
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// SecretName is the machine-vault entry holding its key; "" = keyless
	// (a local server).
	SecretName string `json:"secret_name"`
	// Caps is what a probe found (or the adapter declares).
	Caps jev.Capabilities `json:"caps"`
	// USDPerMTokIn and USDPerMTokOut, when set, price its calls for usage.
	USDPerMTokIn  float64 `json:"usd_per_mtok_in,omitempty"`
	USDPerMTokOut float64 `json:"usd_per_mtok_out,omitempty"`
}

// Builtin reports whether p is the built-in TypeSafe provider.
func (p Provider) Builtin() bool { return p.Name == jev.ProviderTypeSafe }

const (
	providersKey       = "jev.providers"
	defaultProviderKey = "jev.provider"
	// SourceKeyless is the key source of a provider that needs no key.
	SourceKeyless = "keyless"
)

var (
	// ErrUnknownProvider is wrapped when a name is neither "typesafe" nor a configured provider.
	ErrUnknownProvider = errors.New("jev: unknown decision provider")
	// ErrOperatorOnly is returned when someone other than the operator's own
	// terminal tries to add, remove or choose a provider.
	ErrOperatorOnly = errors.New("jev: only your own terminal may add, remove or choose a decision provider " +
		"(an agent, the chat, a workflow run or an unapproved step may not)")
	// ErrExplicitKeyWithProvider is returned when a step's own api_key would go to a provider other than TypeSafe (D5).
	ErrExplicitKeyWithProvider = errors.New("jev: a step's api_key goes only to TypeSafe: remove it, or use the typesafe provider " +
		"(another provider's key is its machine-vault entry)")
	// ErrProviderURL is wrapped by the refusal of a provider base_url that cannot carry a key.
	ErrProviderURL = errors.New("jev: provider base_url refused: it must be an https URL; http is accepted only to this machine " +
		"(localhost, 127.0.0.1, [::1]), and a URL with credentials, a query or a fragment is refused")
)

var providerName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// TypeSafeProvider is the built-in provider's record, for display.
func TypeSafeProvider() Provider {
	return Provider{
		Name: jev.ProviderTypeSafe, Kind: jev.ProviderTypeSafe, BaseURL: strings.TrimRight(jev.DefaultBaseURL, "/"),
		Model: jev.DefaultModel, SecretName: SecretName,
		Caps: jev.Capabilities{Kinds: []string{jev.TypeChoice, jev.TypeNoul, jev.TypeScore}, Probability: jev.ProbNative, Models: true},
	}
}

// Providers lists the built-in TypeSafe provider first, then the configured
// ones by name. A nil db lists TypeSafe alone.
func Providers(db *sql.DB) ([]Provider, error) {
	saved, err := savedProviders(db)
	if err != nil {
		return nil, err
	}
	return append([]Provider{TypeSafeProvider()}, saved...), nil
}

// FindProvider looks a provider up by name ("typesafe" is always found).
func FindProvider(db *sql.DB, name string) (Provider, bool, error) {
	if name == jev.ProviderTypeSafe {
		return TypeSafeProvider(), true, nil
	}
	saved, err := savedProviders(db)
	if err != nil {
		return Provider{}, false, err
	}
	for _, p := range saved {
		if p.Name == name {
			return p, true, nil
		}
	}
	return Provider{}, false, nil
}

// SaveProvider adds p, or replaces the provider of the same name. Only the
// operator's own terminal may (MayNameHost): a provider decides where data
// and a key go. The record is checked (checkProvider) and its kind must be
// registered.
func SaveProvider(ctx context.Context, db *sql.DB, p Provider) error {
	if !MayNameHost(ctx) {
		return ErrOperatorOnly
	}
	p = normaliseProvider(p)
	if err := checkProvider(p); err != nil {
		return err
	}
	if _, ok := builderFor(p.Kind); !ok {
		return fmt.Errorf("jev: provider kind %q is not in this build (known: %s)", p.Kind, strings.Join(Kinds(), ", "))
	}
	return updateProviders(ctx, db, func(tx *sql.Tx, saved []Provider) ([]Provider, error) {
		out := make([]Provider, 0, len(saved)+1)
		for _, q := range saved {
			if q.Name == p.Name {
				continue
			}
			if p.SecretName != "" && q.SecretName == p.SecretName && origin(q.BaseURL) != origin(p.BaseURL) {
				return nil, fmt.Errorf("jev: the vault entry %q is the key of provider %q at %s: a key goes only to its own host",
					p.SecretName, q.Name, origin(q.BaseURL))
			}
			out = append(out, q)
		}
		return append(out, p), nil
	})
}

// RemoveProvider removes a configured provider. It refuses "typesafe" and a
// provider still chosen as the default or for any surface: removing it would
// silently send that data to another host.
func RemoveProvider(ctx context.Context, db *sql.DB, name string) error {
	if !MayNameHost(ctx) {
		return ErrOperatorOnly
	}
	if name == jev.ProviderTypeSafe {
		return errors.New("jev: the typesafe provider is built in and cannot be removed")
	}
	return updateProviders(ctx, db, func(tx *sql.Tx, saved []Provider) ([]Provider, error) {
		out := make([]Provider, 0, len(saved))
		for _, q := range saved {
			if q.Name != name {
				out = append(out, q)
			}
		}
		if len(out) == len(saved) {
			return nil, fmt.Errorf("%w %q", ErrUnknownProvider, name)
		}
		rows, err := tx.QueryContext(ctx, `SELECT key FROM settings WHERE value = ? AND (key = ? OR key LIKE 'jev.%.provider') ORDER BY key`,
			name, defaultProviderKey)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var uses []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, err
			}
			uses = append(uses, k)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(uses) > 0 {
			return nil, fmt.Errorf("jev: provider %q is still in use (%s): choose another provider there first", name, strings.Join(uses, ", "))
		}
		return out, nil
	})
}

// DefaultProvider is the machine default: "jev.provider", else "typesafe".
// For display: a settings read that fails shows as "typesafe" here, while
// building a client fails on it.
func DefaultProvider(db *sql.DB) string {
	name, _ := defaultProvider(db)
	return name
}

// ActiveProvider is the provider surface s uses for profileID when the call
// names none (D9 layers 2–4): the profile's choice for s, else the machine
// default, else "typesafe". It is for display: building a client resolves
// the same way but fails on a settings read that fails and on a provider
// that does not exist, rather than fall back to another host.
func ActiveProvider(db *sql.DB, profileID string, s Surface) string {
	name, _ := activeProvider(db, profileID, s)
	return name
}

func defaultProvider(db *sql.DB) (string, error) {
	v, err := providerSetting(db, defaultProviderKey)
	if err != nil || v == "" {
		return jev.ProviderTypeSafe, err
	}
	return v, nil
}

func activeProvider(db *sql.DB, profileID string, s Surface) (string, error) {
	if key, err := settingKey(profileID, s, "provider"); err == nil {
		v, err := providerSetting(db, key)
		if err != nil {
			return jev.ProviderTypeSafe, err
		}
		if v != "" {
			return v, nil
		}
	}
	return defaultProvider(db)
}

// providerSetting reads a provider choice: "" when none is stored, an error
// when the settings cannot be read (never taken for "none").
func providerSetting(db *sql.DB, key string) (string, error) {
	if db == nil {
		return "", nil
	}
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("jev: cannot read which decision provider to use (%s): %w", key, err)
	}
	return strings.TrimSpace(v), nil
}

// SetDefaultProvider makes name the machine default ("" or "typesafe"
// restores TypeSafe). Operator only.
func SetDefaultProvider(ctx context.Context, db *sql.DB, name string) error {
	if !MayNameHost(ctx) {
		return ErrOperatorOnly
	}
	if name == "" || name == jev.ProviderTypeSafe {
		_, err := db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, defaultProviderKey)
		return err
	}
	if err := mustExist(db, name); err != nil {
		return err
	}
	return setSetting(db, defaultProviderKey, name)
}

// SetSurfaceProvider makes name the provider of surface s for profileID (""
// clears the choice, so the machine default applies). Operator only: it
// decides where that surface's data goes.
func SetSurfaceProvider(ctx context.Context, db *sql.DB, profileID string, s Surface, name string) error {
	if !MayNameHost(ctx) {
		return ErrOperatorOnly
	}
	if !Known(s) && s != Decider && s != DynamicOrg && s != CLI && !strings.HasPrefix(string(s), "node:") {
		return fmt.Errorf("jev: unknown surface %q", s)
	}
	key, err := settingKey(profileID, s, "provider")
	if err != nil {
		return fmt.Errorf("jev: %w", err)
	}
	if name == "" {
		_, err := db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	if err := mustExist(db, name); err != nil {
		return err
	}
	return setSetting(db, key, name)
}

// NewClientFor is NewClient with the provider named by the call (a node's or
// the org decider's config); "" resolves it as NewClient does (D9).
func NewClientFor(ctx context.Context, db *sql.DB, profileID, provider, explicitKey, model string, s Surface) (*jev.Client, error) {
	if profileID == "" {
		return nil, fmt.Errorf("jev: %w", profiledir.ErrNoProfile)
	}
	if provider == "" {
		var err error
		if provider, err = activeProvider(db, profileID, s); err != nil {
			return nil, err
		}
	}
	c, _, err := ResolveProviderClient(ctx, db, profileID, provider, explicitKey, model)
	if err != nil {
		return nil, err
	}
	if db != nil {
		p, _, _ := FindProvider(db, c.Provider)
		c.OnResult = recorder(db, profileID, s, c.Provider, p.USDPerMTokIn, p.USDPerMTokOut)
	}
	return c, nil
}

// ResolveProviderClient builds the client of provider ("" = the machine
// default). "typesafe" is ResolveClient's TypeSafe path, unchanged. Any other
// provider is re-checked from its stored record, refuses a step's own key
// (D5), reads its key from its machine-vault entry only (gated as the
// TypeSafe key is) and is built by its kind's Builder. The source is the
// key's (SourceVault, or SourceKeyless).
func ResolveProviderClient(ctx context.Context, db *sql.DB, profileID, provider, explicit, model string) (*jev.Client, string, error) {
	if provider == "" {
		var err error
		if provider, err = defaultProvider(db); err != nil {
			return nil, "", err
		}
	}
	if provider == jev.ProviderTypeSafe {
		return resolveTypeSafeClient(ctx, db, profileID, explicit, model)
	}
	p, ok, err := FindProvider(db, provider)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("%w %q (`jev provider list` shows the configured ones)", ErrUnknownProvider, provider)
	}
	if strings.TrimSpace(explicit) != "" {
		return nil, "", ErrExplicitKeyWithProvider
	}
	if err := checkProvider(p); err != nil {
		return nil, "", err
	}
	build, ok := builderFor(p.Kind)
	if !ok {
		return nil, "", fmt.Errorf("jev: provider %q is of kind %q, which this build does not include", p.Name, p.Kind)
	}
	key, source, err := providerKey(ctx, db, p)
	if err != nil {
		return nil, source, err
	}
	if model == "" {
		model = p.Model
	}
	b, err := build(p, key)
	if err != nil {
		return nil, source, fmt.Errorf("jev: provider %q: %w", p.Name, err)
	}
	return jev.NewBackendClient(p.Name, model, b), source, nil
}

// providerKey reads p's key from the machine vault entry it names, never
// from a profile's vault or the environment. The read is a secret read like
// @secret:<name>: refused in an unapproved step, and under agent access
// checked against the grant and audited (readEntry).
func providerKey(ctx context.Context, db *sql.DB, p Provider) (key, source string, err error) {
	if p.SecretName == "" {
		return "", SourceKeyless, nil
	}
	if db == nil {
		return "", "", noProviderKey{p.Name, p.SecretName}
	}
	if err := secrets.RefusedReference(ctx, secretRef(p.SecretName)); err != nil {
		return "", "", err
	}
	v, found, err := readEntry(ctx, db, VaultScope, p.SecretName)
	if errors.Is(err, secrets.ErrResolveRefused) {
		return "", "", err
	}
	if err != nil {
		return "", "", fmt.Errorf("jev: provider %q: the machine vault entry %q cannot be read: %v", p.Name, p.SecretName, err)
	}
	if !found || v == "" || strings.HasPrefix(v, "@secret:") {
		return "", "", noProviderKey{p.Name, p.SecretName}
	}
	return v, SourceVault, nil
}

// ProviderKeySource is KeySource for the provider surface s uses for
// profileID when the call names none (ActiveProvider's rule), so a check
// agrees with the client NewClient(…, s) builds. See ProviderKeySourceOf.
func ProviderKeySource(ctx context.Context, db *sql.DB, profileID string, s Surface) (provider, source string, err error) {
	provider, err = activeProvider(db, profileID, s)
	if err != nil {
		return provider, "", err
	}
	source, err = ProviderKeySourceOf(ctx, db, profileID, provider)
	return provider, source, err
}

// ProviderKeySourceOf reports where provider's key would come from ("" = the
// machine default), without decrypting anything: TypeSafe is KeySource;
// another provider is SourceKeyless when it needs no key, else SourceVault
// when its machine-vault entry exists (its name is listed, never read). A
// missing key wraps jev.ErrNoAPIKey; an unknown provider, a refused record
// and a kind not in this build fail as ResolveProviderClient does.
func ProviderKeySourceOf(ctx context.Context, db *sql.DB, profileID, provider string) (string, error) {
	if provider == "" {
		var err error
		if provider, err = defaultProvider(db); err != nil {
			return "", err
		}
	}
	if provider == jev.ProviderTypeSafe {
		return KeySource(ctx, db, profileID)
	}
	p, ok, err := FindProvider(db, provider)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w %q (`jev provider list` shows the configured ones)", ErrUnknownProvider, provider)
	}
	if err := checkProvider(p); err != nil {
		return "", err
	}
	if _, ok := builderFor(p.Kind); !ok {
		return "", fmt.Errorf("jev: provider %q is of kind %q, which this build does not include", p.Name, p.Kind)
	}
	if p.SecretName == "" {
		return SourceKeyless, nil
	}
	if db != nil {
		if entries, err := secrets.List(ctx, db, VaultScope); err == nil {
			for _, e := range entries {
				if e.Name == p.SecretName && e.FieldCount > 0 {
					return SourceVault, nil
				}
			}
		}
	}
	return "", noProviderKey{p.Name, p.SecretName}
}

// noProviderKey is a missing provider key; errors.Is(err, jev.ErrNoAPIKey)
// holds, so surfaces that treat a missing key as "off" do so for every provider.
type noProviderKey struct{ provider, secret string }

func (e noProviderKey) Error() string {
	return fmt.Sprintf("jev: provider %q has no key: store it in the machine vault entry %q", e.provider, e.secret)
}

func (e noProviderKey) Is(target error) bool { return target == jev.ErrNoAPIKey }

func normaliseProvider(p Provider) Provider {
	p.Name, p.Kind = strings.TrimSpace(p.Name), strings.TrimSpace(p.Kind)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	p.Model, p.SecretName = strings.TrimSpace(p.Model), strings.TrimSpace(p.SecretName)
	return p
}

// checkProvider is what a configured provider's record must be, checked when
// it is saved and again whenever its client is built (the settings table is
// plaintext).
func checkProvider(p Provider) error {
	if !providerName.MatchString(p.Name) {
		return fmt.Errorf("jev: provider name %q: use 1 to 32 of a-z, 0-9 and -", p.Name)
	}
	if p.Name == jev.ProviderTypeSafe || p.Kind == jev.ProviderTypeSafe {
		return errors.New("jev: typesafe is the built-in provider and cannot be redefined")
	}
	if !providerName.MatchString(p.Kind) {
		return fmt.Errorf("jev: provider %q: invalid kind %q", p.Name, p.Kind)
	}
	if checkOverride(p.BaseURL) != nil {
		return fmt.Errorf("%w: %q", ErrProviderURL, redact(p.BaseURL))
	}
	if s := p.SecretName; s != "" && (strings.HasPrefix(s, "@secret:") || s == SecretName || keyNameAliases[normaliseKeyName(s)]) {
		return fmt.Errorf("jev: provider %q: secret_name %q names the TypeSafe key, which goes only to TypeSafe: store this provider's key under its own name",
			p.Name, s)
	}
	switch p.Caps.Probability {
	case "", jev.ProbNative, jev.ProbLogprob, jev.ProbSampled, jev.ProbNone:
	default:
		return fmt.Errorf("jev: provider %q: unknown probability kind %q", p.Name, p.Caps.Probability)
	}
	return nil
}

// origin is a base URL's scheme and host, lower case.
func origin(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func mustExist(db *sql.DB, name string) error {
	_, ok, err := FindProvider(db, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownProvider, name)
	}
	return nil
}
