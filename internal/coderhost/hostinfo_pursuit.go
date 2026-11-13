package coderhost

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// What the host tells the status commands about the pursuit loop: the host flag, the judge pin and the per-wake cap. The
// host writes it into its info file (host.json) when it starts; `coder host status` and `task session pursuit status` read it.
// Pins and dollar figures only: no key, no token.

// HostInfoFile is the host's info file, in HostDir.
const HostInfoFile = "host.json"

// PursuitHostInfo is the switch-on state of one host. Enabled false is the default: the loop does not run, and the check-ins of
// a mission whose pursuit was started go on. Reason says in plain words why a host that was asked to run the loop does not.
type PursuitHostInfo struct {
	Enabled    bool    `json:"enabled"`
	Pin        string  `json:"pin,omitempty"`        // the judge's runtime/model
	WakeCapUSD float64 `json:"wakeCapUsd,omitempty"` // X: the most one wake-up turn of a lead may spend
	Reason     string  `json:"reason,omitempty"`

	// The loop is ON by default and idles until a judge pin exists. Missing is the closed code of what is missing
	// (PursuitMissingJudgePin); OperatorOff says the operator turned the loop off on purpose; PinSource is "env" or "saved".
	Missing     string `json:"missing,omitempty"`
	OperatorOff bool   `json:"operatorOff,omitempty"`
	OffSource   string `json:"offSource,omitempty"` // "env" or "saved", when OperatorOff
	PinSource   string `json:"pinSource,omitempty"`

	// The goal checks (MONOAGENT_PURSUIT_VERIFIER), display only like the rest of the file. VerifierPin is what was asked for
	// ("" = the checks are off, the default); VerifierOn says they are set up; VerifierReason says why a checker that was asked
	// for is not (no judge, a pin that does not parse, a scratch folder that cannot be made); Confinement says what confines the
	// programs of command criteria on this host, or what is missing.
	VerifierPin    string `json:"verifierPin,omitempty"`
	VerifierOn     bool   `json:"verifierOn,omitempty"`
	VerifierReason string `json:"verifierReason,omitempty"`
	Confinement    string `json:"confinement,omitempty"`
}

// HostDir is the folder of the coder host that serves the database at dbPath (already path-expanded).
func HostDir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "coder-host") }

// ReadHostPursuit reads the pursuit state the running host published for the database at dbPath. found is false when no host
// has published one (no host, or a host built before this field).
func ReadHostPursuit(dbPath string) (info PursuitHostInfo, found bool) {
	raw, err := os.ReadFile(filepath.Join(HostDir(dbPath), HostInfoFile))
	if err != nil {
		return PursuitHostInfo{}, false
	}
	var doc struct {
		Pursuit *PursuitHostInfo `json:"pursuit"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Pursuit == nil {
		return PursuitHostInfo{}, false
	}
	return *doc.Pursuit, true
}

// ReadHostPursuitState is ReadHostPursuit plus whether the host that wrote the file is alive now: a live host holds host.lock,
// so a lock that can be taken means the file is a leftover of a host that died. (The probe takes the lock for a moment, as
// `coder host status` does.) The file is display-only: it can make a status wrong, never make anything run.
func ReadHostPursuitState(dbPath string) (info PursuitHostInfo, found, alive bool) {
	info, found = ReadHostPursuit(dbPath)
	if !found {
		return info, false, false
	}
	release, err := daemonhb.LockFile(filepath.Join(HostDir(dbPath), "host.lock"))
	if err == nil {
		release()
		return info, true, false
	}
	return info, true, errors.Is(err, daemonhb.ErrHeld)
}
