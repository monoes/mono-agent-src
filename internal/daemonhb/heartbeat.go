// Package daemonhb is the liveness contract of `monoagentcli daemon`
// (plan §7.6): the daemon rewrites ~/.monoagent/daemon-heartbeat.json every
// Interval, and a reader treats it as live only when the timestamp is
// younger than Stale AND the pid still runs — the same rule monomind applies
// to its serve heartbeat, so a crashed daemon never looks alive.
package daemonhb

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	Interval = 10 * time.Second
	Stale    = 30 * time.Second
)

// Heartbeat is the file's content.
type Heartbeat struct {
	V          int       `json:"v"`
	PID        int       `json:"pid"`
	TS         time.Time `json:"ts"`
	APIAddr    string    `json:"api_addr,omitempty"`    // "" when the API is off
	BridgeAddr string    `json:"bridge_addr,omitempty"` // "" when the extension bridge is off
	V1Addr     string    `json:"v1_addr,omitempty"`     // "" without a dedicated OpenAI-compatible API listener
	// APIConfinement and V1Confinement are the confinement policies the daemon
	// really applies on the OpenAI-compatible API of its HTTP API listener and
	// of its dedicated listener ("" where that listener does not serve it).
	// ContextConfinement is the strongest class a key created with --context
	// may use, and AutoConfinement the strongest the auto model may pick.
	// `api status` cannot work any of them out from its own environment.
	APIConfinement     string `json:"api_confinement,omitempty"`
	V1Confinement      string `json:"v1_confinement,omitempty"`
	ContextConfinement string `json:"context_confinement,omitempty"`
	AutoConfinement    string `json:"auto_confinement,omitempty"`
	// APISettings is, for each setting of the OpenAI-compatible API's server (by its key:
	// v1_addr, confinement, max_concurrent, ...), the value the daemon started with and where
	// it came from, so that a reader can tell what a restart would change. A daemon that
	// predates it has none.
	APISettings map[string]APISetting `json:"api_settings,omitempty"`
	Version     string                `json:"version,omitempty"`
	// Schedules are the registered schedule triggers with the scheduler's
	// own next fire time, refreshed on every write.
	Schedules []Schedule `json:"schedules,omitempty"`
	// Account is the monoes.me account verdict, refreshed on every write. It is
	// absent while the account gate is dormant, and in a daemon that predates it.
	Account *AccountState `json:"account,omitempty"`
	// SchedulerTick is when the scheduler loop last ran (NoteSchedulerTick),
	// refreshed on every write. The heartbeat ticker is independent of the
	// scheduler, so only this field tells a wedged scheduler from a healthy
	// one. Zero when the scheduler never reported (an older daemon).
	SchedulerTick time.Time `json:"scheduler_tick,omitzero"`
}

var lastSchedulerTick atomic.Int64 // unix nanoseconds

// NoteSchedulerTick records that the scheduler loop is alive now. The
// scheduler calls it on every pass; the next heartbeat carries the time.
func NoteSchedulerTick() { lastSchedulerTick.Store(time.Now().UnixNano()) }

func schedulerTick() time.Time {
	n := lastSchedulerTick.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// AccountState is the account verdict a heartbeat carries: ok, grace or locked,
// and why. It repeats the fields of internal/account's Status that a reader of
// this file needs, so that this package depends on nothing else of the repository.
type AccountState struct {
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	ValidUntil time.Time `json:"valid_until,omitzero"`
	// Enforced is false in the warn period: the state can say locked while
	// nothing is refused yet.
	Enforced bool `json:"enforced"`
}

// APISetting is one effective setting of the OpenAI-compatible API's server.
type APISetting struct {
	// Value is the canonical text of the effective value: "" where the setting has no value
	// (no dedicated listener, a policy that follows the listener, no certificate).
	Value string `json:"value"`
	// Source is where it came from: flag, env, saved or default.
	Source string `json:"source"`
}

// Schedule is one registered schedule trigger.
type Schedule struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	NextRun    string `json:"next_run"` // RFC3339 UTC
}

// Path returns the heartbeat file path. MONOAGENT_DAEMON_HEARTBEAT
// overrides it (tests, and running two daemons side by side).
func Path() string {
	if p := os.Getenv("MONOAGENT_DAEMON_HEARTBEAT"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".monoagent", "daemon-heartbeat.json")
}

// Write atomically replaces the heartbeat file.
func Write(hb Heartbeat) error {
	hb.V = 1
	if hb.TS.IsZero() {
		hb.TS = time.Now()
	}
	b, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read returns the heartbeat and whether it is live.
func Read() (Heartbeat, bool) {
	var hb Heartbeat
	b, err := os.ReadFile(Path())
	if err != nil {
		return hb, false
	}
	if err := json.Unmarshal(b, &hb); err != nil {
		return hb, false
	}
	return hb, IsLive(hb, time.Now())
}

// IsLive applies the freshness + pid rule.
func IsLive(hb Heartbeat, now time.Time) bool {
	if hb.PID <= 0 || hb.TS.IsZero() {
		return false
	}
	age := now.Sub(hb.TS)
	if age < -Stale || age > Stale {
		return false
	}
	return ProcessAlive(hb.PID)
}

// RunWith writes a heartbeat now and every Interval until ctx ends, then
// removes the file if it still names this process. refresh, when not nil, is
// called before every write, to update fields that change while the daemon
// runs (e.g. Schedules).
func RunWith(ctx context.Context, hb Heartbeat, refresh func(*Heartbeat)) {
	hb.PID = os.Getpid()
	write := func() {
		if refresh != nil {
			refresh(&hb)
		}
		if t := schedulerTick(); !t.IsZero() {
			hb.SchedulerTick = t
		}
		hb.TS = time.Now()
		_ = Write(hb)
	}
	write()
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if cur, _ := Read(); cur.PID == hb.PID {
				_ = os.Remove(Path())
			}
			return
		case <-t.C:
			write()
		}
	}
}
