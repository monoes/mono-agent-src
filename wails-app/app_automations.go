package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Browser automation packages and recordings (Connections page)
//
// Same doctrine as app_org_unification.go: this file imports nothing from
// internal/. Every binding shells out to `monoagentcli --profile <p> --json
// automation|action|record …` and returns its stdout verbatim — the CLI JSON
// in the build contracts §5 is the UI contract. Failures come back in the
// {"error":"…"} shape (cliResultJSON), so the frontend has one check.
// ─────────────────────────────────────────────────────────────────────────────

// automationCLITimeout bounds list/show/install/lifecycle calls.
const automationCLITimeout = 2 * time.Minute

// automationLongCLITimeout bounds calls that drive the browser or the AI
// runner (analyze, verify, test, doctor).
const automationLongCLITimeout = 10 * time.Minute

// ── Execution ──────────────────────────────────────────────────────────────

// runAutomationCLI runs one monoagentcli call with the active profile and
// returns the frontend JSON string.
func (a *App) runAutomationCLI(timeout time.Duration, sub []string, buildErr error) string {
	if buildErr != nil {
		return aiError(buildErr)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	fullArgs, err := automationCLIArgs(pid, sub...)
	if err != nil {
		return aiError(err)
	}
	label := strings.Join(redactArgs(sub), " ")
	a.emitLog("AUTOMATION", "INFO", fmt.Sprintf("$ %s %s", cliBin, strings.Join(redactArgs(fullArgs), " ")))
	startedAt := time.Now()
	cmd := exec.CommandContext(ctx, cliBin, fullArgs...)
	suppressConsole(cmd)
	out, runErr := cmd.Output()
	elapsed := time.Since(startedAt).Round(time.Millisecond)
	res := automationResultJSON(cliBin, out, runErr)
	if runErr != nil {
		a.emitLog("AUTOMATION", "ERROR", fmt.Sprintf("%s failed after %s: %s", label, elapsed, res))
	} else {
		a.emitLog("AUTOMATION", "INFO", fmt.Sprintf("%s finished in %s", label, elapsed))
	}
	return res
}

// automationResultJSON is cliResultJSON, except that a JSON object on
// stdout wins over a non-zero exit: `automation test|validate` and
// `record verify` exit 1 when something failed but still print their full
// report (one JSON document), which the page shows. Error objects
// ({"error": …}) pass through either way.
func automationResultJSON(cliBin string, stdout []byte, runErr error) string {
	trimmed := strings.TrimSpace(string(stdout))
	if runErr != nil && strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed)) {
		return trimmed
	}
	return cliResultJSON(cliBin, stdout, runErr)
}

func (a *App) runAutomation(sub ...string) string {
	return a.runAutomationCLI(automationCLITimeout, sub, nil)
}

// ── Bindings: packages (contracts §5) ──────────────────────────────────────

// ListAutomations includes uninstalled built-ins (removed: true) so the
// page can offer to restore them.
func (a *App) ListAutomations() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAutomation("automation", "list", "--all")
}

func (a *App) ShowAutomation(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAutomationCLI(automationCLITimeout, withPositional([]string{"automation", "show"}, id), requireArg("automation id", id))
}

