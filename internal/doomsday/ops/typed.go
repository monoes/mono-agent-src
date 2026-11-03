package ops

import (
	"encoding/json"
	"fmt"
	"slices"
)

// The typed operations of note 13.4 section 2 beyond the three auto-approvals of types.go. install.package is not
// enabled in B1 (class C2, section 4A): it has no id here, so the allowlist refuses it.
const (
	OpSendThirdParty   = "send.third_party"
	OpVaultUse         = "vault.use"
	OpLocalDestructive = "local.destructive"
	OpHostSetting      = "host.setting"
)

// AllowedTyped says whether op is one of the typed operations of this file. Anything else (install.package, an
// operation on the never-liftable list, an unknown id) is denied with no way to approve (T15).
func AllowedTyped(op string) bool {
	return slices.Contains([]string{OpSendThirdParty, OpVaultUse, OpLocalDestructive, OpHostSetting}, op)
}

// SendThirdParty enables send.third_party for one platform and account. Destinations are canonical handles or hosts,
// or Any. A platform without a handle rule (CanonHandle) is refused at entry.
type SendThirdParty struct {
	Platform     string   `json:"platform"`
	Account      string   `json:"account"`
	Destinations []string `json:"destinations"`
	MaxPerRun    int64    `json:"max_per_run"`
	ContentClass string   `json:"content_class"`
}

// LocalDestructive enables local.destructive under roots (their directory identity is recorded by the entry, and
// the effect is performed on the open handle; neither is in this package).
type LocalDestructive struct {
	Roots    []string `json:"roots"`
	Verbs    []string `json:"verbs"` // delete, overwrite
	MaxCount int64    `json:"max_count"`
	MaxBytes int64    `json:"max_bytes"`
}

// HostSetting enables host.setting for the listed keys, each with the values it may take, or Any. A key on the
// never-liftable list (spec 4.2) is refused first, whatever the charter says.
type HostSetting struct {
	Keys map[string][]string `json:"keys"`
}

// NeverLiftableSettings is the list of host setting keys no charter can enable (spec 4.2). It is checked before
// anything else. The authoritative list belongs to the host; this default is the floor and is only ever extended.
var NeverLiftableSettings = []string{"operator_gate", "agent_access", "vault_grant", "doomsday", "approval_policy", "charter"}

// VaultUse enables vault.use. Every number is a plain integer within published bounds (note 13.4 section 5A).
type VaultUse struct {
	Secrets     []string `json:"secrets"` // exact, case-sensitive
	Hosts       []string `json:"hosts"`
	MaxAttempts int64    `json:"max_attempts"`
	BucketSize  int64    `json:"bucket_size"`
	BucketCount int64    `json:"bucket_count"`
	MaxCallMS   int64    `json:"max_call_ms"`
	SlotMS      int64    `json:"slot_ms"`
	Predicate   string   `json:"predicate"` // a marker the host looks for in the body; empty: no predicate
}

// Published bounds of the vault.use numbers.
const (
	MaxBucketSize  = 1 << 20
	MaxBucketCount = 16
	MinCallMS      = 1000
	MaxCallMS      = 60000
	MinSlotMS      = 100
	MaxAttemptsCap = 1000
	MaxSlots       = 64
)

// Slots is ceil(max_call_ms / slot_ms), a constant of the charter.
func (v VaultUse) Slots() int64 { return (v.MaxCallMS + v.SlotMS - 1) / v.SlotMS }

// Cap is the most body bytes the host reads.
func (v VaultUse) Cap() int64 { return v.BucketSize * v.BucketCount }

func bad(field, why string) error { return invalidLimit("vault.use: %s %s", field, why) }

// Validate checks bounds. Each field is range-checked before any product is formed, so nothing wraps.
func (v VaultUse) Validate() error {
	switch {
	case len(v.Secrets) == 0 || slices.Contains(v.Secrets, ""):
		return bad("secrets", "must name at least one secret")
	case len(v.Hosts) == 0:
		return bad("hosts", "must name at least one host (or the explicit "+Any+")")
	case v.BucketSize < 1 || v.BucketSize > MaxBucketSize:
		return bad("bucket_size", fmt.Sprintf("is 1 to %d", MaxBucketSize))
	case v.BucketCount < 1 || v.BucketCount > MaxBucketCount:
		return bad("bucket_count", fmt.Sprintf("is 1 to %d", MaxBucketCount))
	case v.MaxCallMS < MinCallMS || v.MaxCallMS > MaxCallMS:
		return bad("max_call_ms", fmt.Sprintf("is %d to %d", MinCallMS, MaxCallMS))
	case v.SlotMS < MinSlotMS || v.SlotMS > v.MaxCallMS:
		return bad("slot_ms", fmt.Sprintf("is %d to max_call_ms", MinSlotMS))
	case v.MaxAttempts < 1 || v.MaxAttempts > MaxAttemptsCap:
		return bad("max_attempts", fmt.Sprintf("is 1 to %d", MaxAttemptsCap))
	case v.Slots() > MaxSlots:
		return bad("slot_ms", fmt.Sprintf("gives %d slots, over %d", v.Slots(), MaxSlots))
	}
	for _, h := range v.Hosts {
		if h == Any {
			continue
		}
		if _, err := CanonHost(h); err != nil {
			return bad("hosts", err.Error())
		}
	}
	return nil
}

