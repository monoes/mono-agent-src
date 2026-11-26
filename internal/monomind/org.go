package monomind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// Org observe/action commands (protocol §7): thin proxies over
// `monomind org <sub> [<name>] --format json`. Every subcommand resolves
// its project by cwd (§7.1), so callers pass projectRoot explicitly — this
// client manages multiple project roots, unlike the CLI's own cwd.
//
// Read-only/action results are returned as json.RawMessage rather than
// decoded into typed structs: the org JSON shapes are still evolving on the
// monomind side and callers here (the CLI, then the Wails bindings, then
// the frontend) only need to pass the payload through, not manipulate it in
// Go. This avoids silently dropping fields we didn't anticipate.

// orgTimeout bounds one-shot org observe/action calls.
var orgTimeout = 60 * time.Second

// runOrgJSON runs `monomind org <args...> --format json` with cwd=projectRoot
// and returns the raw stdout payload.
func runOrgJSON(ctx context.Context, projectRoot string, args ...string) (json.RawMessage, error) {
	return runOrgJSONFull(ctx, projectRoot, args, append(append([]string{"org"}, args...), "--format", "json"))
}

// runOrgJSONText is runOrgJSON for a subcommand whose trailing values are
// free text (an answer, a gate's resolution) or ids: cmd is the subcommand
// and its flags, and values go after "--", so a value like "--by=rule" is
// never read as a flag.
func runOrgJSONText(ctx context.Context, projectRoot string, cmd []string, values ...string) (json.RawMessage, error) {
	full := append(append([]string{"org"}, cmd...), "--format", "json", "--")
	return runOrgJSONFull(ctx, projectRoot, append(append([]string(nil), cmd...), values...), append(full, values...))
}

// runOrgJSONFull runs `monomind <full...>`; args names the command in errors.
func runOrgJSONFull(ctx context.Context, projectRoot string, args, full []string) (json.RawMessage, error) {
	bin, err := EnsureIn(ctx, projectRoot)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, orgTimeout)
	defer cancel()
	cmd := CommandContext(cctx, bin, full...)
	inRoot(cmd, projectRoot)

	out, err := cmd.Output()
	if err != nil {
		return nil, orgCommandError(args, err)
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("monomind org %s: empty output", strings.Join(args, " "))
	}
	return json.RawMessage(trimmed), nil
}

// orgCommandError extracts the actionable message from a failed org
// subprocess. Exit codes are not reliably distinguishable between usage and
// runtime errors (protocol §7.1 caveat verified against org.ts), so this
// always surfaces stderr text rather than branching on exit status.
func orgCommandError(args []string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg := strings.TrimSpace(string(ee.Stderr))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("monomind org %s: %s", strings.Join(args, " "), msg)
	}
	return fmt.Errorf("monomind org %s: %w", strings.Join(args, " "), err)
}

// runOrgText runs `monomind org <args...>` whose output is human text, not
// protocol JSON (validate/reload — neither takes --format json). Mirrors
// runOrgJSON's arg convention: callers pass the subcommand args without the
// "org" prefix, which is added internally. Returns the trimmed
// stdout+stderr text and a non-nil error when the exit code is non-zero,
// using orgCommandError's stderr-extraction pattern already established for
// runOrgJSON.
func runOrgText(ctx context.Context, projectRoot string, args ...string) (string, error) {
	bin, err := EnsureIn(ctx, projectRoot)
	if err != nil {
		return "", err
	}
	return runOrgTextWith(ctx, bin, projectRoot, args...)
}

// runOrgTextWith is runOrgText through a given monomind binary.
func runOrgTextWith(ctx context.Context, bin, projectRoot string, args ...string) (string, error) {
	out, err := orgTextOutput(ctx, bin, projectRoot, args...)
	return orgTextResult(args, out, err)
}

