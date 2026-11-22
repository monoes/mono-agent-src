package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Pure argv builders for app_automations.go. Every builder puts flags
// first and `--` before positional ids and paths, so an id or a file name
// that starts with "-" can never be read as a flag.

func requireArg(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s required", name)
	}
	return nil
}

// withPositional appends `--` and the positional arguments.
func withPositional(args []string, pos ...string) []string {
	return append(append(args, "--"), pos...)
}

// recordDeleteArgs is `record delete`; the CLI refuses without --yes, and the
// GUI has already asked in its own confirm dialog (RecordingsTab.jsx).
func recordDeleteArgs(id string) []string {
	return withPositional([]string{"record", "delete", "--yes"}, id)
}

// automationCLIArgs builds the full argv: global flags first, then the
// subcommand words.
func automationCLIArgs(profileID string, sub ...string) ([]string, error) {
	return scopedCLIArgs(profileID, sub...)
}

// InstallSpec is InstallAutomation's JSON argument.
type InstallSpec struct {
	ExpectSHA256 string `json:"expectSha256"` // sha256 from the dry-run review: install exactly those bytes
	Replace      bool   `json:"replace"`      // the user confirmed replacing the installed package (review.replaceRequired)
}

// automationInstallArgs builds `automation install --dry-run|--yes … -- <src>`.
// The GUI shows the review from the dry run, then confirms with --yes and
// the reviewed sha256, so a URL is not trusted twice.
func automationInstallArgs(path string, dryRun bool, spec InstallSpec) ([]string, error) {
	if err := requireArg("package path", path); err != nil {
		return nil, err
	}
	args := []string{"automation", "install"}
	if dryRun {
		args = append(args, "--dry-run")
	} else {
		args = append(args, "--yes")
		if spec.ExpectSHA256 != "" {
			args = append(args, "--expect-sha256", spec.ExpectSHA256)
		}
		if spec.Replace {
			args = append(args, "--replace")
		}
	}
	return withPositional(args, path), nil
}

// automationLifecycleArgs builds `automation <verb> -- <id>` for the footer
// buttons (uninstall, restore, enable, disable, rollback).
func automationLifecycleArgs(verb, id string) ([]string, error) {
	switch verb {
	case "uninstall", "restore", "enable", "disable", "rollback":
	default:
		return nil, fmt.Errorf("unknown automation command %q", verb)
	}
	if err := requireArg("automation id", id); err != nil {
		return nil, err
	}
	return withPositional([]string{"automation", verb}, id), nil
}

// automationTrustArgs builds `automation trust --<flag> -- <id>`.
func automationTrustArgs(id, flag string) ([]string, error) {
	switch flag {
	case "scripts", "no-scripts", "live", "no-live":
	default:
		return nil, fmt.Errorf("unknown trust flag %q", flag)
	}
	if err := requireArg("automation id", id); err != nil {
		return nil, err
	}
	return withPositional([]string{"automation", "trust", "--" + flag}, id), nil
}

func automationTestArgs(id, action string, live bool) ([]string, error) {
	if err := requireArg("automation id", id); err != nil {
		return nil, err
	}
	args := []string{"automation", "test"}
	if live {
		args = append(args, "--live")
	}
	pos := []string{id}
	if action != "" {
		pos = append(pos, action)
	}
	return withPositional(args, pos...), nil
}

func automationExportArgs(id, path string, withRecordings bool) ([]string, error) {
	if err := requireArg("automation id", id); err != nil {
		return nil, err
	}
	if err := requireArg("output path", path); err != nil {
		return nil, err
	}
	args := []string{"automation", "export", "-o", path}
	if withRecordings {
		args = append(args, "--with-recordings")
	}
	return withPositional(args, id), nil
}

func actionExportArgs(ref, path string) ([]string, error) {
	if !strings.Contains(ref, ".") {
		return nil, fmt.Errorf("action reference must be <automation>.<action>, got %q", ref)
	}
	if err := requireArg("output path", path); err != nil {
		return nil, err
	}
	return withPositional([]string{"action", "export", "-o", path}, ref), nil
}