// ParseVaultUse reads the charter's vault.use object from JSON, refusing every numeric field that is not a plain
// integer (a float, a string, a sign, an exponent, an overflow) and naming the field. NaN and Infinity are not JSON
// and fail the parse. The result is validated.
func ParseVaultUse(raw []byte) (VaultUse, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return VaultUse{}, bad("object", "does not parse: "+err.Error())
	}
	var v VaultUse
	for name, dst := range map[string]*int64{"bucket_size": &v.BucketSize, "bucket_count": &v.BucketCount, "max_call_ms": &v.MaxCallMS, "slot_ms": &v.SlotMS, "max_attempts": &v.MaxAttempts} {
		r, ok := m[name]
		if !ok {
			return VaultUse{}, bad(name, "is missing")
		}
		n, err := ParseInt64(string(r))
		if err != nil {
			return VaultUse{}, bad(name, "is not a plain integer")
		}
		*dst = n
	}
	rest := map[string]json.RawMessage{}
	for _, k := range []string{"secrets", "hosts", "predicate"} {
		if r, ok := m[k]; ok {
			rest[k] = r
		}
	}
	b, _ := json.Marshal(rest)
	if err := json.Unmarshal(b, &v); err != nil {
		return VaultUse{}, bad("secrets/hosts/predicate", "do not parse: "+err.Error())
	}
	return v, v.Validate()
}

func (s SendThirdParty) validate() error {
	if !HasPlatformRule(s.Platform) {
		return invalidLimit("%s: %v: %q", OpSendThirdParty, ErrNoPlatformRule, s.Platform)
	}
	switch {
	case s.Account == "":
		return invalidLimit("%s: account must name an account of the profile", OpSendThirdParty)
	case len(s.Destinations) == 0:
		return invalidLimit("%s: destinations must be listed (or the explicit %s)", OpSendThirdParty, Any)
	case s.MaxPerRun < 1:
		return invalidLimit("%s: max_per_run must be at least 1", OpSendThirdParty)
	case s.ContentClass == "":
		return invalidLimit("%s: content_class is needed", OpSendThirdParty)
	}
	for _, d := range s.Destinations {
		if d != Any {
			if _, err := s.canonDest(d); err != nil {
				return invalidLimit("%s: %v", OpSendThirdParty, err)
			}
		}
	}
	return nil
}

func (s SendThirdParty) canonDest(d string) (string, error) { return CanonHandle(s.Platform, d) }

func (l LocalDestructive) validate() error {
	switch {
	case len(l.Roots) == 0:
		return invalidLimit("%s: roots must be listed", OpLocalDestructive)
	case len(l.Verbs) == 0 || !subset(l.Verbs, []string{"delete", "overwrite"}):
		return invalidLimit("%s: verbs are delete and overwrite", OpLocalDestructive)
	case l.MaxCount < 1 || l.MaxBytes < 0:
		return invalidLimit("%s: max_count must be at least 1 and max_bytes not negative", OpLocalDestructive)
	}
	for _, r := range l.Roots {
		if len(r) < 2 || r[0] != '/' {
			return invalidLimit("%s: a root is an absolute path, %q is not", OpLocalDestructive, r)
		}
	}
	return nil
}

func (h HostSetting) validate() error {
	if len(h.Keys) == 0 {
		return invalidLimit("%s: keys must be listed", OpHostSetting)
	}
	for k, vals := range h.Keys {
		if slices.Contains(NeverLiftableSettings, k) {
			return invalidLimit("%s: %q is on the never-liftable list", OpHostSetting, k)
		}
		if len(vals) == 0 {
			return invalidLimit("%s: %q needs its allowed values (or the explicit %s)", OpHostSetting, k, Any)
		}
	}
	return nil
}

func subset(a, of []string) bool {
	for _, x := range a {
		if !slices.Contains(of, x) {
			return false
		}
	}
	return true
}
