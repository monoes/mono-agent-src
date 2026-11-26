package main

// TypeSafe Jev settings (Settings page → "TypeSafe Jev"). Every method
// shells out to `monoagentcli jev …` through runMonoCLI; this file never
// touches the vault, the settings table or TypeSafe's API. The key only
// travels on the CLI's stdin — never argv, never a log line.

import (
	"errors"
	"strconv"
	"strings"
)

// JevSurface is one implicit Jev integration point (e.g. "hil").
type JevSurface struct {
	Surface          string   `json:"surface"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Egress           []string `json:"egress"`
	Enabled          bool     `json:"enabled"`
	Threshold        float64  `json:"threshold"`
	DefaultThreshold float64  `json:"default_threshold"`
	// Provider is the surface's own provider choice for the profile (it may
	// name the default); "" when it follows the default. ProviderPinned is
	// true when it has a choice of its own (a CLI before pinning leaves it out).
	Provider       string `json:"provider"`
	ProviderPinned bool   `json:"provider_pinned,omitempty"`
	// SampledLowers is true when the surface lowers a threshold a sampling
	// provider cannot read (the CLI's list; a CLI without it leaves it out).
	SampledLowers bool `json:"sampled_lowers_threshold,omitempty"`
}

// JevStatusInfo mirrors `jev status --json`: where the key comes from
// (never the key itself), the model, and each surface's state.
type JevStatusInfo struct {
	ProfileID string       `json:"profile_id"`
	KeySource string       `json:"key_source"` // config | vault | profile-vault | env | none
	KeyEntry  string       `json:"key_entry"`  // vault entry name when key_source is vault
	Model     string       `json:"model"`
	BaseURL   string       `json:"base_url"`
	Surfaces  []JevSurface `json:"surfaces"`
	// The machine-default decision provider, its kind and host, and how it
	// obtains probabilities (native | logprob | softmax | sampled | none).
	Provider     string `json:"provider"`
	ProviderKind string `json:"provider_kind"`
	ProviderHost string `json:"provider_host"`
	Probability  string `json:"probability"`
	// For a default other than TypeSafe: where its key comes from (vault |
	// keyless) and why it cannot be used ("" when it can).
	ProviderKeySource string `json:"provider_key_source,omitempty"`
	ProviderError     string `json:"provider_error,omitempty"`
}

// JevKeySetResult mirrors `jev key set --json`.
type JevKeySetResult struct {
	KeySource string `json:"key_source"`
	KeyEntry  string `json:"key_entry"`
	Replaced  bool   `json:"replaced"`
}

// JevKeyTestResult mirrors `jev key test --json` (exit 0 even when ok=false).
type JevKeyTestResult struct {
	OK        bool     `json:"ok"`
	KeySource string   `json:"key_source"`
	Models    []string `json:"models"`
	Error     string   `json:"error"`
}

// JevKeyRemoveResult mirrors `jev key remove --json`.
type JevKeyRemoveResult struct {
	Removed string `json:"removed"`
}

// JevSurfaceResult mirrors `jev enable|disable <surface> --json`.
type JevSurfaceResult struct {
	ProfileID string   `json:"profile_id"`
	Surface   string   `json:"surface"`
	Enabled   bool     `json:"enabled"`
	Threshold float64  `json:"threshold"`
	Egress    []string `json:"egress"`
}

// JevUsageRow is one surface's aggregate in `jev usage --json`.
type JevUsageRow struct {
	Surface      string  `json:"surface"`
	Provider     string  `json:"provider"`
	Calls        int     `json:"calls"`
	Failures     int     `json:"failures"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	EstimatedUSD float64 `json:"estimated_usd"`
	AvgLatencyMS int     `json:"avg_latency_ms"`
}

// JevUsageReport mirrors `jev usage --since <d> --json`.
type JevUsageReport struct {
	ProfileID string        `json:"profile_id"`
	Since     string        `json:"since"`
	Surfaces  []JevUsageRow `json:"surfaces"`
	Total     JevUsageRow   `json:"total"`
}

