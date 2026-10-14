package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Org × Workflow unification bindings (plan 2026-09-15 §7.4/§7.7, contracts
// 2026-09-16 §4/§5).
//
// Same doctrine as app_orgs.go: this file never imports internal/monomind or
// any org/grant/autonomy package — every binding shells `monoagentcli …` and
// returns its stdout JSON verbatim. Argument building lives in pure
// functions (…Args) so it is unit-testable without a binary
// (app_org_unification_test.go).
//
// Grants, automation roles, and autonomy MUST go through these bindings, never
// through UpdateOrgRole (app_orgs_design.go): enforcement lives in the CLI's
// DB rows (C-3, C-54), and a direct JSON edit would at best be stripped by
// reconcile and at worst look like it worked.
// ─────────────────────────────────────────────────────────────────────────────

// aliasPattern is the role-id-shaped slug an automation alias must match
// (plan §2 naming rule). Checked here so a bad value fails fast with a clear
// message instead of a CLI round trip.
var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// durationPattern accepts the pause durations the GUI offers (30m, 2h) and
// any other whole number of minutes or hours.
var durationPattern = regexp.MustCompile(`^[0-9]+[mh]$`)

var (
	validGrantModes     = map[string]bool{"run": true, "trigger": true, "status": true}
	validApprovals      = map[string]bool{"none": true, "required": true}
	validLevels         = map[string]bool{"manual": true, "mid": true, "full": true}
	validDeciderKinds   = map[string]bool{"model": true, "boss": true, "parent": true, "jev": true}
	validTiers          = map[string]bool{"routine": true, "consequential": true, "irreversible": true}
	validFailureActions = map[string]bool{"deny": true, "human": true}
	validReplyModes     = regexp.MustCompile(`^(last_node|node:.+)$`)
)

func requireOrg(org string) error {
	if strings.TrimSpace(org) == "" {
		return fmt.Errorf("org name required")
	}
	return nil
}

func requireAlias(alias string) error {
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("invalid automation alias %q: start with a letter, then lowercase letters, digits, or underscores (40 max)", alias)
	}
	return nil
}

// ── Pure argument builders (everything after `org`) ─────────────────────────

func automationListArgs(org string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	return []string{"automation", "list", org}, nil
}

func automationAddArgs(org, workflowID, alias string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workflowID) == "" {
		return nil, fmt.Errorf("workflow id required")
	}
	if err := requireAlias(alias); err != nil {
		return nil, err
	}
	return []string{"automation", "add", org, "--workflow", workflowID, "--alias", alias}, nil
}

func automationRemoveArgs(org, alias string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	if err := requireAlias(alias); err != nil {
		return nil, err
	}
	return []string{"automation", "remove", org, "--alias", alias}, nil
}

func grantListArgs(org string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	return []string{"grant", "list", org}, nil
}

// grantSpec is SetOrgGrant's specJSON (contracts §5). Pointer fields are
// optional: absent means "let the CLI apply its default". Note that `grant
// add` is an upsert which rebuilds the whole row from its flags, so an edit
// that omits a field resets it to that default rather than keeping what the
// row already held — the caller has to resend everything it wants preserved.
type grantSpec struct {
	Role           string `json:"role"`
	Automation     string `json:"automation"`
	Mode           string `json:"mode"`
	Wait           *bool  `json:"wait"`
	TimeoutSeconds *int   `json:"timeout_seconds"`
	Approval       string `json:"approval"`
	MaxCallsPerRun *int   `json:"max_calls_per_run"`
	MaxCallsPerDay *int   `json:"max_calls_per_day"`
	MaxOutputBytes *int   `json:"max_output_bytes"`
}

