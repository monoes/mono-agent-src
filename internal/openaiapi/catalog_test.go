package openaiapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

// testFuncs returns catalog lookups backed by the captured scan: claude,
// codex, antigravity and hermes installed, grok not.
func testFuncs(t *testing.T) CatalogFuncs {
	t.Helper()
	scan := loadScanFixture(t)
	scan.Agents = append(scan.Agents, monomind.ScanEntry{ID: "grok", Installed: false})
	return CatalogFuncs{
		Scan: func(context.Context) (*monomind.ScanResult, error) { return scan, nil },
		Models: func(_ context.Context, runtime, _ string) ([]monomind.RuntimeModel, error) {
			switch runtime {
			case "claude":
				return []monomind.RuntimeModel{
					{ID: "default", Label: "Default"},
					{ID: "opus[1m]", Label: "Opus (1M context)", EffortLevels: []string{"low", "high"}},
					{ID: "opus", Label: "Opus", AliasOf: "opus[1m]"},
				}, nil
			case "codex":
				return []monomind.RuntimeModel{{ID: "gpt-6-astra", Label: "GPT-6-Astra"}}, nil
			case "antigravity":
				return []monomind.RuntimeModel{{ID: "gemini-3.8-flash-high", Label: "Gemini 3.8 Flash (High)"}}, nil
			}
			return nil, nil // a runtime without a listing command: monomind.ListModels answers nil, nil
		},
		Caps: func(context.Context) (*monomind.CapabilitySet, error) {
			return monomind.NewCapabilitySet("2.22.0", monomind.CapAgentExecSandbox), nil
		},
		Roster: func(context.Context) ([]agentroster.Result, error) { return nil, nil },
	}
}

// waitForRefresh waits for the background reload an expired list starts.
func waitForRefresh(t *testing.T, c *Catalog) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		idle := c.flight == nil
		c.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the background reload did not finish")
}

func ids(models []ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func TestCatalogListsInstalledRuntimesAndAlwaysOffersDefault(t *testing.T) {
	c := NewCatalog(testFuncs(t), time.Minute)
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude/default", "claude/opus[1m]", "claude/opus",
		"codex/default", "codex/gpt-6-astra",
		"antigravity/default", "antigravity/gemini-3.8-flash-high",
		"hermes/default", // its listing failed: the default model is still offered
	}
	got := ids(models)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	byID := map[string]ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if m := byID["claude/opus[1m]"]; m.Class != ChatOnly || m.Runtime != "claude" || m.Model != "opus[1m]" || m.Label != "Opus (1M context)" || len(m.Efforts) != 2 {
		t.Errorf("claude/opus[1m] = %+v", m)
	}
	if byID["codex/gpt-6-astra"].Class != Sandboxed || byID["antigravity/default"].Class != Unconfined {
		t.Errorf("classes: codex %v, antigravity %v", byID["codex/gpt-6-astra"].Class, byID["antigravity/default"].Class)
	}
	if !byID["claude/opus"].Alias || byID["claude/opus[1m]"].Alias {
		t.Errorf("alias flags wrong: %+v / %+v", byID["claude/opus"], byID["claude/opus[1m]"])
	}
}

func TestCatalogFetchesRuntimeListsInParallel(t *testing.T) {
	f := testFuncs(t)
	var started atomic.Int32
	f.Models = func(ctx context.Context, runtime, _ string) ([]monomind.RuntimeModel, error) {
		started.Add(1)
		deadline := time.After(3 * time.Second)
		for started.Load() < 4 { // the four installed runtimes must all be in flight at once
			select {
			case <-deadline:
				return nil, errors.New("listings are running one after another")
			case <-time.After(time.Millisecond):
			}
		}
		return []monomind.RuntimeModel{{ID: "m-" + runtime}}, nil
	}
	c := NewCatalog(f, time.Minute)
	begin := time.Now()
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("listing took %v: not parallel", took)
	}
	for _, m := range models {
		if m.Model == "m-claude" {
			return
		}
	}
	t.Fatalf("a runtime's own listing was dropped: %v", ids(models))
}

