package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// The operator's host-wide pursuit settings. The loop is ON by default (nothing is built until a judge pin exists, so the
// default spends nothing); these two reserved rows (like MissionChildrenConfig: `config set` and agents cannot write them,
// only the operator-only `coder host pursuit` verbs) are the only ways to change that:
//
//	coder.pursuit_loop   absent or empty = the default (on); "on", "1", "true" (any case) = on; ANY other value is an explicit OFF
//	                     (so a hand-written "false", "OFF", "no" or "disabled" fails closed, never open)
//	coder.pursuit_judge  the saved judge pin, runtime/model[@version]; absent or empty = none (the loop idles: "judge-pin" missing)
//
// Both are read once, when the host starts. A read error fails closed in the host wiring (the loop stays off).
const (
	PursuitLoopConfig  = "coder.pursuit_loop"
	PursuitJudgeConfig = "coder.pursuit_judge"

	// PursuitPauseCommand pauses one mission's pursuit (an existing operator verb that takes the mission's session id);
	// PursuitPauseUsage is how the first-run notice shows it.
	PursuitPauseCommand = "task session pursuit pause"
	PursuitPauseUsage   = PursuitPauseCommand + " <session-id>"
	// PursuitMissingJudgePin is the closed code host.json carries while no judge pin is configured.
	PursuitMissingJudgePin = "judge-pin"
)

func (s *Store) configValue(name string) (string, error) {
	var raw string
	err := s.db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(raw), err
}

// setConfigValue upserts the row; an empty value is the cleared state (it reads as absent).
func (s *Store) setConfigValue(name, v string) error {
	_, err := s.db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`, name, v)
	return err
}

// PursuitOperatorOff says whether the operator turned the loop off (false when never set: the default is on).
func (s *Store) PursuitOperatorOff() (bool, error) {
	v, err := s.configValue(PursuitLoopConfig)
	return err == nil && PursuitLoopValueOff(v), err
}

// PursuitLoopValueOff reads a spelling of the switch (the env var or the saved row): empty or on/1/true (any case, trimmed) is
// the default, ON; any other non-empty value is an explicit OFF. An unrecognised spelling must never leave the loop on.
func PursuitLoopValueOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "on", "1", "true":
		return false
	}
	return true
}

// PendingPursuitRestart says whether the saved settings differ from what the running host started with, so that a restart
// would change its loop. The env var is the host's own and not visible here: a host whose pin or OFF came from it is not compared.
func PendingPursuitRestart(savedOff bool, savedPin string, h PursuitHostInfo) bool {
	if h.OperatorOff && h.OffSource == "env" {
		return false // the host's own env OFF wins over anything saved
	}
	if savedOff != (h.OperatorOff && h.OffSource == "saved") {
		return true
	}
	if savedOff || h.PinSource == "env" {
		return false
	}
	return savedPin != h.Pin
}

// SetPursuitOperatorOff writes the operator's explicit OFF, or clears it (back to the default).
func (s *Store) SetPursuitOperatorOff(off bool) error {
	if off {
		return s.setConfigValue(PursuitLoopConfig, "off")
	}
	return s.setConfigValue(PursuitLoopConfig, "")
}

// SavedPursuitJudge is the judge pin the operator saved ("" when none).
func (s *Store) SavedPursuitJudge() (string, error) { return s.configValue(PursuitJudgeConfig) }

// SetSavedPursuitJudge saves a judge pin ("" clears it). A pin that is not runtime/model[@version] is refused.
func (s *Store) SetSavedPursuitJudge(pin string) error {
	pin = strings.TrimSpace(pin)
	if pin != "" && !ValidPursuitPin(pin) {
		return fmt.Errorf("%q is not runtime/model[@version]", pin)
	}
	return s.setConfigValue(PursuitJudgeConfig, pin)
}

// ValidPursuitPin is the shape rule of the judge pin (the same as judgemodel.New's).
func ValidPursuitPin(pin string) bool {
	rt, md, ok := strings.Cut(pin, "/")
	name, _, _ := strings.Cut(md, "@")
	return ok && rt != "" && name != "" && !strings.ContainsAny(pin, " \t\r\n")
}

// PursuitNotice is the first-run notice of a mission chat: what a pursuit does, what it costs at most and how to pause. Every
// figure comes from the constants of the caps; the app shows this text and never writes its own.
//
// wakeCap is the cap the running host published (MONOAGENT_PURSUIT_WAKE_CAP_USD can raise it); 0 (no host state) shows the default.
func PursuitNotice(wakeCap float64) string {
	if wakeCap <= 0 {
		wakeCap = DefaultWakeCapUSD
	}
	return fmt.Sprintf("This mission will pursue its goal while you are away, but only after you accept its goal and confirm. Caps: $%.2f a wake-up turn (default $2; up to $10 if the host was started with a larger cap), %d%% of the mission budget, %d wake-ups and %d parked questions until you next act; this host at most %d wake-ups and %d judge calls an hour. Pause any time with `%s`; closing the mission ends it.",
		wakeCap, missionpursuit.DefaultPct, missionpursuit.DefaultWakesMax, missionpursuit.DefaultParkedMax, MaxPursuitWakesPerHour, MaxPursuitJudgeCallsPerHour, PursuitPauseUsage)
}
