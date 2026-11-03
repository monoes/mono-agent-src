package store

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// The constraints of the typed operations of note 13.4, carried by the charter so they are part of its hash. A nil
// section enables nothing (default deny), and a section is allowed only with the authority line it needs. The ops
// package turns these into its own Constraints; the numeric bounds below are copies of its published ones, and a test in
// ops pins that the two agree.

// Published bounds (copies of the ops package's).
const (
	maxBucketSize  = 1 << 20
	maxBucketCount = 16
	minCallMS      = 1000
	maxCallMS      = 60000
	minSlotMS      = 100
	maxAttempts    = 1000
	maxSlots       = 64
)

// SendLine enables send.third_party for one platform and account.
type SendLine struct {
	Platform     string   `json:"platform"`
	Account      string   `json:"account"`
	Destinations []string `json:"destinations"` // canonical handles or hosts, or "*"
	MaxPerRun    int64    `json:"max_per_run"`
	ContentClass string   `json:"content_class"`
}

// VaultLine enables vault.use: the secrets (by name), the hosts they may go to, and the attempt and body-read bounds.
// The effect is https only and without a port; the host enforces that at the effect.
type VaultLine struct {
	Secrets     []string `json:"secrets"`
	Hosts       []string `json:"hosts"` // canonical hosts, or "*"
	MaxAttempts int64    `json:"max_attempts"`
	BucketSize  int64    `json:"bucket_size"`
	BucketCount int64    `json:"bucket_count"`
	MaxCallMS   int64    `json:"max_call_ms"`
	SlotMS      int64    `json:"slot_ms"`
	Predicate   string   `json:"predicate"`
}

// LocalLine enables local.destructive under absolute roots.
type LocalLine struct {
	Roots    []string `json:"roots"`
	Verbs    []string `json:"verbs"` // delete, overwrite
	MaxCount int64    `json:"max_count"`
	MaxBytes int64    `json:"max_bytes"`
}

// SettingsLine enables host.setting for the listed keys, each with the values it may take, or "*".
type SettingsLine struct {
	Keys map[string][]string `json:"keys"`
}

// Typed is the charter's section for the typed operations. GrantTargets narrows who a spend grant may go to ("lead",
// "child"); empty keeps both when the spending line is lifted.
type Typed struct {
	Send         *SendLine     `json:"send,omitempty"`
	Vault        *VaultLine    `json:"vault,omitempty"`
	Local        *LocalLine    `json:"local,omitempty"`
	Settings     *SettingsLine `json:"settings,omitempty"`
	GrantTargets []string      `json:"grant_targets,omitempty"`
}

