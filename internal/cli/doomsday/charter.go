// Package doomsday is the command line of the open-ended ("doomsday") mode:
// start, stop, status and render. It holds no authority of its own. The
// charter's canonical bytes, hash and insertion belong to the store, Stop's
// order to the stop package; both are reached through Engine, so this package
// is testable with a fake and the mode stays off where no Engine is wired.
package doomsday

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// Line is one separately ticked lifted-authority line (spec 3.1).
type Line string

const (
	LineSpending     Line = "spending"
	LineDestructive  Line = "destructive_local"
	LineSecrets      Line = "secrets_vault"
	LineOutbound     Line = "outbound_third_party"
	LineInstall      Line = "install_software"
	LineHostSettings Line = "host_settings"
	// LinePlatformCaps is per-run platform caps. It is ticked by name only:
	// the "everything" preset never includes it (Q4).
	LinePlatformCaps Line = "platform_caps"
)

// AllLines is every line, in the order the charter shows them.
var AllLines = []Line{LineSpending, LineDestructive, LineSecrets, LineOutbound, LineInstall, LineHostSettings, LinePlatformCaps}

// EverythingLines is the "everything" preset: every line except platform caps.
func EverythingLines() []Line {
	var out []Line
	for _, l := range AllLines {
		if l != LinePlatformCaps {
			out = append(out, l)
		}
	}
	return out
}

// Limit defaults and the charter maximum (spec 3.1, Q1).
const (
	DefaultTreeDepth     = 4
	MaxTreeDepth         = 12
	DefaultLivePerTree   = 30
	DefaultMaxConcurrent = 30
	DefaultSpawnRate     = 6   // spawns per lead per 10 minutes, today's limit
	DefaultOpenSessions  = 50  // today's limit
	DefaultMaxTotal      = 100 // not fixed by the spec: a bounded queue, chosen here
	maxCharterBytes      = 1 << 20
)

// Limits are the named, unit-bearing inputs to the existing checks.
type Limits struct {
	TreeDepth     int `json:"tree_depth"`
	LivePerTree   int `json:"live_per_tree"`
	SpawnRate     int `json:"spawn_rate"`
	OpenSessions  int `json:"open_sessions"`
	MaxConcurrent int `json:"max_concurrent"`
	MaxTotal      int `json:"max_total"`
	// BudgetMicro is the run's USD ceiling in micro-dollars. Exactly one of
	// BudgetMicro and NoCeiling is chosen; there is no silent default.
	BudgetMicro int64 `json:"budget_micro,omitempty"`
	NoCeiling   bool  `json:"no_ceiling,omitempty"`
	// ConcurrentAboveLive is the explicit choice max_concurrent above
	// live_per_tree needs.
	ConcurrentAboveLive bool `json:"concurrent_above_live,omitempty"`
	// The resource governor's numbers; zero is its default (500 rows, 8 MiB, safe minimum 2). They bound the queue of
	// launches waiting for a slot and the concurrency used when the machine cannot be read; none raises max_concurrent.
	QueueMaxRows      int   `json:"queue_max_rows,omitempty"`
	QueueMaxBytes     int64 `json:"queue_max_bytes,omitempty"`
	SafeMinConcurrent int   `json:"safe_min_concurrent,omitempty"`
}

// Pin names one model exactly as the host reports it (Q6).
type Pin struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Version  string `json:"version"`
}

func (p Pin) isSet() bool { return p.Provider != "" && p.Model != "" && p.Version != "" }

// Pins fix the effective models and the judging policy for the whole run.
type Pins struct {
	Boss     Pin    `json:"boss"`
	Verifier Pin    `json:"verifier"`
	Judging  string `json:"judging_policy"`
}

// PerAsk are the limits for what the boss may auto-approve (Q5). All zero
// auto-approves nothing.
type PerAsk struct {
	SpendMicro int64  `json:"ask_spend_micro"`
	Tasks      int    `json:"ask_tasks"`
	Access     string `json:"ask_access"` // named when any limit is declared; never "full"
	Depth      int    `json:"ask_depth"`
	// RunSpendMicro and RunTasks cap the run's cumulative auto-approved totals (note 13.4), so many small asks cannot
	// add up to what one large ask would have been. Required, each at least the per-ask value, once any limit is declared.
	RunSpendMicro int64 `json:"run_spend_micro,omitempty"`
	RunTasks      int64 `json:"run_tasks,omitempty"`
	// AnswerClasses are the answer classes the boss may answer by itself (ask.answer). Empty: every critical ask parks.
	AnswerClasses []string `json:"answer_classes,omitempty"`
}

// AutoApproves says whether any per-ask limit is declared.
func (a PerAsk) AutoApproves() bool {
	return a.SpendMicro > 0 || a.Tasks > 0 || a.Access != "" || a.Depth > 0 || a.RunSpendMicro > 0 || a.RunTasks > 0 || len(a.AnswerClasses) > 0
}

