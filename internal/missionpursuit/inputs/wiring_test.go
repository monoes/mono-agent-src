package inputs

import (
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

// The strategy store's own Running/RunOf say running for ANY session in state 'running': they know nothing of the pursuit.
// Wiring the engine or the intake to them instead of this package's Reader would run a mission with no pursuit, an unstarted
// one, or a paused one. This table names the difference, so a swap of the RunState is caught by name here.
func TestTheStrategyStoresOwnRunStateIsNotThePursuitRunState(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f fixture)
		want  bool // what the inputs Reader says; the strategy store says true in every row
	}{
		{"no pursuit row", func(*testing.T, fixture) {}, false},
		{"unstarted row", func(t *testing.T, f fixture) {
			mustExec(t, f.db, `INSERT INTO mission_pursuit (session_id, enabled, created_at, updated_at) VALUES (?, 0, 'x', 'x')`, f.session)
		}, false},
		{"paused", func(t *testing.T, f fixture) { f.startPursuit(t); f.pause(t) }, false},
		{"started and running", func(t *testing.T, f fixture) { f.startPursuit(t) }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(t, f)
			st := strategy.NewStore(f.db)
			if ok, err := st.Running(bg, itoa(f.session), 1); err != nil || !ok {
				t.Fatalf("premise: the strategy store's Running = %v, %v; it is expected to say true for a running session", ok, err)
			}
			if ok, err := st.RunOf(f.session).Running(bg); err != nil || !ok {
				t.Fatalf("premise: the strategy store's RunOf = %v, %v", ok, err)
			}
			if ok, err := f.reader.Running(bg, itoa(f.session), 1); err != nil || ok != c.want {
				t.Errorf("inputs Reader.Running = %v, %v; want %v", ok, err, c.want)
			}
			if ok, err := f.reader.RunOf(f.session).Running(bg); err != nil || ok != c.want {
				t.Errorf("inputs Reader.RunOf = %v, %v; want %v", ok, err, c.want)
			}
		})
	}
}
