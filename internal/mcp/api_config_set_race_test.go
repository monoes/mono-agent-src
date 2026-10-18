package mcp

// Two api_config_set calls in flight at once, which a host does when it sends two in a turn (each runs
// on a goroutine of its own), must both land: each saves what it was given, and neither undoes the
// other. The saved settings are one row, so a call that read the row and wrote it back whole from
// what it had read would lose the other's change. apiconfig.Apply is one BEGIN IMMEDIATE
// transaction, and these tests are about the tool going through it.

import (
	"context"
	"testing"
	"time"
)

// raceSettle gives two calls that were sent together time to start and to queue
// for the write lock. A slower server only lets a test pass, never fail.
const raceSettle = 300 * time.Millisecond

// Both calls are sent while another connection holds the database's write lock, so that both have
// started before either can write, every time (raceSettle gives them time to queue for it). The
// database waits for a lock for 5 seconds, far more than that.
func TestTwoAPIConfigSetCallsInFlightBothLand(t *testing.T) {
	f := newConfigFixture(t, configSetup{})
	// Both settings have a value before, so that a call that wrote back what it had read would show
	// as the other's change undone, and not only as a setting that is missing.
	f.save("max_concurrent=2", "turn_timeout=11m")
	w := newWireSession(t, f.Server)
	w.call("api_config_get", nil) // the server is up before the lock is taken

	tx, err := f.Side.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('race-lock', '1') ON CONFLICT(key) DO UPDATE SET value = '2'`); err != nil {
		t.Fatal(err)
	}
	a := w.send("api_config_set", set(map[string]any{"max_concurrent": "8"}))
	b := w.send("api_config_set", set(map[string]any{"turn_timeout": "20m"}))
	time.Sleep(raceSettle)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		if text, isErr := w.await(id); isErr {
			t.Fatalf("a change of one setting failed: %s", text)
		}
	}
	w.finish()

	if s := f.saved(); s.MaxConcurrent != "8" || s.TurnTimeout != "20m" {
		t.Errorf("both calls must land: max_concurrent %q, turn_timeout %q", s.MaxConcurrent, s.TurnTimeout)
	}
}

// The same without the lock, over pairs of settings that the other pairs do not touch, one pair
// after another: for every pair of calls at once, both settings end as their call said. Each has a
// value before that its call replaces (and that the gate has nothing to say about: the CLI saved
// them). The TLS files are a pair that one call cannot save by halves, so they are not here.
func TestManyAPIConfigSetCallsAtOnceAllLand(t *testing.T) {
	f := newConfigFixture(t, configSetup{})
	w := newWireSession(t, f.Server)
	type call struct{ key, before, after string }
	pairs := [][2]call{
		{{"max_concurrent", "2", "8"}, {"turn_timeout", "11m", "20m"}},
		{{"confinement", "any", "chat-only"}, {"context_confinement", "sandboxed", "chat-only"}},
		{{"auto_confinement", "sandboxed", "chat-only"}, {"image_runtimes", "codex,antigravity", "codex"}}, // a narrowing: swapping for a runtime that was not there is a widening (P18)
		{{"tool_runtimes", "claude,codex", "claude"}, {"v1_addr", "127.0.0.1:9000", "127.0.0.1:9443"}},
	}
	for round := 0; round < 5; round++ {
		for _, p := range pairs {
			f.clear()
			f.save(p[0].key+"="+p[0].before, p[1].key+"="+p[1].before)
			ids := []string{
				w.send("api_config_set", set(map[string]any{p[0].key: p[0].after})),
				w.send("api_config_set", set(map[string]any{p[1].key: p[1].after})),
			}
			for _, id := range ids {
				w.mustSucceed(w.await(id))
			}
			got := f.saved()
			for _, c := range p {
				if got.Get(c.key) != c.after {
					t.Fatalf("round %d: %s is %q, want %q: the call that set it was undone by the other", round, c.key, got.Get(c.key), c.after)
				}
			}
		}
	}
	w.finish()
}

// mustSucceed fails the test when an answer is an error.
func (w *wireSession) mustSucceed(text string, isErr bool) {
	w.t.Helper()
	if isErr {
		w.t.Fatalf("a call failed: %s", scrubbed(text))
	}
}
