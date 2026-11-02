package agentpurposes

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentroster"
	_ "github.com/monoes/mono-agent/internal/config" // installs the storage layer's write guard
	"github.com/monoes/mono-agent/internal/testdb"
)

func TestBuiltinIDsAreTheStableList(t *testing.T) {
	want := "coding debugging research deep-research planning strategy review testing security-audit writing data automation browsing design triage judging orchestrating translation"
	if got := strings.Join(IDs(), " "); got != want {
		t.Errorf("ids = %s", got)
	}
}

func TestForCategory(t *testing.T) {
	for cat, want := range map[string]string{
		"engineering": "coding", "review": "review", "research": "research", "testing": "testing",
		"planning": "planning", "security": "security-audit", "docs": "writing", "analysis": "data",
		"automation": "automation", "Engineering ": "coding", "backend-engineering": "coding",
		"operations": "", "": "", "general": "",
	} {
		if got := ForCategory(cat); got != want {
			t.Errorf("ForCategory(%q) = %q, want %q", cat, got, want)
		}
	}
}

func TestDecodeReadsAValidRowAndRefusesACorruptOne(t *testing.T) {
	good := `{"v":1,"pairs":[{"runtime":"claude","model":"haiku"}],"rule":"order","differentFromAuthor":true,"fallback":"any","budgetSharePct":20}`
	s, err := Decode(good)
	if err != nil || len(s.Pairs) != 1 || s.Rule != RuleOrder || !s.DifferentFromAuthor || s.Fallback != FallbackAny || s.BudgetSharePct != 20 {
		t.Fatalf("good row: %+v %v", s, err)
	}
	for name, raw := range map[string]string{
		"not json":         `{`,
		"wrong version":    `{"v":2,"pairs":[],"rule":"any","fallback":"none","budgetSharePct":0}`,
		"unknown field":    `{"v":1,"pairs":[],"rule":"any","fallback":"none","budgetSharePct":0,"extra":1}`,
		"unknown rule":     `{"v":1,"pairs":[],"rule":"random","fallback":"none","budgetSharePct":0}`,
		"unknown fallback": `{"v":1,"pairs":[],"rule":"any","fallback":"maybe","budgetSharePct":0}`,
		"budget over 100":  `{"v":1,"pairs":[],"rule":"any","fallback":"none","budgetSharePct":101}`,
		"half a pair":      `{"v":1,"pairs":[{"runtime":"claude","model":""}],"rule":"any","fallback":"none","budgetSharePct":0}`,
		"duplicate pair":   `{"v":1,"pairs":[{"runtime":"a","model":"b"},{"runtime":"a","model":"b"}],"rule":"any","fallback":"none","budgetSharePct":0}`,
		"trailing data":    good + `{}`,
		"empty":            ``,
	} {
		set, err := Decode(raw)
		if err == nil {
			t.Errorf("%s: decoded %+v", name, set)
		}
		if !set.IsDefault() || set.Rule != RuleAny || set.Fallback != FallbackNone {
			t.Errorf("%s: a corrupt row must read as the default, got %+v", name, set)
		}
	}
}

func rosterOf(entries ...agentroster.Entry) []agentroster.RuntimeRoster {
	by := map[string]*agentroster.RuntimeRoster{}
	var out []agentroster.RuntimeRoster
	for _, e := range entries {
		rr := by[e.Runtime]
		if rr == nil {
			out = append(out, agentroster.RuntimeRoster{Runtime: e.Runtime})
			rr = &out[len(out)-1]
			by[e.Runtime] = rr
		}
		rr.Models = append(rr.Models, e)
	}
	for i := range out {
		by[out[i].Runtime] = &out[i]
	}
	return out
}

func entry(runtime, model, state string) agentroster.Entry {
	return agentroster.Entry{Result: agentroster.Result{Runtime: runtime, Model: model}, State: state}
}

func TestOnlyAValidatedPairMayBeChosen(t *testing.T) {
	roster := FromRoster(rosterOf(
		entry("claude", "opus", agentroster.StateReady),
		entry("claude", "old", agentroster.StateStale),
		entry("codex", "bad", agentroster.StateFailed),
		entry("pi", "new", agentroster.StateUntested),
	))
	statuses := map[string]string{}
	for _, p := range roster {
		statuses[p.Runtime+"/"+p.Model] = p.Status
	}
	want := map[string]string{"claude/opus": "ok", "claude/old": "stale", "codex/bad": "failed", "pi/new": "unvalidated"}
	for k, v := range want {
		if statuses[k] != v {
			t.Errorf("%s = %q, want %q", k, statuses[k], v)
		}
	}
	ok := Setting{Pairs: []Pair{{"claude", "opus"}}, Rule: RuleAny, Fallback: FallbackNone}
	if err := CheckValidated(ok, roster); err != nil {
		t.Errorf("a validated pair: %v", err)
	}
	for _, p := range []Pair{{"claude", "old"}, {"codex", "bad"}, {"pi", "new"}, {"claude", "nonexistent"}} {
		bad := Setting{Pairs: []Pair{p}, Rule: RuleAny, Fallback: FallbackNone}
		if err := CheckValidated(bad, roster); err == nil || !strings.Contains(err.Error(), p.Key()) {
			t.Errorf("%s was accepted: %v", p.Key(), err)
		}
	}
}

