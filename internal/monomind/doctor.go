package monomind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CapDoctorJSON: `monomind doctor --json` (protocol rev 9, §10).
const CapDoctorJSON = "doctor-json"

// CapDoctorReadOnly: `doctor --json` without --fix/--install writes nothing.
// CapDoctorOffline: `doctor --offline` skips the checks that use the network.
const (
	CapDoctorReadOnly = "doctor-read-only"
	CapDoctorOffline  = "doctor-offline"
)

// DoctorTimeout bounds one `monomind doctor` run (some checks shell out to
// git, npm and the network).
const DoctorTimeout = 2 * time.Minute

// DoctorResult is one row of `monomind doctor --json`.
type DoctorResult struct {
	Component string  `json:"component"`
	Name      string  `json:"name"`
	Status    string  `json:"status"` // pass | warn | fail | info | skipped
	Message   string  `json:"message"`
	Fix       *string `json:"fix"`
	FixSafety *string `json:"fix_safety"` // auto | confirm | manual
	FixFlag   *string `json:"fix_flag"`   // --fix | --install
}

// DoctorReport is the `monomind doctor --json` payload (v1).
type DoctorReport struct {
	V       int  `json:"v"`
	Success bool `json:"success"`
	Summary struct {
		Passed   int `json:"passed"`
		Warnings int `json:"warnings"`
		Failed   int `json:"failed"`
		Info     int `json:"info"`
		Skipped  int `json:"skipped"`
	} `json:"summary"`
	Results []DoctorResult `json:"results"`
	Fixes   []struct {
		Component string `json:"component"`
		Outcome   string `json:"outcome"`
	} `json:"fixes"`
}

// DoctorOptions selects what `monomind doctor` does.
type DoctorOptions struct {
	Dir       string // project folder to check (the doctor's cwd)
	Component string // "" = all checks
	Fix       bool   // --fix (auto fixes)
	Install   bool   // --install (confirm fixes)
	Offline   bool   // --offline (capability doctor-offline): skip the checks that use the network
}

// DoctorReportVersion is the `monomind doctor --json` format this reads.
const DoctorReportVersion = 1

// ErrDoctorFormat: monomind speaks a report format this monoagent doesn't.
var ErrDoctorFormat = errors.New("unknown monomind doctor report format")

// Doctor runs `monomind doctor --json` in opts.Dir. A failing check makes
// monomind exit 1 but still print the report, so the report is parsed
// whatever the exit status.
func Doctor(ctx context.Context, bin string, opts DoctorOptions) (*DoctorReport, error) {
	if err := CheckOutside(bin, opts.Dir); err != nil {
		return nil, err
	}
	args := []string{"doctor", "--json"}
	if opts.Component != "" {
		args = append(args, "-c", opts.Component)
	}
	if opts.Fix {
		args = append(args, "--fix")
	}
	if opts.Install {
		args = append(args, "--install")
	}
	if opts.Offline {
		args = append(args, "--offline")
	}
	ctx, cancel := context.WithTimeout(ctx, DoctorTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.Dir
	cmd.Env = PinEnvIn(append(ScrubDestinations(os.Environ()), "CI=true"), bin, opts.Dir) // never prompt
	// On the deadline, end everything monomind started (git, npm), and
	// don't let a child that still holds the output pipe keep Output()
	// waiting.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessGroup(cmd, 0)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var rep DoctorReport
	if err := json.Unmarshal(JSONBody(out), &rep); err != nil || rep.V == 0 {
		if runErr == nil {
			runErr = errors.New("output is not a doctor report")
		}
		msg := fmt.Sprintf("monomind doctor in %s: %v", opts.Dir, runErr)
		if tail := lastLines(stderr.String(), 3); tail != "" {
			msg += ": " + tail
		}
		return nil, errors.New(msg)
	}
	if rep.V != DoctorReportVersion {
		return nil, fmt.Errorf("%w: monomind reports format v%d, this monoagent reads v%d", ErrDoctorFormat, rep.V, DoctorReportVersion)
	}
	return &rep, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// skipDirs are never descended into when looking for projects.
var skipDirs = map[string]bool{"node_modules": true, ".git": true, ".monomind": true, ".monoagent": true,
	".claude": true, "dist": true, "build": true, "vendor": true, ".venv": true}

// ProjectsUnder lists the monomind projects inside root (not root itself):
// folders up to three levels down that `monomind init` set up, plus
// monomind's own registered projects (~/.monomind-projects.json) under
// root. Sorted, de-duplicated, absolute.
func ProjectsUnder(root string) []string {
	root = filepath.Clean(root)
	found := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 3 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || skipDirs[e.Name()] {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if IsInitializedAt(p) {
				found[p] = true
			}
			walk(p, depth+1)
		}
	}
	walk(root, 1)
	for _, p := range registeredProjects() {
		p = filepath.Clean(p)
		if strings.HasPrefix(p, root+string(filepath.Separator)) && IsInitializedAt(p) {
			found[p] = true
		}
	}
	out := make([]string, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// registeredProjects reads ~/.monomind-projects.json (written by monomind's
// `init upgrade --all`); nil when absent or unreadable.
func registeredProjects() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(home, ".monomind-projects.json"))
	if err != nil {
		return nil
	}
	var f struct {
		Projects []string `json:"projects"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return f.Projects
}

// JSONBody returns out from its first line that starts a JSON document.
// monomind releases up to 2.16.x print an "↑ … available" update notice on
// stdout, ahead of the JSON, on the first run after a release (fixed in
// monomind by sending it to stderr); skipping such leading lines keeps
// those versions parseable.
func JSONBody(out []byte) []byte {
	rest := out
	for len(rest) > 0 {
		trimmed := bytes.TrimLeft(rest, " \t\r")
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return trimmed
		}
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		rest = rest[i+1:]
	}
	return out
}

// KnownGoodMonomindVersion is the oldest monomind mono-agent is tested
// against end to end (the golden fixtures in testdata/monomind-2.24.1 are
// recorded from it). MinMonomindVersion is what the client needs to work at
// all and stays a hard requirement; an older monomind between the two still
// runs, with the gaps KnownGoodDegrades names.
const KnownGoodMonomindVersion = "2.24.1"

// KnownGoodDegrades says what is lost below KnownGoodMonomindVersion.
const KnownGoodDegrades = "org sections (documents handed between sections), the schedule audit lines of " +
	"scheduled runs, and runtime-isolation findings are only validated and reported from monomind " +
	KnownGoodMonomindVersion + "; with an older monomind, `org validate` can accept or reject definitions " +
	"differently (the removed \"loops\" key, constant keys) and those parts of an org's state are absent " +
	"from what the app shows"

// BelowKnownGood reports whether version is older than
// KnownGoodMonomindVersion. An empty or unparseable version is not "below":
// nothing is known about it, so nothing is warned.
func BelowKnownGood(version string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return false
	}
	return !versionAtLeast(v, KnownGoodMonomindVersion)
}

// KnownGoodAdvisory is the warning for a monomind older than
// KnownGoodMonomindVersion, "" when version is not.
func KnownGoodAdvisory(version string) string {
	if !BelowKnownGood(version) {
		return ""
	}
	return fmt.Sprintf("monomind %s is older than %s, the version mono-agent is tested with: %s — update it: npm install -g @monoes/monomindcli@latest",
		strings.TrimPrefix(strings.TrimSpace(version), "v"), KnownGoodMonomindVersion, KnownGoodDegrades)
}