func grantAddArgs(org, specJSON string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	var s grantSpec
	if err := json.Unmarshal([]byte(specJSON), &s); err != nil {
		return nil, fmt.Errorf("invalid grant spec: %w", err)
	}
	if strings.TrimSpace(s.Role) == "" {
		return nil, fmt.Errorf("grant spec: role required")
	}
	if err := requireAlias(s.Automation); err != nil {
		return nil, err
	}
	args := []string{"grant", "add", org, "--role", s.Role, "--automation", s.Automation}
	if s.Mode != "" {
		if !validGrantModes[s.Mode] {
			return nil, fmt.Errorf("grant spec: mode must be run, trigger, or status")
		}
		args = append(args, "--mode", s.Mode)
	}
	if s.Wait != nil {
		args = append(args, "--wait="+strconv.FormatBool(*s.Wait))
	}
	if s.TimeoutSeconds != nil {
		if *s.TimeoutSeconds <= 0 {
			return nil, fmt.Errorf("grant spec: timeout_seconds must be positive")
		}
		args = append(args, "--timeout", strconv.Itoa(*s.TimeoutSeconds))
	}
	if s.Approval != "" {
		if !validApprovals[s.Approval] {
			return nil, fmt.Errorf("grant spec: approval must be none or required")
		}
		args = append(args, "--approval", s.Approval)
	}
	if s.MaxCallsPerRun != nil {
		if *s.MaxCallsPerRun <= 0 {
			return nil, fmt.Errorf("grant spec: max_calls_per_run must be positive")
		}
		args = append(args, "--max-calls-per-run", strconv.Itoa(*s.MaxCallsPerRun))
	}
	if s.MaxCallsPerDay != nil {
		if *s.MaxCallsPerDay <= 0 {
			return nil, fmt.Errorf("grant spec: max_calls_per_day must be positive")
		}
		args = append(args, "--max-calls-per-day", strconv.Itoa(*s.MaxCallsPerDay))
	}
	if s.MaxOutputBytes != nil {
		if *s.MaxOutputBytes <= 0 {
			return nil, fmt.Errorf("grant spec: max_output_bytes must be positive")
		}
		args = append(args, "--max-output-bytes", strconv.Itoa(*s.MaxOutputBytes))
	}
	return args, nil
}

func grantRemoveArgs(org, role, alias string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(role) == "" {
		return nil, fmt.Errorf("role required")
	}
	if err := requireAlias(alias); err != nil {
		return nil, err
	}
	return []string{"grant", "remove", org, "--role", role, "--automation", alias}, nil
}

// automationRoleSpec is AddAutomationRole's specJSON (contracts §5).
type automationRoleSpec struct {
	Alias     string `json:"alias"`
	ReportsTo string `json:"reports_to"`
	Title     string `json:"title"`
	Reply     string `json:"reply"`
}

func automationRoleAddArgs(org, specJSON string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	var s automationRoleSpec
	if err := json.Unmarshal([]byte(specJSON), &s); err != nil {
		return nil, fmt.Errorf("invalid automation role spec: %w", err)
	}
	if err := requireAlias(s.Alias); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.ReportsTo) == "" {
		// An endpoint role may never be root (plan §6.1).
		return nil, fmt.Errorf("automation role spec: reports_to required — an automation cannot be the org's root")
	}
	args := []string{"automation-role", "add", org, "--alias", s.Alias, "--reports-to", s.ReportsTo}
	if s.Title != "" {
		args = append(args, "--title", s.Title)
	}
	if s.Reply != "" {
		if !validReplyModes.MatchString(s.Reply) {
			return nil, fmt.Errorf("automation role spec: reply must be last_node or node:<name>")
		}
		args = append(args, "--reply", s.Reply)
	}
	return args, nil
}

func automationRoleRemoveArgs(org, roleID string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(roleID) == "" {
		return nil, fmt.Errorf("role id required")
	}
	return []string{"automation-role", "remove", org, "--role", roleID}, nil
}

func effectiveToolsArgs(org, roleID string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	if strings.TrimSpace(roleID) == "" {
		return nil, fmt.Errorf("role id required")
	}
	return []string{"effective-tools", org, "--role", roleID}, nil
}

func needsYouArgs(org string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	return []string{"autonomy", "needs-you", org}, nil
}

