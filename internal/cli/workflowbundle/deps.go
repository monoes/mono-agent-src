// Package workflowbundle is workflow bundles (spec §5.3): `workflow export
// --bundle-automations` embeds the automation packages a workflow's nodes use, and `workflow
// import` reports, installs or (with --replace-automations) replaces them. `library publish`
// bundles through it too. What it needs from the CLI around it comes in through Deps.
package workflowbundle

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
)

// Deps is what bundling takes from the CLI around it.
type Deps struct {
	// OpenRegistry opens the installed automation packages.
	OpenRegistry func() (*automation.Registry, error)
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// ErrConfirmationRequired is the CLI's error for a yes/no answer nobody can give.
	ErrConfirmationRequired error
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empty entries (the CLI's
// splitCSV).
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// orDash returns s, or "-" when s is empty (the CLI's orDash).
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// issuesHaveErrors reports whether any issue is an error (the CLI's issuesHaveErrors).
func issuesHaveErrors(issues []automation.IssueJSON) bool {
	for _, is := range issues {
		if is.Severity == "error" {
			return true
		}
	}
	return false
}

// confirmYes asks a y/N question on in; anything but y/yes is a no (the CLI's confirmYes).
func confirmYes(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