func TestStoreRoundTripResetAndCorruptRow(t *testing.T) {
	db := testdb.Open(t)
	st := Store{DB: db, ProfileID: "p1"}
	got, err := st.Load("coding")
	if err != nil || got.Err != nil || !got.Setting.IsDefault() {
		t.Fatalf("nothing stored: %+v %v", got, err)
	}
	set := Setting{Pairs: []Pair{{"claude", "haiku"}}, Rule: RuleCheapest, Fallback: FallbackNone, Effort: "low"}
	if err := st.Save("coding", set); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Load("coding"); got.Err != nil || len(got.Setting.Pairs) != 1 || got.Setting.Rule != RuleCheapest || got.Setting.Effort != "low" {
		t.Errorf("round trip: %+v", got)
	}
	if other, _ := (Store{DB: db, ProfileID: "p2"}).Load("coding"); !other.Setting.IsDefault() {
		t.Errorf("another profile sees %+v", other.Setting)
	}
	if u, _ := st.Usable(); len(u["coding"].Pairs) != 1 {
		t.Errorf("usable = %+v", u)
	}
	if err := st.Reset("coding"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Load("coding"); got.Err != nil || !got.Setting.IsDefault() {
		t.Errorf("after reset: %+v", got)
	}
	// A corrupt row reads as the default, reports itself, and is left out of what the resolver uses.
	if err := db.SaveConfig(Prefix+"p1.review", `{"pairs": "nope"`); err != nil {
		t.Fatal(err)
	}
	got, err = st.Load("review")
	if err != nil || got.Err == nil || !got.Setting.IsDefault() {
		t.Errorf("corrupt row: %+v %v", got, err)
	}
	// The resolver's read fails closed: an unreadable row is an error naming the purpose, never the default.
	if u, err := st.Usable(); err == nil || !strings.Contains(err.Error(), "review") || u != nil {
		t.Errorf("a corrupt row must fail the resolver's read and name the purpose: %+v %v", u, err)
	}
	if err := st.Reset("review"); err != nil {
		t.Fatal(err)
	}
	if u, err := st.Usable(); err != nil || len(u) != len(builtins) {
		t.Errorf("after the reset every purpose is usable: %d %v", len(u), err)
	}
	if _, err := st.Load("wizardry"); err == nil {
		t.Error("an unknown purpose was loaded")
	}
	if err := st.Save("wizardry", set); err == nil {
		t.Error("an unknown purpose was saved")
	}
}

// The configs write guard refuses the rows to anything but the operator, so
// even a caller that skipped the command's own check changes nothing.
func TestAnAgentContextCannotWriteTheRows(t *testing.T) {
	db := testdb.Open(t)
	st := Store{DB: db, ProfileID: "p1"}
	t.Setenv("CLAUDECODE", "1")
	set := Setting{Pairs: []Pair{{"claude", "haiku"}}, Rule: RuleAny, Fallback: FallbackNone}
	if err := st.Save("coding", set); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("an agent's save: %v", err)
	}
	if err := db.SaveConfig(Prefix+"p1.coding", "{}"); err == nil {
		t.Error("a raw write of the row passed")
	}
	if got, _ := st.Load("coding"); !got.Setting.IsDefault() {
		t.Errorf("the refused write left %+v", got.Setting)
	}
}

func TestScopesNameRealPurposesAndTheirOwnDefault(t *testing.T) {
	for cat, sc := range scopes {
		if !sc.Allows(sc.Default) {
			t.Errorf("%s: default %s is not allowed", cat, sc.Default)
		}
		for _, id := range sc.Allowed {
			if _, ok := Lookup(id); !ok {
				t.Errorf("%s: %s is not a purpose", cat, id)
			}
		}
	}
	for _, p := range Builtins() {
		if sc, ok := ScopeFor(p.Category); !ok || sc.Default != p.ID {
			t.Errorf("purpose %s: its category %q maps to %+v ok=%v", p.ID, p.Category, sc, ok)
		}
	}
	if _, ok := ScopeFor("mystery"); ok {
		t.Error("an unknown category is mapped")
	}
	if sc, ok := ScopeFor("Backend-Engineering "); !ok || sc.Default != "coding" {
		t.Errorf("a category that contains a mapped one: %+v %v", sc, ok)
	}
}

// LoadAll reads every purpose from one snapshot: while a model is moved from
// one purpose to another in a single write, a reader sees it in exactly one of
// them, never in both or neither (a mix that existed at neither end).
func TestLoadAllReadsOneSnapshot(t *testing.T) {
	db := testdb.Open(t)
	st := Store{DB: db, ProfileID: "p1"}
	pair := func(id string) string {
		v, err := Encode(Setting{Pairs: []Pair{{"claude", id}}, Rule: RuleAny, Fallback: FallbackNone})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	write := func(coding, debugging string) {
		tx, err := db.DB.Begin()
		if err != nil {
			t.Error(err)
			return
		}
		for name, v := range map[string]string{st.name("coding"): coding, st.name("debugging"): debugging} {
			if _, err := tx.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?,?,datetime('now'),datetime('now'))
				ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data`, name, v); err != nil {
				t.Error(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Error(err)
		}
	}
	write(pair("opus"), pair("haiku"))
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 { // opus moves from coding to debugging, haiku back
				write(pair("haiku"), pair("opus"))
			} else {
				write(pair("opus"), pair("haiku"))
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 0; i < 3000; i++ {
		all, err := st.LoadAll()
		if err != nil {
			t.Fatal(err)
		}
		c, d := all["coding"].Setting.Pairs, all["debugging"].Setting.Pairs
		if len(c) != 1 || len(d) != 1 || c[0].Model == d[0].Model {
			t.Fatalf("read %d saw a mix that never existed: coding %v, debugging %v", i, c, d)
		}
	}
}