// orgTextOutput runs `<bin> org <args...>` in projectRoot: its combined
// output and exit error, unformatted.
func orgTextOutput(ctx context.Context, bin, projectRoot string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, orgTimeout)
	defer cancel()
	cmd := CommandContext(cctx, bin, append([]string{"org"}, args...)...)
	inRoot(cmd, projectRoot)
	return cmd.CombinedOutput()
}

// orgTextResult is runOrgTextWith's result from orgTextOutput's.
func orgTextResult(args []string, out []byte, err error) (string, error) {
	if err != nil {
		// CombinedOutput leaves ExitError.Stderr empty, so orgCommandError
		// alone would say only "exit status 1": keep what monomind printed
		// (for validate, the list of problems).
		if msg := strings.TrimSpace(string(out)); msg != "" {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return "", fmt.Errorf("monomind org %s: %s", strings.Join(args, " "), msg)
			}
		}
		return "", orgCommandError(args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// OrgValidate runs `monomind org validate <name>` and returns its output
// text. A non-nil error means validation failed (or the subprocess itself
// failed to run) — err's message is the actionable text from the CLI.
func OrgValidate(ctx context.Context, projectRoot, name string) (string, error) {
	return runOrgText(ctx, projectRoot, "validate", name)
}

// OrgReload signals a running org's daemon to pick up config changes
// without a full restart (writes the CLI's own `reload` sentinel file).
func OrgReload(ctx context.Context, projectRoot, name string) (string, error) {
	return runOrgText(ctx, projectRoot, "reload", name)
}

// OrgList returns every org in the project (`org list`).
func OrgList(ctx context.Context, projectRoot string) (json.RawMessage, error) {
	raw, err := runOrgJSON(ctx, projectRoot, "list")
	if err != nil {
		return nil, err
	}
	return dropUnnamableOrgs(raw), nil
}

// dropUnnamableOrgs removes list items whose name could not belong to an org.
//
// The orgs folder holds more than orgs — a .mcp.json tool config, whatever
// else a user drops beside them — and monomind's own lister takes every
// .json in it, so `org list` offered ".mcp" as an org with no roles. The
// designer then reported it, correctly for an empty config, as having no
// root role; it sorts before every letter, so it was the entry the GUI
// selected by default and the error a user saw constantly while every real
// org was fine. monomind itself refuses to act on such a name ("invalid org
// name"), so listing it can only mislead.
//
// Unparseable or unexpected output is passed through untouched: this filters
// a known bad entry, it does not police the protocol.
func dropUnnamableOrgs(raw json.RawMessage) json.RawMessage {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw
	}
	itemsRaw, ok := envelope["items"]
	if !ok {
		return raw
	}
	var items []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return raw
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var probe struct {
			Name string `json:"name"`
		}
		// An item we cannot read a name from stays — dropping it would hide
		// an org over a shape we simply did not expect.
		if err := json.Unmarshal(item, &probe); err != nil || probe.Name == "" || orgdesign.ValidOrgName(probe.Name) {
			kept = append(kept, item)
		}
	}
	if len(kept) == len(items) {
		return raw
	}
	filtered, err := json.Marshal(kept)
	if err != nil {
		return raw
	}
	envelope["items"] = filtered
	out, err := json.Marshal(envelope)
	if err != nil {
		return raw
	}
	return out
}

