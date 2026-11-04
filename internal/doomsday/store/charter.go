package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

// CanonVersion is the version of the canonical encoding. It is part of the hashed bytes, so a change of the rules
// can never make an old hash match a new form.
const CanonVersion = 3

// LegacyCanonVersion is the version before the governor's limits: a charter stored under it keeps verifying, and its
// governor limits read as unset (their defaults). Nothing new is ever written under it.
const LegacyCanonVersion = 2

// Governor limit bounds (Validate).
const (
	MaxQueueRows  = 100000
	MaxQueueBytes = 1 << 30
)

const charterDomain = "monoes/doomsday/charter/v1"

var knownLines = map[string]bool{
	LineSpending: true, LineDestructive: true, LineVault: true, LineThirdParty: true,
	LineInstall: true, LineHostSettings: true, LinePlatformCaps: true,
}

var criterionKinds = map[string]bool{"command": true, "test": true, "file": true, "output": true, "rubric": true}

// EverythingLines is the "everything" preset: every line except platform caps, which can only be ticked by name (Q4).
func EverythingLines() []string {
	return []string{LineSpending, LineDestructive, LineVault, LineThirdParty, LineInstall, LineHostSettings}
}

// WithDefaults fills the unset limits with the settled defaults (depth 4, live_per_tree 30, max_concurrent 30).
func (c Charter) WithDefaults() Charter {
	if c.Limits.TreeDepth == 0 {
		c.Limits.TreeDepth = DefaultTreeDepth
	}
	if c.Limits.LivePerTree == 0 {
		c.Limits.LivePerTree = 30
	}
	if c.Limits.MaxConcurrent == 0 {
		c.Limits.MaxConcurrent = 30
	}
	return c
}

// Validate refuses a charter the mode cannot honour. It has no defaults of its own: every limit must be explicit.
func (c Charter) Validate() error {
	bad := func(f string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidCharter, fmt.Sprintf(f, a...))
	}
	if c.Profile == "" {
		return bad("profile is empty")
	}
	if c.Generation < 1 {
		return bad("generation must be at least 1")
	}
	if c.Goal == "" {
		return bad("goal is empty")
	}
	for _, cr := range c.Criteria {
		if !criterionKinds[cr.Kind] {
			return bad("criterion kind %q", cr.Kind)
		}
	}
	l := c.Limits
	if l.QueueMaxRows < 0 || l.QueueMaxRows > MaxQueueRows || l.QueueMaxBytes < 0 || l.QueueMaxBytes > MaxQueueBytes {
		return bad("queue_max_rows is 0..%d and queue_max_bytes 0..%d", MaxQueueRows, MaxQueueBytes)
	}
	if l.SafeMinConcurrent < 0 || l.SafeMinConcurrent > l.MaxConcurrent {
		return bad("safe_min_concurrent %d is outside 0..max_concurrent", l.SafeMinConcurrent)
	}
	if l.TreeDepth < 1 || l.TreeDepth > MaxTreeDepth {
		return bad("tree_depth %d outside 1..%d", l.TreeDepth, MaxTreeDepth)
	}
	for name, v := range map[string]int{"live_per_tree": l.LivePerTree, "spawn_rate": l.SpawnRate,
		"open_sessions": l.OpenSessions, "max_concurrent": l.MaxConcurrent, "max_total": l.MaxTotal} {
		if v < 1 {
			return bad("%s must be positive", name)
		}
	}
	if l.NoCeiling == (l.BudgetMicro > 0) || l.BudgetMicro < 0 {
		return bad("budget must be either a positive ceiling or the explicit no-ceiling value")
	}
	for _, ln := range c.Lines {
		if !knownLines[ln] {
			return bad("unknown line %q", ln)
		}
	}
	if err := c.PerAsk.validate(); err != nil {
		return bad("%v", err)
	}
	if err := c.Typed.validate(c.Lines); err != nil {
		return bad("%v", err)
	}
	if !c.BossPin.complete() || !c.VerifierPin.complete() {
		return bad("the boss and the verifier must each be pinned (provider, model, version, purpose policy)")
	}
	if c.BossPin == c.VerifierPin {
		return bad("the finishing verifier must be a pin distinct from the boss")
	}
	return nil
}

func (p PerAsk) validate() error {
	if !p.Declared {
		if p.SpendMicro != 0 || p.Tasks != 0 || p.Access != "" || p.Depth != 0 || p.RunSpendMicro != 0 || p.RunTasks != 0 || len(p.AnswerClasses) != 0 {
			return fmt.Errorf("per-ask values set without declaring per-ask limits")
		}
		return nil
	}
	if p.SpendMicro < 0 || p.Tasks < 0 || p.Depth < 0 {
		return fmt.Errorf("per-ask limits must not be negative")
	}
	if p.Access == "" || p.Access == "full" {
		return fmt.Errorf("ask_access must be named and is never full")
	}
	if p.RunSpendMicro < p.SpendMicro || p.RunTasks < int64(p.Tasks) || p.RunSpendMicro < 0 || p.RunTasks < 0 {
		return fmt.Errorf("run caps must be set and at least the per-ask limits")
	}
	seen := map[string]bool{}
	for _, cl := range p.AnswerClasses {
		if cl == "" || seen[cl] {
			return fmt.Errorf("answer classes must be named and distinct")
		}
		seen[cl] = true
	}
	return nil
}