func TestCatalogCachesAndCoalescesConcurrentLoads(t *testing.T) {
	f := testFuncs(t)
	var scans atomic.Int32
	scan := f.Scan
	f.Scan = func(ctx context.Context) (*monomind.ScanResult, error) {
		scans.Add(1)
		time.Sleep(50 * time.Millisecond)
		return scan(ctx)
	}
	c := NewCatalog(f, time.Minute)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Models(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := scans.Load(); got != 1 {
		t.Fatalf("ten concurrent callers triggered %d scans, want 1", got)
	}

	if _, err := c.Models(context.Background()); err != nil || scans.Load() != 1 {
		t.Fatalf("a call within the TTL must be served from the cache (scans=%d, err=%v)", scans.Load(), err)
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := c.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForRefresh(t, c)
	if scans.Load() != 2 {
		t.Fatalf("a call after the TTL must reload, in the background (scans=%d)", scans.Load())
	}
}

func TestCatalogMarksModelsTheRosterValidated(t *testing.T) {
	f := testFuncs(t)
	f.Roster = func(context.Context) ([]agentroster.Result, error) {
		return []agentroster.Result{
			{Runtime: "claude", Model: "opus[1m]", Status: agentroster.StatusOK, ValidatedAt: time.Now()},
			{Runtime: "codex", Model: "gpt-6-astra", Status: agentroster.StatusAuth, ValidatedAt: time.Now()},
		}, nil
	}
	models, err := NewCatalog(f, time.Minute).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validated := map[string]bool{}
	for _, m := range models {
		validated[m.ID] = m.Validated
	}
	if !validated["claude/opus[1m]"] || validated["codex/gpt-6-astra"] || validated["claude/default"] {
		t.Errorf("validated flags: %v", validated)
	}
}

// The fallback of the auto model prefers the cheapest and fastest validated
// model, so the catalog carries what each validation measured, and nothing for a
// model whose validation did not pass.
func TestCatalogCarriesTheValidationStatsOfValidatedModels(t *testing.T) {
	f := testFuncs(t)
	f.Roster = func(context.Context) ([]agentroster.Result, error) {
		return []agentroster.Result{
			{Runtime: "claude", Model: "opus[1m]", Status: agentroster.StatusOK, CostUSD: 0.0123, HasCost: true, LatencyMs: 2400, ValidatedAt: time.Now()},
			{Runtime: "claude", Model: "default", Status: agentroster.StatusOK, LatencyMs: 900, ValidatedAt: time.Now()}, // no cost reported
			{Runtime: "codex", Model: "gpt-6-astra", Status: agentroster.StatusAuth, CostUSD: 5, HasCost: true, LatencyMs: 1, ValidatedAt: time.Now()},
		}, nil
	}
	models, err := NewCatalog(f, time.Minute).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ModelInfo{}
	for _, m := range models {
		by[m.ID] = m
	}
	if m := by["claude/opus[1m]"]; !m.Validated || !m.HasCost || m.CostUSD != 0.0123 || m.LatencyMs != 2400 {
		t.Errorf("a validated model carries what the validation measured: %+v", m)
	}
	if m := by["claude/default"]; !m.Validated || m.HasCost || m.CostUSD != 0 || m.LatencyMs != 900 {
		t.Errorf("a validation that reported no cost has none: %+v", m)
	}
	if m := by["codex/gpt-6-astra"]; m.Validated || m.HasCost || m.CostUSD != 0 || m.LatencyMs != 0 {
		t.Errorf("a model whose validation failed has no stats: %+v", m)
	}
}

func TestCatalogResolve(t *testing.T) {
	c := NewCatalog(testFuncs(t), time.Minute)
	ctx := context.Background()

	for name, want := range map[string]string{
		"claude/opus[1m]":                   "claude/opus[1m]",
		"claude/default":                    "claude/default",
		"claude":                            "claude/default", // a bare runtime means its default model
		"codex":                             "codex/default",
		"agy/gemini-3.8-flash-high":         "antigravity/gemini-3.8-flash-high",
		"agy":                               "antigravity/default",
		"antigravity/gemini-3.8-flash-high": "antigravity/gemini-3.8-flash-high",
		"Claude/default":                    "claude/default", // runtime ids are case-insensitive
		"claude/opus":                       "claude/opus",    // an alias id still resolves
	} {
		got, err := c.Resolve(ctx, name)
		if err != nil || got.ID != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", name, got.ID, err, want)
		}
	}

	// Everything else is "not found", including every shape that could be
	// mistaken for a command line flag by the runner.
	for _, bad := range []string{
		"", "/", "claude/", "/default", "nope/default", "claude/does-not-exist",
		"-x/default", "claude/--help", "claude/--model x", "claude/opus[1m] --x",
		"claude/default\n", "claude /default", "claude/$(id)", "claude/;ls", "grok/default", "auto",
		string(make([]byte, 300)),
	} {
		if got, err := c.Resolve(ctx, bad); !errors.Is(err, ErrUnknownModel) {
			t.Errorf("Resolve(%q) = %+v, %v; want ErrUnknownModel", bad, got, err)
		}
	}
}

// A client that still names a superseded model gets the one that replaced it,
// whether that is listed by its id or only as its family's alias.
func TestCatalogResolvesASupersededModelToItsReplacement(t *testing.T) {
	for _, listed := range []string{"claude-sonnet-5-5", "sonnet"} {
		f := testFuncs(t)
		f.Roster = func(context.Context) ([]agentroster.Result, error) {
			return []agentroster.Result{{Runtime: "claude", Model: listed, Status: agentroster.StatusOK, ValidatedAt: time.Now()}}, nil
		}
		got, err := NewCatalog(f, time.Minute).Resolve(context.Background(), "claude/claude-sonnet-4-6")
		if err != nil || monomind.ModelFamily("claude", got.Model) != "sonnet" {
			t.Errorf("listed as %s: Resolve = %q, %v", listed, got.ID, err)
		}
	}
}

func TestCatalogVisibleAppliesThePolicyAndHidesAliases(t *testing.T) {
	c := NewCatalog(testFuncs(t), time.Minute)
	ctx := context.Background()

	chat, _ := c.Visible(ctx, Policy{Max: ChatOnly})
	for _, m := range chat {
		if m.Runtime != "claude" || m.Alias {
			t.Errorf("chat-only policy listed %s (alias=%v)", m.ID, m.Alias)
		}
	}
	if len(chat) != 2 { // claude/default and claude/opus[1m]; claude/opus is an alias
		t.Errorf("chat-only listed %v", ids(chat))
	}
	sand, _ := c.Visible(ctx, Policy{Max: Sandboxed})
	if len(sand) != 4 { // + codex/default and codex/gpt-6-astra
		t.Errorf("sandboxed listed %v", ids(sand))
	}
	all, _ := c.Visible(ctx, Policy{Max: Unconfined})
	if len(all) != 7 { // every non-alias model
		t.Errorf("any listed %v", ids(all))
	}
}

func TestCatalogOffersRosterModelsTheRuntimeDoesNotList(t *testing.T) {
	f := testFuncs(t)
	f.Roster = func(context.Context) ([]agentroster.Result, error) {
		return []agentroster.Result{
			{Runtime: "codex", Model: "my-custom-model", Label: "my-custom-model", Status: agentroster.StatusUntested, Source: agentroster.SourceManual},
			{Runtime: "grok", Model: "grok-9", Status: agentroster.StatusOK},              // not installed: ignored
			{Runtime: "codex", Model: "--dangerously-skip", Status: agentroster.StatusOK}, // not a model id: ignored
		}, nil
	}
	c := NewCatalog(f, time.Minute)
	got, err := c.Resolve(context.Background(), "codex/my-custom-model")
	if err != nil || got.Class != Sandboxed {
		t.Fatalf("a roster model of an installed runtime must resolve with its runtime's class: %+v, %v", got, err)
	}
	for _, name := range []string{"grok/grok-9", "codex/--dangerously-skip"} {
		if _, err := c.Resolve(context.Background(), name); !errors.Is(err, ErrUnknownModel) {
			t.Errorf("%s must stay unknown, got %v", name, err)
		}
	}
}

func TestCatalogCachesAnEmptyList(t *testing.T) {
	var scans atomic.Int32
	f := testFuncs(t)
	f.Scan = func(context.Context) (*monomind.ScanResult, error) {
		scans.Add(1)
		return &monomind.ScanResult{}, nil // no runtime installed
	}
	c := NewCatalog(f, time.Minute)
	for range 3 {
		if models, err := c.Models(context.Background()); err != nil || len(models) != 0 {
			t.Fatalf("models = %v, %v", ids(models), err)
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("an empty list is a result too: %d scans for three calls, want 1", scans.Load())
	}
}