// automationRerecordArgs builds `automation rerecord -- <id> <key>`.
func automationRerecordArgs(id, key string) ([]string, error) {
	if err := requireArg("automation id", id); err != nil {
		return nil, err
	}
	if err := requireArg("selector key", key); err != nil {
		return nil, err
	}
	return withPositional([]string{"automation", "rerecord"}, id, key), nil
}

func recordAnalyzeArgs(id, automation string, advanced bool) ([]string, error) {
	if err := requireArg("recording id", id); err != nil {
		return nil, err
	}
	args := []string{"record", "analyze"}
	if automation != "" {
		args = append(args, "--automation", automation)
	}
	if advanced {
		args = append(args, "--allow-advanced")
	}
	return withPositional(args, id), nil
}

// validateInputNames rejects names that could not round-trip as name=value.
func validateInputNames(inputs map[string]string) error {
	for name := range inputs {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "=\n") {
			return fmt.Errorf("invalid input name %q", name)
		}
	}
	return nil
}

// writeInputsFile writes the verify inputs to a new 0600 file and returns
// its path; the caller removes it. Values never go on argv, where the Logs
// page and `ps` would show them.
func writeInputsFile(inputs map[string]string) (string, error) {
	f, err := os.CreateTemp("", "monoagent-verify-inputs-*.json")
	if err != nil {
		return "", err
	}
	path := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(f).Encode(inputs); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func recordVerifyArgs(draftDir string, full bool, inputsFile string) ([]string, error) {
	if err := requireArg("draft directory", draftDir); err != nil {
		return nil, err
	}
	args := []string{"record", "verify"}
	if full {
		args = append(args, "--full")
	}
	if inputsFile != "" {
		args = append(args, "--inputs-file", inputsFile)
	}
	return withPositional(args, draftDir), nil
}

// SaveDraftSpec is SaveDraft's JSON argument (one JSON string crosses the
// Wails boundary instead of a long positional list).
type SaveDraftSpec struct {
	As           string            `json:"as"`           // action | fragment | workflow
	Automation   string            `json:"automation"`   // existing target package
	New          string            `json:"new"`          // new package id (wins over Automation)
	Name         string            `json:"name"`         // action / fragment name
	RenameInputs map[string]string `json:"renameInputs"` // AI name → user name, changed ones only
	Force        bool              `json:"force"`        // save despite error-level lint (the user chose "Save anyway")
}

func recordSaveArgs(draftDir string, s SaveDraftSpec) ([]string, error) {
	if err := requireArg("draft directory", draftDir); err != nil {
		return nil, err
	}
	args := []string{"record", "save"}
	switch s.As {
	case "":
	case "action", "fragment", "workflow":
		args = append(args, "--as", s.As)
	default:
		return nil, fmt.Errorf("save as must be action, fragment or workflow, got %q", s.As)
	}
	if s.New != "" {
		args = append(args, "--new", s.New)
	} else if s.Automation != "" {
		args = append(args, "--automation", s.Automation)
	}
	if s.Name != "" {
		args = append(args, "--name", s.Name)
	}
	if s.Force {
		args = append(args, "--force")
	}
	if err := validateInputNames(s.RenameInputs); err != nil {
		return nil, err
	}
	for _, from := range sortedKeys(s.RenameInputs) {
		to := s.RenameInputs[from]
		if to != "" && to != from {
			if strings.ContainsAny(to, "=\n") {
				return nil, fmt.Errorf("invalid input name %q", to)
			}
			args = append(args, "--rename-input", from+"="+to)
		}
	}
	return withPositional(args, draftDir), nil
}

// redactArgs masks the value of any --input name=value pair for logging.
// Nothing this file builds puts a secret on argv; this is a backstop.
func redactArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "--input" {
			if name, _, ok := strings.Cut(out[i+1], "="); ok {
				out[i+1] = name + "=***"
			}
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