// OrgLogs returns the org's bus event log (`org logs <name>`). There is no
// --tail flag on the live monomind CLI despite the plan doc's claim.
// run, when non-empty, scopes to that specific run id (`--run <id>`)
// instead of monomind's own default of "the most recent run" (org.ts's
// resolveRun).
func OrgLogs(ctx context.Context, projectRoot, name, run string) (json.RawMessage, error) {
	args := []string{"logs", name}
	if run != "" {
		args = append(args, "--run", run)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgReport returns the org's run report; all=true requests every recorded
// run (`org report <name> --all`) instead of just one. run, when non-empty,
// scopes the non-all case to that specific run id (`--run <id>`) instead of
// monomind's own default of "the most recent run" — ignored when all=true,
// since --all already reports on every run.
func OrgReport(ctx context.Context, projectRoot, name string, all bool, run string) (json.RawMessage, error) {
	args := []string{"report", name}
	if all {
		args = append(args, "--all")
	} else if run != "" {
		args = append(args, "--run", run)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgCosts returns per-role token/cost totals (`org costs <name>`). run, when
// non-empty, scopes to that specific run id (`--run <id>`) instead of
// monomind's own default of "the most recent run".
func OrgCosts(ctx context.Context, projectRoot, name, run string) (json.RawMessage, error) {
	args := []string{"costs", name}
	if run != "" {
		args = append(args, "--run", run)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgFlow returns the org's role communication graph (`org flow <name>`).
// run, when non-empty, scopes to that specific run id (`--run <id>`) instead
// of monomind's own default of "the most recent run".
func OrgFlow(ctx context.Context, projectRoot, name, run string) (json.RawMessage, error) {
	args := []string{"flow", name}
	if run != "" {
		args = append(args, "--run", run)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgQuestions returns pending human-input questions (`org questions <name>`).
func OrgQuestions(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "questions", name)
}

// OrgApprovals returns pending tool/action approval requests (`org
// approvals <name>`) — the queue checked by checkApproval for
// Bash/WebFetch/WebSearch/org_complete, distinct from and not resolved by
// OrgQuestions/OrgGates.
func OrgApprovals(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "approvals", name)
}

// OrgGates returns pending decision gates (`org gates <name>`).
func OrgGates(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "gates", name)
}

// OrgHumanItemsAll returns every question, approval or gate of the org,
// resolved ones included (`org questions|approvals|gates <name> --all`):
// kind is "questions", "approvals" or "gates". Resolving an item twice is
// only safe when the caller can tell "already resolved" from "unknown".
func OrgHumanItemsAll(ctx context.Context, projectRoot, name, kind string) (json.RawMessage, error) {
	switch kind {
	case "questions", "approvals", "gates":
	default:
		return nil, fmt.Errorf("monomind org: unknown item kind %q", kind)
	}
	return runOrgJSON(ctx, projectRoot, kind, name, "--all")
}

// OrgDecisions returns the org's decision trace (`org decisions <name>`).
// run, when non-empty, scopes to that specific run id (`--run <id>`) instead
// of monomind's own default of "the most recent run".
func OrgDecisions(ctx context.Context, projectRoot, name, run string) (json.RawMessage, error) {
	args := []string{"decisions", name}
	if run != "" {
		args = append(args, "--run", run)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgMemoryStats returns org memory statistics (`org memory <name> stats`).
func OrgMemoryStats(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "memory", name, "stats")
}

// OrgAnswer answers a pending human-input question
// (`org answer <name> <questionID> <answer...>`).
func OrgAnswer(ctx context.Context, projectRoot, name, questionID, answer string) (json.RawMessage, error) {
	return runOrgJSONText(ctx, projectRoot, []string{"answer"}, name, questionID, answer)
}

// OrgGateApprove approves a decision gate
// (`org gate-approve <name> <gateID> [resolution...]`).
func OrgGateApprove(ctx context.Context, projectRoot, name, gateID, resolution string) (json.RawMessage, error) {
	values := []string{name, gateID}
	if resolution != "" {
		values = append(values, resolution)
	}
	return runOrgJSONText(ctx, projectRoot, []string{"gate-approve"}, values...)
}

// OrgGateReject rejects a decision gate (`org gate-reject <name> <gateID> [resolution...]`).
func OrgGateReject(ctx context.Context, projectRoot, name, gateID, resolution string) (json.RawMessage, error) {
	values := []string{name, gateID}
	if resolution != "" {
		values = append(values, resolution)
	}
	return runOrgJSONText(ctx, projectRoot, []string{"gate-reject"}, values...)
}