// Criterion is a success criterion: a command with its expected result, or a rubric.
type Criterion struct {
	Kind     string `json:"kind"` // "command" or "rubric"
	Text     string `json:"text,omitempty"`
	Command  string `json:"command,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// Charter is what the operator writes and the store hashes.
type Charter struct {
	Goal     string      `json:"goal"`
	Criteria []Criterion `json:"criteria,omitempty"`
	Limits   Limits      `json:"limits"`
	Lines    []Line      `json:"lines,omitempty"`
	Pins     Pins        `json:"pins"`
	PerAsk   PerAsk      `json:"per_ask"`
}

// LoadCharter reads a charter strictly (unknown fields are refused) and fills
// the zero limits with their defaults.
func LoadCharter(r io.Reader) (Charter, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxCharterBytes+1))
	if err != nil {
		return Charter{}, err
	}
	if len(raw) > maxCharterBytes {
		return Charter{}, fmt.Errorf("the charter is larger than %d bytes", maxCharterBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Charter
	if err := dec.Decode(&c); err != nil {
		return Charter{}, fmt.Errorf("the charter is not valid: %w", err)
	}
	if dec.More() {
		return Charter{}, errors.New("the charter is not valid: data after the charter object")
	}
	c.applyDefaults()
	return c, nil
}

func (c *Charter) applyDefaults() {
	l := &c.Limits
	for _, d := range []struct {
		v   *int
		def int
	}{{&l.TreeDepth, DefaultTreeDepth}, {&l.LivePerTree, DefaultLivePerTree}, {&l.SpawnRate, DefaultSpawnRate},
		{&l.OpenSessions, DefaultOpenSessions}, {&l.MaxConcurrent, DefaultMaxConcurrent}, {&l.MaxTotal, DefaultMaxTotal}} {
		if *d.v == 0 {
			*d.v = d.def
		}
	}
}

// Tick adds lines (de-duplicated, in AllLines order).
func (c *Charter) Tick(lines ...Line) {
	have := map[Line]bool{}
	for _, l := range c.Lines {
		have[l] = true
	}
	for _, l := range lines {
		have[l] = true
	}
	var out []Line
	for _, l := range AllLines {
		if have[l] {
			out = append(out, l)
		}
	}
	for l := range have { // unknown names stay, so Validate can refuse them
		if !contains(AllLines, l) {
			out = append(out, l)
		}
	}
	c.Lines = out
}

func contains(ls []Line, l Line) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}

// Has says whether a line is ticked.
func (c Charter) Has(l Line) bool { return contains(c.Lines, l) }

// Validate lists every problem at once.
func (c Charter) Validate() error {
	var bad []string
	add := func(f string, a ...interface{}) { bad = append(bad, fmt.Sprintf(f, a...)) }
	if strings.TrimSpace(c.Goal) == "" {
		add("the goal is empty")
	}
	l := c.Limits
	if l.TreeDepth < 1 || l.TreeDepth > MaxTreeDepth {
		add("tree_depth %d is outside 1..%d", l.TreeDepth, MaxTreeDepth)
	}
	for name, v := range map[string]int{"live_per_tree": l.LivePerTree, "spawn_rate": l.SpawnRate,
		"open_sessions": l.OpenSessions, "max_concurrent": l.MaxConcurrent, "max_total": l.MaxTotal} {
		if v < 1 {
			add("%s must be at least 1", name)
		}
	}
	if l.MaxConcurrent > l.LivePerTree && !l.ConcurrentAboveLive {
		add("max_concurrent %d is above live_per_tree %d: set concurrent_above_live to choose that on purpose", l.MaxConcurrent, l.LivePerTree)
	}
	if l.QueueMaxRows < 0 || l.QueueMaxRows > store.MaxQueueRows || l.QueueMaxBytes < 0 || l.QueueMaxBytes > store.MaxQueueBytes {
		add("queue_max_rows is 0..%d and queue_max_bytes 0..%d (0 is the default)", store.MaxQueueRows, store.MaxQueueBytes)
	}
	if l.SafeMinConcurrent < 0 || l.SafeMinConcurrent > l.MaxConcurrent {
		add("safe_min_concurrent %d is outside 0..max_concurrent (0 is the default)", l.SafeMinConcurrent)
	}
	if l.MaxTotal < l.LivePerTree {
		add("max_total %d is below live_per_tree %d", l.MaxTotal, l.LivePerTree)
	}
	switch {
	case l.BudgetMicro < 0:
		add("budget_micro is negative")
	case l.BudgetMicro == 0 && !l.NoCeiling:
		add("choose a budget: set budget_micro, or no_ceiling for no ceiling of the charter's own")
	case l.BudgetMicro > 0 && l.NoCeiling:
		add("budget_micro and no_ceiling are both set")
	}
	for _, x := range c.Lines {
		if !contains(AllLines, x) {
			add("unknown line %q", x)
		}
	}
	if !c.Pins.Boss.isSet() {
		add("pin the boss: provider, model and version are all needed")
	}
	if c.Pins.Judging == "" {
		add("pin the judging purpose policy")
	}
	if !c.Pins.Verifier.isSet() {
		add("pin the finishing verifier: provider, model and version are all needed")
	} else if c.Pins.Verifier == c.Pins.Boss {
		add("the verifier is the boss's own model: finishing needs a distinct pinned verifier")
	}
	if p := c.PerAsk; p.SpendMicro < 0 || p.Tasks < 0 || p.Depth < 0 || p.RunSpendMicro < 0 || p.RunTasks < 0 {
		add("a per-ask limit is negative")
	} else if p.Access == "full" {
		add("ask_access is never full")
	} else if p.AutoApproves() && p.Access == "" {
		add("name ask_access when any per-ask limit is declared")
	} else if p.AutoApproves() && (p.RunSpendMicro < p.SpendMicro || p.RunTasks < int64(p.Tasks) || p.RunTasks < 1) {
		add("name the run caps (run_spend_micro, run_tasks) when any per-ask limit is declared: each at least the per-ask limit")
	}
	for i, k := range c.Criteria {
		switch {
		case k.Kind == "command" && (k.Command == "" || k.Expected == ""):
			add("criterion %d: a command criterion needs the command and its expected result", i+1)
		case k.Kind == "rubric" && k.Text == "":
			add("criterion %d: a rubric criterion needs its text", i+1)
		case k.Kind != "command" && k.Kind != "rubric":
			add("criterion %d: kind must be command or rubric", i+1)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return errors.New("the charter is invalid:\n  - " + strings.Join(bad, "\n  - "))
}