// jevArg refuses empty values and anything that would parse as a flag.
func jevArg(what, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New(what + " is required")
	}
	if strings.HasPrefix(v, "-") {
		return "", errors.New("invalid " + what + " " + strconv.Quote(v))
	}
	return v, nil
}

// JevStatus returns the key source, model and every surface's state.
func (a *App) JevStatus() (JevStatusInfo, error) {
	if err := a.signedOut(); err != nil {
		return JevStatusInfo{}, err
	}
	var st JevStatusInfo
	if err := a.runMonoCLI("", &st, "jev", "status"); err != nil {
		return JevStatusInfo{}, err
	}
	if st.Surfaces == nil {
		st.Surfaces = []JevSurface{}
	}
	return st, nil
}

// JevSetKey stores the TypeSafe key in the machine-wide vault. The key is
// passed only on stdin.
func (a *App) JevSetKey(key string) (JevKeySetResult, error) {
	if err := a.signedOut(); err != nil {
		return JevKeySetResult{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return JevKeySetResult{}, errors.New("paste a TypeSafe API key first")
	}
	var res JevKeySetResult
	if err := a.runMonoCLI(key, &res, "jev", "key", "set"); err != nil {
		return JevKeySetResult{}, err
	}
	return res, nil
}

// JevTestKey asks TypeSafe (via the CLI) which models the key can use.
func (a *App) JevTestKey() (JevKeyTestResult, error) {
	if err := a.signedOut(); err != nil {
		return JevKeyTestResult{}, err
	}
	var res JevKeyTestResult
	if err := a.runMonoCLI("", &res, "jev", "key", "test"); err != nil {
		return JevKeyTestResult{}, err
	}
	if res.Models == nil {
		res.Models = []string{}
	}
	return res, nil
}

// JevRemoveKey deletes the key's vault entry.
func (a *App) JevRemoveKey() (JevKeyRemoveResult, error) {
	if err := a.signedOut(); err != nil {
		return JevKeyRemoveResult{}, err
	}
	var res JevKeyRemoveResult
	if err := a.runMonoCLI("", &res, "jev", "key", "remove"); err != nil {
		return JevKeyRemoveResult{}, err
	}
	return res, nil
}

// JevSetSurface switches one surface on or off. Enabling passes --yes: the
// GUI shows the surface's egress list and asks before calling this, which is
// the same consent `jev enable` asks for in a terminal. threshold > 0 sets
// the gate (a threshold-only change is an enable with the new value);
// threshold <= 0 keeps the current one.
func (a *App) JevSetSurface(surface string, enabled bool, threshold float64) (JevSurfaceResult, error) {
	if err := a.signedOut(); err != nil {
		return JevSurfaceResult{}, err
	}
	s, err := jevArg("surface", surface)
	if err != nil {
		return JevSurfaceResult{}, err
	}
	args := []string{"jev", "disable", s}
	if enabled {
		if threshold > 1 {
			return JevSurfaceResult{}, errors.New("threshold must be between 0 and 1")
		}
		args = []string{"jev", "enable", s, "--yes"}
		if threshold > 0 {
			args = append(args, "--threshold", strconv.FormatFloat(threshold, 'f', -1, 64))
		}
	}
	var res JevSurfaceResult
	if err := a.runMonoCLI("", &res, args...); err != nil {
		return JevSurfaceResult{}, err
	}
	return res, nil
}

// JevUsage reports calls, tokens and estimated cost per surface since
// `since` ago (e.g. "7d", "24h"; default 7d).
func (a *App) JevUsage(since string) (JevUsageReport, error) {
	if err := a.signedOut(); err != nil {
		return JevUsageReport{}, err
	}
	if strings.TrimSpace(since) == "" {
		since = "7d"
	}
	s, err := jevArg("since", since)
	if err != nil {
		return JevUsageReport{}, err
	}
	var rep JevUsageReport
	if err := a.runMonoCLI("", &rep, "jev", "usage", "--since", s); err != nil {
		return JevUsageReport{}, err
	}
	if rep.Surfaces == nil {
		rep.Surfaces = []JevUsageRow{}
	}
	return rep, nil
}