// InstallAutomationDryRun returns the install review, including the sha256
// of the exact bytes reviewed.
func (a *App) InstallAutomationDryRun(path string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := automationInstallArgs(path, true, InstallSpec{})
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

// InstallAutomation installs after the review; specJSON is an InstallSpec
// carrying the reviewed sha256 and the replace-built-in consent.
func (a *App) InstallAutomation(path, specJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	var spec InstallSpec
	if strings.TrimSpace(specJSON) != "" {
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			return aiError(fmt.Errorf("install spec: %w", err))
		}
	}
	args, err := automationInstallArgs(path, false, spec)
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

func (a *App) ExportAutomation(id, path string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := automationExportArgs(id, path, false)
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

func (a *App) ExportAction(ref, path string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := actionExportArgs(ref, path)
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

func (a *App) UninstallAutomation(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.automationLifecycle("uninstall", id)
}
func (a *App) RestoreAutomation(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.automationLifecycle("restore", id)
}
func (a *App) EnableAutomation(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.automationLifecycle("enable", id)
}
func (a *App) DisableAutomation(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.automationLifecycle("disable", id)
}
func (a *App) RollbackAutomation(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.automationLifecycle("rollback", id)
}

func (a *App) automationLifecycle(verb, id string) string {
	args, err := automationLifecycleArgs(verb, id)
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

// SetAutomationTrust flips one per-package trust choice: flag is scripts,
// no-scripts, live or no-live (`automation trust`).
func (a *App) SetAutomationTrust(id, flag string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := automationTrustArgs(id, flag)
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

// automationRerecordTimeout covers the CLI's own 180s wait for the user to
// click the element, plus opening the tab.
const automationRerecordTimeout = 4 * time.Minute

// RerecordSelector opens the site in the user's browser and replaces one
// selector with the element the user clicks (contracts §9). Errors include
// "cancelled", "timeout" and "browser bridge not connected".
func (a *App) RerecordSelector(id, key string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := automationRerecordArgs(id, key)
	return a.runAutomationCLI(automationRerecordTimeout, args, err)
}

// ValidateAutomation validates a package directory or action file.
func (a *App) ValidateAutomation(path string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAutomationCLI(automationCLITimeout, withPositional([]string{"automation", "validate"}, path), requireArg("path", path))
}

// TestAutomation runs fixture tests (or live ones) for a package or one action.
func (a *App) TestAutomation(id, action string, live bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := automationTestArgs(id, action, live)
	return a.runAutomationCLI(automationLongCLITimeout, args, err)
}

// DoctorAutomations reports selector health and issues; id "" = all.
func (a *App) DoctorAutomations(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sub := []string{"automation", "doctor"}
	if id != "" {
		sub = withPositional(sub, id)
	}
	return a.runAutomationCLI(automationLongCLITimeout, sub, nil)
}

// ── Bindings: recordings ───────────────────────────────────────────────────

func (a *App) ListRecordings() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAutomation("record", "list")
}

func (a *App) ShowRecording(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAutomationCLI(automationCLITimeout, withPositional([]string{"record", "show"}, id), requireArg("recording id", id))
}

func (a *App) DeleteRecording(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAutomationCLI(automationCLITimeout, recordDeleteArgs(id), requireArg("recording id", id))
}

// AnalyzeRecording turns a recording into a draft; automation "" lets the
// analyzer propose a target (or a new package). advanced lets the AI use
// scripts and other advanced steps (`--allow-advanced`), which the page
// only offers behind a warning.
func (a *App) AnalyzeRecording(id, automation string, advanced bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := recordAnalyzeArgs(id, automation, advanced)
	return a.runAutomationCLI(automationLongCLITimeout, args, err)
}

// VerifyDraft replays a draft; full=false is safe mode (stops before the
// first side-effecting step). inputsJSON is an optional {name: value}
// object for inputs the draft has no value for (secrets are never
// recorded). The values go to the CLI in a 0600 file removed afterwards,
// never on argv.
func (a *App) VerifyDraft(draftDir string, full bool, inputsJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	inputs := map[string]string{}
	if strings.TrimSpace(inputsJSON) != "" {
		if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
			return aiError(fmt.Errorf("verify inputs: invalid JSON"))
		}
	}
	for k, v := range inputs {
		if v == "" {
			delete(inputs, k)
		}
	}
	if err := validateInputNames(inputs); err != nil {
		return aiError(err)
	}
	file := ""
	if len(inputs) > 0 {
		f, err := writeInputsFile(inputs)
		if err != nil {
			return aiError(fmt.Errorf("verify inputs: %w", err))
		}
		defer os.Remove(f)
		file = f
	}
	args, err := recordVerifyArgs(draftDir, full, file)
	return a.runAutomationCLI(automationLongCLITimeout, args, err)
}

// SaveDraft saves a verified draft; specJSON is a SaveDraftSpec.
func (a *App) SaveDraft(draftDir, specJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	var spec SaveDraftSpec
	if strings.TrimSpace(specJSON) != "" {
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			return aiError(fmt.Errorf("save spec: %w", err))
		}
	}
	args, err := recordSaveArgs(draftDir, spec)
	return a.runAutomationCLI(automationCLITimeout, args, err)
}

// ── File dialogs ───────────────────────────────────────────────────────────

// ChooseAutomationPackage opens a native picker for a .mpkg file and
// returns its path ("" when cancelled).
func (a *App) ChooseAutomationPackage() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Import automation",
		Filters: []runtime.FileFilter{{DisplayName: "Automation package", Pattern: "*.mpkg"}},
	})
	if err != nil {
		return ""
	}
	return path
}

// ChooseAutomationExportPath opens a native save dialog for a .mpkg file
// and returns its path ("" when cancelled).
func (a *App) ChooseAutomationExportPath(defaultName string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if defaultName == "" {
		defaultName = "automation.mpkg"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export automation",
		DefaultFilename: defaultName,
		Filters:         []runtime.FileFilter{{DisplayName: "Automation package", Pattern: "*.mpkg"}},
	})
	if err != nil {
		return ""
	}
	return path
}