func groupArgs(verb, holding string) ([]string, error) {
	if strings.TrimSpace(holding) == "" {
		return nil, fmt.Errorf("holding org name required")
	}
	switch verb {
	case "start", "stop", "status":
	default:
		return nil, fmt.Errorf("unknown group action %q", verb)
	}
	return []string{"group", verb, holding}, nil
}

// sendSpec is SendOrgMessage's specJSON.
type sendSpec struct {
	To      string `json:"to"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func sendArgs(org, specJSON string) ([]string, error) {
	if err := requireOrg(org); err != nil {
		return nil, err
	}
	var s sendSpec
	if err := json.Unmarshal([]byte(specJSON), &s); err != nil {
		return nil, fmt.Errorf("invalid message spec: %w", err)
	}
	if strings.TrimSpace(s.To) == "" {
		return nil, fmt.Errorf("message spec: to required")
	}
	if strings.TrimSpace(s.Subject) == "" {
		return nil, fmt.Errorf("message spec: subject required")
	}
	args := []string{"send", org, "--to", s.To}
	if s.From != "" {
		args = append(args, "--from", s.From)
	}
	args = append(args, "--subject", s.Subject, "--body", s.Body)
	return args, nil
}

// orgCLIArgs builds the full argv for an org subcommand: global flags first
// (profile, JSON mode), then `org`, the project root, and the subcommand.
func orgCLIArgs(profileID, projectRoot string, sub []string) []string {
	full := []string{}
	if profileID != "" {
		full = append(full, "--profile", profileID)
	}
	full = append(full, "--json", "org")
	if projectRoot != "" {
		full = append(full, "--project", projectRoot)
	}
	return append(full, sub...)
}

func statusCLIArgs(profileID string) []string {
	full := []string{}
	if profileID != "" {
		full = append(full, "--profile", profileID)
	}
	return append(full, "--json", "status")
}

// looksLikeJSON reports whether the CLI's stdout is the JSON document every
// `org`/`status` subcommand promises (contracts §4) rather than prose.
func looksLikeJSON(out string) bool {
	return strings.HasPrefix(out, "{") || strings.HasPrefix(out, "[")
}

// notJSONError explains stdout that parses as neither. The realistic cause is
// a monoagentcli on PATH older than this GUI: a cobra parent command given a
// subcommand it doesn't know prints its own help — on stdout, exit code 0 —
// so the page used to fail with `JSON Parse error: Unexpected identifier
// "Observe"` (the first word of `org`'s help) with nothing pointing at the
// stale binary. Name the binary and quote what it actually said.
func notJSONError(cliBin, out string) error {
	first, _, _ := strings.Cut(out, "\n")
	first = strings.TrimSpace(first)
	if len(first) > 160 {
		first = first[:160] + "…"
	}
	if cliBin == "" {
		cliBin = "monoagentcli"
	}
	return fmt.Errorf("%s printed text instead of JSON — it is most likely an older build that lacks this command; reinstall it from this checkout (go build -o %s ./cmd/monoagentcli). It said: %s",
		cliBin, cliBin, first)
}

// cliResultJSON turns one CLI invocation's outcome into the string handed to
// the frontend: stdout verbatim on success; on failure the CLI's own
// {"error":…} stdout payload when it printed one (contracts §4), else stderr,
// else the exec error — always as the aiError shape.
func cliResultJSON(cliBin string, stdout []byte, runErr error) string {
	trimmed := strings.TrimSpace(string(stdout))
	if runErr == nil {
		if trimmed == "" {
			return aiError(fmt.Errorf("empty output"))
		}
		if !looksLikeJSON(trimmed) {
			return aiError(notJSONError(cliBin, trimmed))
		}
		return trimmed
	}
	if strings.HasPrefix(trimmed, "{") {
		var probe struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(trimmed), &probe) == nil && probe.Error != "" {
			return trimmed
		}
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) && cliStderr(ee.Stderr) != "" {
		return aiError(fmt.Errorf("%s", cliStderr(ee.Stderr)))
	}
	return aiError(runErr)
}
