// Package autoinstall is the automation package pipeline of `monoagentcli`:
// `automation validate pack install export` and the lifecycle commands
// (uninstall restore enable disable rollback), plus the review → confirm →
// install flow, the hashed file writer and the result printers that `action`,
// `library` and workflow bundles share. Everything these need from the CLI
// around them comes in through Deps, so they can be tested in isolation.
package autoinstall

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
)

// Deps is what the pipeline takes from the CLI around it.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// OpenRegistry opens the automation registry (the CLI's openAutomationRegistry).
	OpenRegistry func() (*automation.Registry, error)
	// StdinIsTerminal reports whether a person can answer the install prompt on stdin.
	StdinIsTerminal func() bool
	// WithInstallResult keeps a failed install's result on its error for --json.
	WithInstallResult func(err error, res *automation.InstallResult) error
	// InstallResultOf is the result WithInstallResult kept on err, or nil.
	InstallResultOf func(err error) *automation.InstallResult
	// Reported marks err as already reported on stdout: the CLI exits 1 without printing it.
	Reported func(err error) error
	// NotFound is a not-found error (exit 2).
	NotFound func(format string, a ...any) error
}

// The helpers below are package copies of the CLI's own (automation_list.go,
// automation_doctor.go and automation.go), with the same output.

func countProblems(issues []automation.IssueJSON) int {
	n := 0
	for _, is := range issues {
		if is.Severity == "error" || is.Severity == "warning" {
			n++
		}
	}
	return n
}

// printIssues prints validation findings, one per line.
func printIssues(out io.Writer, issues []automation.IssueJSON) {
	if len(issues) == 0 {
		return
	}
	// "info" findings (e.g. legacy_suggested_domains) are notes, not
	// problems: they are listed but not counted.
	if n := countProblems(issues); n > 0 {
		fmt.Fprintf(out, "\n%d issue(s):\n", n)
	} else {
		fmt.Fprintln(out, "\nnotes:")
	}
	for _, is := range issues {
		loc := is.File
		if is.StepID != "" {
			loc += "#" + is.StepID
		}
		fmt.Fprintf(out, "  %-7s %s %s: %s\n", is.Severity, loc, is.Code, is.Message)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// writeJSONTo writes v as indented JSON to w.
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// confirmYes asks a y/N question on in; anything but y/yes is a no.
func confirmYes(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// splitCSV splits a comma-separated flag value, dropping empty items.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