func (p Pin) complete() bool {
	return p.Provider != "" && p.Model != "" && p.Version != "" && p.PurposePolicy != ""
}

// normalized returns a copy whose lines are sorted and de-duplicated, so the same set always encodes alike.
func (c Charter) normalized() Charter {
	seen := map[string]bool{}
	var ls []string
	for _, l := range c.Lines {
		if !seen[l] {
			seen[l] = true
			ls = append(ls, l)
		}
	}
	sort.Strings(ls)
	c.Lines = ls
	if len(c.PerAsk.AnswerClasses) > 0 {
		cl := append([]string(nil), c.PerAsk.AnswerClasses...)
		sort.Strings(cl)
		c.PerAsk.AnswerClasses = cl
	}
	c.Typed = c.Typed.normalized()
	return c
}

// Canonical is the versioned, domain-separated, length-delimited encoding of every field, including the profile and
// the generation. Two charters differing in any field never encode alike.
func (c Charter) Canonical() []byte {
	c = c.normalized()
	ver := int64(CanonVersion)
	if c.legacy {
		ver = LegacyCanonVersion
	}
	var b []byte
	str := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	num := func(n int64) { b = binary.BigEndian.AppendUint64(b, uint64(n)) }
	flag := func(v bool) {
		if v {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	pin := func(p Pin) { str(p.Provider); str(p.Model); str(p.Version); str(p.PurposePolicy) }
	str(charterDomain)
	num(ver)
	str(c.Profile)
	num(c.Generation)
	str(c.Goal)
	num(int64(len(c.Criteria)))
	for _, cr := range c.Criteria {
		str(cr.Kind)
		str(cr.Spec)
		str(cr.Expected)
	}
	l := c.Limits
	for _, v := range []int{l.TreeDepth, l.LivePerTree, l.SpawnRate, l.OpenSessions, l.MaxConcurrent, l.MaxTotal} {
		num(int64(v))
	}
	num(l.BudgetMicro)
	flag(l.NoCeiling)
	if !c.legacy {
		num(int64(l.QueueMaxRows))
		num(l.QueueMaxBytes)
		num(int64(l.SafeMinConcurrent))
	}
	num(int64(len(c.Lines)))
	for _, ln := range c.Lines {
		str(ln)
	}
	pin(c.BossPin)
	pin(c.VerifierPin)
	flag(c.PerAsk.Declared)
	num(c.PerAsk.SpendMicro)
	num(int64(c.PerAsk.Tasks))
	str(c.PerAsk.Access)
	num(int64(c.PerAsk.Depth))
	num(c.PerAsk.RunSpendMicro)
	num(c.PerAsk.RunTasks)
	num(int64(len(c.PerAsk.AnswerClasses)))
	for _, cl := range c.PerAsk.AnswerClasses {
		str(cl)
	}
	c.Typed.appendCanonical(str, num, flag)
	return b
}

// canonVersion is the version this charter encodes under: a charter loaded from a legacy row encodes as it was
// written, so it keeps its hash, id and MAC.
func (c Charter) canonVersion() int {
	if c.legacy {
		return LegacyCanonVersion
	}
	return CanonVersion
}

// Hash is SHA-256 over Canonical.
func (c Charter) Hash() [32]byte { return sha256.Sum256(c.Canonical()) }

// ID is the charter identity derived from the hash (and so from the profile and generation).
func (c Charter) ID() string {
	h := c.Hash()
	return "dd-" + hex.EncodeToString(h[:12])
}

// HMACKeyer is a Keyer over a raw key; each domain gets its own derived key.
type HMACKeyer struct{ key []byte }

// NewHMACKeyer wraps key, which the caller derives from the profile's vault key.
func NewHMACKeyer(key []byte) HMACKeyer { return HMACKeyer{key: append([]byte(nil), key...)} }

// MAC authenticates the length-delimited parts under the key derived for domain.
func (k HMACKeyer) MAC(domain string, parts ...[]byte) ([]byte, error) {
	if len(k.key) == 0 {
		return nil, ErrUnverifiable
	}
	dk := hmac.New(sha256.New, k.key)
	dk.Write([]byte("monoes/doomsday/key/v1/" + domain))
	m := hmac.New(sha256.New, dk.Sum(nil))
	for _, p := range parts {
		m.Write(binary.AppendUvarint(nil, uint64(len(p))))
		m.Write(p)
	}
	return m.Sum(nil), nil
}
