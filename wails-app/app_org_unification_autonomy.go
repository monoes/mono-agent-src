package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func autonomyShowArgs(org string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	return []string{"autonomy", "show", org}, nil
}

// autonomySpec is SetOrgAutonomy's specJSON (contracts §5). Every field is
// optional so the GUI can change just the level, or just the decider,
// without resending the rest. ClearTiers is a GUI extension mapping to
// repeated --clear-tier.
type autonomySpec struct {
	Level   string `json:"level"`
	Decider *struct {
		Kind           string `json:"kind"`
		Model          string `json:"model"`
		Runtime        string `json:"runtime"`
		Fallback       string `json:"fallback"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
		// Threshold is the jev decider's gate, in (0,1] (--decider-threshold).
		Threshold *float64 `json:"threshold"`
	} `json:"decider"`
	Policy           *string           `json:"policy"`
	Tiers            map[string]string `json:"tiers"`
	ClearTiers       []string          `json:"clear_tiers"`
	OnDeciderFailure string            `json:"on_decider_failure"`
	Limits           *struct {
		MaxDecisionsPerRun  *int     `json:"max_decisions_per_run"`
		MaxDeciderUSDPerRun *float64 `json:"max_decider_usd_per_run"`
	} `json:"limits"`
}

func autonomySetArgs(org, specJSON string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	var s autonomySpec
	if err := json.Unmarshal([]byte(specJSON), &s); err != nil {
		return nil, fmt.Errorf("invalid autonomy spec: %w", err)
	}
	args := []string{"autonomy", "set", org, "--by", "gui"}
	if s.Level != "" {
		if !validLevels[s.Level] {
			return nil, fmt.Errorf("autonomy spec: level must be manual, mid, or full")
		}
		args = append(args, "--level", s.Level)
	}
	if d := s.Decider; d != nil {
		if d.Kind != "" {
			if !validDeciderKinds[d.Kind] {
				return nil, fmt.Errorf("autonomy spec: decider kind must be model, boss, parent, or jev")
			}
			args = append(args, "--decider", d.Kind)
		}
		if d.Runtime != "" {
			args = append(args, "--decider-runtime", d.Runtime)
		}
		if d.Model != "" {
			args = append(args, "--decider-model", d.Model)
		}
		if d.Fallback != "" {
			if d.Fallback != "model" {
				return nil, fmt.Errorf("autonomy spec: decider fallback must be model")
			}
			args = append(args, "--fallback", d.Fallback)
		}
		if d.TimeoutSeconds != nil {
			if *d.TimeoutSeconds <= 0 {
				return nil, fmt.Errorf("autonomy spec: decider timeout_seconds must be positive")
			}
			args = append(args, "--decider-timeout", strconv.Itoa(*d.TimeoutSeconds))
		}
		if d.Threshold != nil {
			if !(*d.Threshold > 0 && *d.Threshold <= 1) {
				return nil, fmt.Errorf("autonomy spec: decider threshold must be in (0,1]")
			}
			args = append(args, "--decider-threshold", strconv.FormatFloat(*d.Threshold, 'f', -1, 64))
		}
	}
	if s.Policy != nil {
		args = append(args, "--policy", *s.Policy)
	}
	// Sorted so the argument list is deterministic (map order is not).
	tierKeys := make([]string, 0, len(s.Tiers))
	for k := range s.Tiers {
		tierKeys = append(tierKeys, k)
	}
	sort.Strings(tierKeys)
	for _, k := range tierKeys {
		v := s.Tiers[k]
		if !validTiers[v] {
			return nil, fmt.Errorf("autonomy spec: tier for %q must be routine, consequential, or irreversible", k)
		}
		if strings.TrimSpace(k) == "" || strings.Contains(k, "=") {
			return nil, fmt.Errorf("autonomy spec: invalid decision class %q", k)
		}
		args = append(args, "--tier", k+"="+v)
	}
	for _, k := range s.ClearTiers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		args = append(args, "--clear-tier", k)
	}
	if s.OnDeciderFailure != "" {
		if !validFailureActions[s.OnDeciderFailure] {
			return nil, fmt.Errorf("autonomy spec: on_decider_failure must be deny or human")
		}
		args = append(args, "--on-decider-failure", s.OnDeciderFailure)
	}
	if l := s.Limits; l != nil {
		if l.MaxDecisionsPerRun != nil {
			args = append(args, "--max-decisions", strconv.Itoa(*l.MaxDecisionsPerRun))
		}
		if l.MaxDeciderUSDPerRun != nil {
			args = append(args, "--max-decider-usd", strconv.FormatFloat(*l.MaxDeciderUSDPerRun, 'f', -1, 64))
		}
	}
	if len(args) == 5 { // "autonomy", "set", org, "--by", "gui"
		return nil, fmt.Errorf("autonomy spec: nothing to change")
	}
	return args, nil
}

// autonomyPauseArgs: duration "" pauses until resumed; otherwise "30m", "2h", ….
func autonomyPauseArgs(org, duration string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	args := []string{"autonomy", "pause", org}
	if duration != "" {
		if !durationPattern.MatchString(duration) {
			return nil, fmt.Errorf("invalid pause duration %q: use minutes or hours, e.g. 30m or 2h", duration)
		}
		args = append(args, "--for", duration)
	}
	return args, nil
}

func autonomyResumeArgs(org string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	return []string{"autonomy", "resume", org}, nil
}

func autonomyDecisionsArgs(org, run string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	args := []string{"autonomy", "decisions", org}
	if run != "" {
		args = append(args, "--run", run)
	}
	return args, nil
}
