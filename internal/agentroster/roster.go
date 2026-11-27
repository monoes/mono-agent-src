package agentroster

import (
	"slices"
	"sort"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// Roster states.
const (
	StateReady    = "ready"    // answered recently on the current runtime version
	StateStale    = "stale"    // answered, but too long ago or on another version
	StateFailed   = "failed"   // the last test or a real turn failed
	StateUntested = "untested" // added by hand, never tested
)

// DefaultMaxAge is how long a passing result counts as ready.
const DefaultMaxAge = 7 * 24 * time.Hour

// Entry is one roster model with its computed state.
type Entry struct {
	Result
	State       string `json:"state"`
	StaleReason string `json:"stale_reason,omitempty"` // age|version|rate_limited
	// TrackRecord is the model's success rate per role category from real
	// dynamic-org workers (AttachQuality); empty until it has run one.
	TrackRecord []Rate `json:"track_record,omitempty"`
}

// RuntimeRoster is one runtime and its models.
type RuntimeRoster struct {
	Runtime   string `json:"runtime"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	LoginHint string `json:"login_hint,omitempty"`
	// ExecutionSupported / ExecutionUnsupportedReason carry `agent scan`'s
	// fields: false when monomind cannot run the runtime, with its reason.
	ExecutionSupported         *bool   `json:"execution_supported,omitempty"`
	ExecutionUnsupportedReason string  `json:"execution_unsupported_reason,omitempty"`
	Models                     []Entry `json:"models"`
	Ready                      int     `json:"ready"`
}

// Replaced reports that a newer version of model's family stands in for it in
// results: that version, or claude's alias for the family, answered.
func Replaced(results []Result, runtime, model string) (newest string, ok bool) {
	newest, old := monomind.Superseded(runtime, model)
	if !old || runtime == "claude" && monomind.OtherClaudeProvider() {
		return "", false
	}
	family := monomind.ModelFamily(runtime, newest)
	return newest, slices.ContainsFunc(results, func(r Result) bool {
		return r.Runtime == runtime && (r.Model == newest || r.Model == family) && (Works(r.Status) || r.Status == StatusRateLimited)
	})
}

// Build groups results by runtime and computes each model's state. scan may
// be nil (no version or install checks); with a scan, every installed
// runtime is listed, even one never validated, and a runtime that is no
// longer installed has all its models failed.
func Build(results []Result, scan *monomind.ScanResult, now time.Time, maxAge time.Duration) []RuntimeRoster {
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	byRuntime := map[string]*RuntimeRoster{}
	var order []string
	get := func(id string) *RuntimeRoster {
		if rr, ok := byRuntime[id]; ok {
			return rr
		}
		rr := &RuntimeRoster{Runtime: id, Models: []Entry{}}
		byRuntime[id] = rr
		order = append(order, id)
		return rr
	}
	if scan != nil {
		for _, a := range scan.Installed() {
			rr := get(a.ID)
			rr.Installed = true
			if a.Version != nil {
				rr.Version = *a.Version
			}
			if a.LoginHint != nil {
				rr.LoginHint = *a.LoginHint
			}
		}
	}
	for _, r := range results {
		// A model whose replacement answers is not offered or staffed. Until
		// then the old row stays, and a turn on it runs the replacement.
		if _, replaced := Replaced(results, r.Runtime, r.Model); replaced {
			continue
		}
		rr := get(r.Runtime)
		if rr.LoginHint == "" && r.Status == StatusAuth {
			rr.LoginHint = r.LoginHint
		}
		e := Entry{Result: r}
		switch {
		case r.Status == StatusUntested:
			e.State = StateUntested
		case r.Status == StatusRateLimited:
			// Transient: keep it staffable and have it re-checked.
			e.State, e.StaleReason = StateStale, StatusRateLimited
		case !Works(r.Status):
			e.State = StateFailed
		case scan != nil && !rr.Installed:
			e.State, e.Detail = StateFailed, "runtime is not installed"
		case now.Sub(r.ValidatedAt) > maxAge:
			e.State, e.StaleReason = StateStale, "age"
		case scan != nil && rr.Version != "" && r.RuntimeVersion != "" && rr.Version != r.RuntimeVersion:
			e.State, e.StaleReason = StateStale, "version"
		default:
			e.State = StateReady
			rr.Ready++
		}
		rr.Models = append(rr.Models, e)
	}
	out := make([]RuntimeRoster, 0, len(order))
	for _, id := range order {
		if scan != nil {
			if e := scan.Find(id); e != nil && e.ExecutionSupported != nil {
				byRuntime[id].ExecutionSupported = e.ExecutionSupported
				if reason, ok := e.UnsupportedReason(); ok {
					byRuntime[id].ExecutionUnsupportedReason = reason
				}
			}
		}
		out = append(out, *byRuntime[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Runtime < out[j].Runtime })
	return out
}
