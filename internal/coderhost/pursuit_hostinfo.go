package coderhost

import (
	"encoding/json"
	"os"
	"path/filepath"
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
