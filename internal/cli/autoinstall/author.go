package autoinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/spf13/cobra"
)

// Authoring commands: validate, pack (new is in automation_new.go, test in
// automation_fixtures.go).

// NewValidateCmd builds `automation validate`.
func (d Deps) NewValidateCmd() *cobra.Command {
	var builtin bool
	cmd := &cobra.Command{
		Use:   "validate <dir|file.mpkg|action.json>",
		Short: "Validate a package directory, a .mpkg file or a single action file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			issues, err := validateTarget(args[0], builtin)
			if err != nil {
				return err
			}
			ok := !IssuesHaveErrors(issues)
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				if err := writeJSONTo(out, map[string]any{"ok": ok, "issues": issues}); err != nil {
					return err
				}
				if !ok {
					// Exit 1; the JSON above already says why (one document).
					return d.Reported(fmt.Errorf("%s has validation errors", args[0]))
				}
				return nil
			}
			printIssues(out, issues)
			if ok {
				fmt.Fprintf(out, "OK: %s is valid\n", args[0])
				return nil
			}
			return fmt.Errorf("%s has validation errors", args[0])
		},
	}
	cmd.Flags().BoolVar(&builtin, "builtin", false, "Validate as a built-in package (automatic under a source checkout's automations/)")
	return cmd
}

// validateTarget validates whatever path points at. Always returns a
// non-nil slice so --json prints "issues": []. A package is checked as a
// built-in (no imported-package rules) with builtin or when it lives under
// a source checkout's automations/ (the official packages' source).
func validateTarget(target string, builtin bool) ([]automation.IssueJSON, error) {
	st, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	var pkg *automation.Package
	switch {
	case st.IsDir():
		pkg, err = automation.OpenDir(target)
	case strings.EqualFold(filepath.Ext(target), ".json"):
		return validateActionFile(target)
	default:
		pkg, err = automation.OpenFile(target)
	}
	if err != nil {
		return nil, err
	}
	if builtin || IsBuiltinSourceDir(target) {
		pkg.Source = automation.SourceBuiltin
	}
	issues := automation.Validate(pkg)
	if issues == nil {
		issues = []automation.IssueJSON{}
	}
	return issues, nil
}

// IsBuiltinSourceDir reports whether dir is one of the official packages'
// sources in a checkout: automations/<id> (next to automations/embed.go),
// or data/automations/<id> in a checkout from before the move.
func IsBuiltinSourceDir(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	parent := filepath.Dir(abs)
	if filepath.Base(parent) != "automations" {
		return false
	}
	if filepath.Base(filepath.Dir(parent)) == "data" {
		return true
	}
	_, err = os.Stat(filepath.Join(parent, "embed.go"))
	return err == nil
}

// validateActionFile lints one loose action JSON with no package context.
func validateActionFile(file string) ([]automation.IssueJSON, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var def action.ActionDef
	if err := json.Unmarshal(raw, &def); err != nil {
		return []automation.IssueJSON{{File: filepath.Base(file), Severity: "error",
			Code: "invalid_json", Message: err.Error()}}, nil
	}
	out := []automation.IssueJSON{}
	for _, is := range action.Validate(&def, nil) {
		out = append(out, automation.IssueJSON{File: filepath.Base(file), Severity: is.Severity,
			StepID: is.StepID, Code: is.Code, Message: is.Message})
	}
	return out, nil
}

// IssuesHaveErrors reports whether any issue is an error (warnings and notes are not).
func IssuesHaveErrors(issues []automation.IssueJSON) bool {
	for _, is := range issues {
		if is.Severity == "error" {
			return true
		}
	}
	return false
}

// NewPackCmd builds `automation pack`.
func (d Deps) NewPackCmd() *cobra.Command {
	var outFile string
	cmd := &cobra.Command{
		Use:   "pack <dir>",
		Short: "Pack a package directory into a .mpkg file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			pkg, err := automation.OpenDir(dir)
			if err != nil {
				return err
			}
			if issues := automation.Validate(pkg); IssuesHaveErrors(issues) {
				if !d.JSONOutput() {
					printIssues(cmd.ErrOrStderr(), issues)
				}
				return fmt.Errorf("%s has validation errors; fix them before packing", dir)
			}
			if outFile == "" {
				outFile = fmt.Sprintf("%s-%s.mpkg", pkg.Manifest.ID, pkg.Manifest.Version)
			}
			sum, err := WriteHashed(outFile, func(w io.Writer) error { return automation.Pack(dir, w) })
			if err != nil {
				return err
			}
			return d.PrintFileResult(cmd.OutOrStdout(), outFile, sum, "Packed")
		},
	}
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "Output file (default <id>-<version>.mpkg)")
	return cmd
}

// WriteHashed creates file, streams write into it and returns the sha256 of
// what was written. A failed write removes the partial file.
func WriteHashed(file string, write func(io.Writer) error) (string, error) {
	f, err := os.Create(file)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	werr := write(io.MultiWriter(f, h))
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(file)
		return "", werr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PrintFileResult prints {"file","sha256"} (contracts §5) or a line.
func (d Deps) PrintFileResult(out io.Writer, file, sum, verb string) error {
	abs, err := filepath.Abs(file)
	if err != nil {
		abs = file
	}
	if d.JSONOutput() {
		return writeJSONTo(out, map[string]string{"file": abs, "sha256": sum})
	}
	fmt.Fprintf(out, "%s: %s\nsha256: %s\n", verb, abs, sum)
	return nil
}