func sortedSet(in []string) []string {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// normalized returns a deep copy whose sets are sorted and de-duplicated, so the same set always encodes alike.
func (t Typed) normalized() Typed {
	var out Typed
	if s := t.Send; s != nil {
		c := *s
		c.Destinations = sortedSet(s.Destinations)
		out.Send = &c
	}
	if v := t.Vault; v != nil {
		c := *v
		c.Secrets, c.Hosts = sortedSet(v.Secrets), sortedSet(v.Hosts)
		out.Vault = &c
	}
	if l := t.Local; l != nil {
		c := *l
		c.Roots, c.Verbs = sortedSet(l.Roots), sortedSet(l.Verbs)
		out.Local = &c
	}
	if h := t.Settings; h != nil {
		c := SettingsLine{Keys: make(map[string][]string, len(h.Keys))}
		for k, vs := range h.Keys {
			c.Keys[k] = sortedSet(vs)
		}
		out.Settings = &c
	}
	out.GrantTargets = sortedSet(t.GrantTargets)
	return out
}

// validate refuses a section the mode cannot honour. lines is the charter's lifted lines.
func (t Typed) validate(lines []string) error {
	needs := func(present bool, line, what string) error {
		if present && !slices.Contains(lines, line) {
			return fmt.Errorf("%s is declared but the %s line is not lifted", what, line)
		}
		return nil
	}
	for _, e := range []error{
		needs(t.Send != nil, LineThirdParty, "send"), needs(t.Vault != nil, LineVault, "vault"),
		needs(t.Local != nil, LineDestructive, "local"), needs(t.Settings != nil, LineHostSettings, "settings"),
	} {
		if e != nil {
			return e
		}
	}
	if s := t.Send; s != nil {
		if s.Platform == "" || s.Account == "" || s.ContentClass == "" || s.MaxPerRun < 1 || !namesAll(s.Destinations) {
			return fmt.Errorf("send needs a platform, account, content class, listed destinations and max_per_run of at least 1")
		}
	}
	if v := t.Vault; v != nil {
		if err := v.validate(); err != nil {
			return err
		}
	}
	if l := t.Local; l != nil {
		if err := l.validate(); err != nil {
			return err
		}
	}
	if h := t.Settings; h != nil {
		if len(h.Keys) == 0 {
			return fmt.Errorf("settings need at least one key")
		}
		for k, vs := range h.Keys {
			if strings.TrimSpace(k) == "" || !namesAll(vs) {
				return fmt.Errorf("setting %q needs its allowed values (or *)", k)
			}
		}
	}
	for _, g := range t.GrantTargets {
		if g != "lead" && g != "child" {
			return fmt.Errorf("grant target %q is not lead or child", g)
		}
	}
	if len(t.GrantTargets) > 0 && !slices.Contains(lines, LineSpending) {
		return fmt.Errorf("grant targets are declared but the %s line is not lifted", LineSpending)
	}
	return nil
}

func (l LocalLine) validate() error {
	if !namesAll(l.Roots) || !namesAll(l.Verbs) || l.MaxCount < 1 || l.MaxBytes < 0 {
		return fmt.Errorf("local needs roots, verbs, max_count of at least 1 and a non-negative max_bytes")
	}
	for _, r := range l.Roots {
		if len(r) < 2 || r[0] != '/' {
			return fmt.Errorf("a local root is an absolute path")
		}
	}
	for _, v := range l.Verbs {
		if v != "delete" && v != "overwrite" {
			return fmt.Errorf("local verbs are delete and overwrite")
		}
	}
	return nil
}

func namesAll(xs []string) bool {
	if len(xs) == 0 {
		return false
	}
	for _, x := range xs {
		if strings.TrimSpace(x) == "" {
			return false
		}
	}
	return true
}

func (v VaultLine) validate() error {
	bad := func(f string) error { return fmt.Errorf("vault: %s is outside its published bound", f) }
	switch {
	case !namesAll(v.Secrets) || !namesAll(v.Hosts):
		return fmt.Errorf("vault needs at least one secret and one host (or *)")
	case v.BucketSize < 1 || v.BucketSize > maxBucketSize:
		return bad("bucket_size")
	case v.BucketCount < 1 || v.BucketCount > maxBucketCount:
		return bad("bucket_count")
	case v.MaxCallMS < minCallMS || v.MaxCallMS > maxCallMS:
		return bad("max_call_ms")
	case v.SlotMS < minSlotMS || v.SlotMS > v.MaxCallMS:
		return bad("slot_ms")
	case v.MaxAttempts < 1 || v.MaxAttempts > maxAttempts:
		return bad("max_attempts")
	case (v.MaxCallMS+v.SlotMS-1)/v.SlotMS > maxSlots:
		return bad("slot count")
	}
	return nil
}

// appendCanonical writes the section in the same length-delimited style as the rest of the charter: a presence flag per
// section, then every field, sets sorted. t must already be normalized.
func (t Typed) appendCanonical(str func(string), num func(int64), flag func(bool)) {
	list := func(xs []string) {
		num(int64(len(xs)))
		for _, x := range xs {
			str(x)
		}
	}
	flag(t.Send != nil)
	if s := t.Send; s != nil {
		str(s.Platform)
		str(s.Account)
		list(s.Destinations)
		num(s.MaxPerRun)
		str(s.ContentClass)
	}
	flag(t.Vault != nil)
	if v := t.Vault; v != nil {
		list(v.Secrets)
		list(v.Hosts)
		for _, n := range []int64{v.MaxAttempts, v.BucketSize, v.BucketCount, v.MaxCallMS, v.SlotMS} {
			num(n)
		}
		str(v.Predicate)
	}
	flag(t.Local != nil)
	if l := t.Local; l != nil {
		list(l.Roots)
		list(l.Verbs)
		num(l.MaxCount)
		num(l.MaxBytes)
	}
	flag(t.Settings != nil)
	if h := t.Settings; h != nil {
		keys := make([]string, 0, len(h.Keys))
		for k := range h.Keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		num(int64(len(keys)))
		for _, k := range keys {
			str(k)
			list(h.Keys[k])
		}
	}
	list(t.GrantTargets)
}
