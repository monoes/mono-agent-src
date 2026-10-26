package health

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Workflow definitions live in a file and in database rows; a change is published to both through
// an intent row (see workflow.HybridWorkflowStore.Publish). This check reads those rows.
const (
	CheckWorkflowPublications = "workflows.publications"

	FixWorkflowPublicationsRecover = "workflows.publications.recover"
	// FixWorkflowPublicationsResolve is parameterized: "<workflow id>:keep-rows" or "<workflow id>:keep-file".
	FixWorkflowPublicationsResolve = "workflows.publications.resolve"
	// FixWorkflowPublicationsAdopt is parameterized: "<workflow id>:<profile id>:<sha>". It is
	// Optional, so no "fix everything" path (doctor --fix --yes, setup --yes, the app's Fix issues
	// button) ever runs it: it only runs when named.
	FixWorkflowPublicationsAdopt = "workflows.publications.adopt"
)

// WorkflowPublicationsInfo is what the check reads (all local).
type WorkflowPublicationsInfo struct {
	// Open lists the changes that are not finished.
	Open []OpenWorkflowPublication
	// Unreadable lists workflow files that do not parse; Pending lists valid files whose content
	// has not been reconciled with the database yet (an outside edit waiting for the next import).
	Unreadable []string
	Pending    []string
	// ImportedInactive lists workflows that appeared as outside files and were imported inactive.
	ImportedInactive []string
	// Adoptable lists (up to a cap) workflow files with no stored copy: they are neither imported nor
	// used until an operator adopts them. MoreAdoptable counts the rest.
	Adoptable     []AdoptableWorkflowFile
	MoreAdoptable int
}

// AdoptableWorkflowFile is what an operator needs to see before adopting a file.
type AdoptableWorkflowFile struct {
	ID, ProfileID, SHA, Path string
	NodeTypes, Triggers      []string
	Preview                  []string
}

// OpenWorkflowPublication is one unfinished change.
type OpenWorkflowPublication struct {
	WorkflowID string
	Kind       string
	Origin     string
	State      string // pending | unresolved
	Age        time.Duration
	// OwnerGone: the process that started it is no longer running, or has held it past its lease.
	OwnerGone bool
	Note      string
}

func workflowPublicationChecks() []Check {
	return []Check{{ID: CheckWorkflowPublications, Group: GroupCore, Title: "Workflow files and database",
		DependsOn: []string{CheckDB}, Run: checkWorkflowPublications}}
}

func workflowPublicationFixes() []Fix {
	return []Fix{{FixInfo: FixInfo{ID: FixWorkflowPublicationsRecover,
		Label: "Put workflow files back in step with the database", Safety: SafetyAuto},
		Apply: fixWorkflowPublications},
		{FixInfo: FixInfo{ID: FixWorkflowPublicationsResolve, Label: "Resolve the unfinished change of {arg}",
			Safety: SafetyConfirm, Command: "monoagentcli doctor fix workflows.publications.resolve:{arg}"},
			ApplyArg: fixWorkflowPublicationResolve},
		{FixInfo: FixInfo{ID: FixWorkflowPublicationsAdopt, Label: "Adopt the workflow file {arg} as a new, inactive workflow",
			Safety: SafetyConfirm, Optional: true, Command: "monoagentcli doctor fix workflows.publications.adopt:{arg}"},
			ApplyArg: func(ctx context.Context, env *Env, arg string, progress func(string)) error {
				parts := strings.Split(arg, ":")
				if env.AdoptWorkflowFile == nil || len(parts) != 3 {
					return fmt.Errorf("use workflows.publications.adopt:<workflow id>:<profile id>:<sha256> exactly as `doctor` prints it")
				}
				progress("adopting " + parts[0])
				return env.AdoptWorkflowFile(ctx, parts[0], parts[1], parts[2])
			}}}
}

func fixWorkflowPublicationResolve(ctx context.Context, env *Env, arg string, progress func(string)) error {
	i := strings.LastIndex(arg, ":")
	if i <= 0 || env.ResolveWorkflowPublication == nil {
		return fmt.Errorf("use workflows.publications.resolve:<workflow id>:keep-rows or :keep-file")
	}
	id, keep := arg[:i], arg[i+1:]
	progress("resolving " + id + " (" + keep + ")")
	return env.ResolveWorkflowPublication(ctx, id, keep)
}

func checkWorkflowPublications(ctx context.Context, env *Env) Result {
	if env.WorkflowPublications == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	info, err := env.WorkflowPublications(ctx)
	if err != nil {
		return Result{Status: StatusWarn, Summary: "cannot read the workflow change records", Detail: err.Error()}
	}
	res := Result{Status: StatusOK, Summary: "workflow files and database agree"}
	stale, unresolved, unsafe := 0, 0, 0
	for _, o := range info.Open {
		if !safeWorkflowID.MatchString(o.WorkflowID) {
			unsafe++
			continue
		}
		child := Result{ID: CheckWorkflowPublications + "." + rowID(o.WorkflowID), Title: o.WorkflowID}
		switch {
		case o.State == "unresolved":
			unresolved++
			child.Status = StatusWarn
			child.Summary = "the file changed while a change was in progress; choose which to keep"
			child.Detail = capText(o.Note) + "\nkeep the committed rows (rewrites the file): monoagentcli doctor fix workflows.publications.resolve:" + o.WorkflowID + ":keep-rows" +
				"\nkeep the file as it is (the rows follow it): monoagentcli doctor fix workflows.publications.resolve:" + o.WorkflowID + ":keep-file"
			// No default fix: either choice discards something, so the person picks (see Detail).
		case o.OwnerGone:
			stale++
			child.Status = StatusWarn
			child.Summary = fmt.Sprintf("a %s by %s did not finish (%s ago)", o.Kind, quotedOrDefault(o.Origin, "an earlier run"), o.Age.Round(time.Second))
			child.FixID = FixWorkflowPublicationsRecover
		default:
			child.Status = StatusInfo
			child.Summary = fmt.Sprintf("a %s is in progress", o.Kind)
		}
		res.Children = append(res.Children, child)
	}
	unsafe += appendIDRows(&res, info.Unreadable, ".unreadable.", StatusWarn, "the workflow file does not parse; it is not used until it is fixed")
	unsafe += appendIDRows(&res, info.Pending, ".pending.", StatusInfo, "edited outside the app; it is picked up by the next command that opens the workflows")
	for _, a := range info.Adoptable {
		arg := a.ID + ":" + a.ProfileID + ":" + a.SHA
		cmd := "monoagentcli --profile " + a.ProfileID + " doctor fix " + FixWorkflowPublicationsAdopt + ":" + arg
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".adopt." + rowID(a.ID), Title: a.ID,
			Status: StatusInfo,
			Summary: fmt.Sprintf("a workflow file with no stored copy: profile %q, nodes %s, triggers %s; it is not used until you adopt it (inactive, unapproved)",
				a.ProfileID, quotedList(a.NodeTypes), quotedList(a.Triggers)),
			Detail: fmt.Sprintf("file %q\nsha256 %s (check with: sha256sum <file>)\nnodes (escaped, cut):\n%s\nadopt it only after reading this; an operator's own terminal is required, with --profile naming the file's profile:\n%s",
				a.Path, a.SHA, quotedLines(a.Preview), cmd),
			FixID: FixWorkflowPublicationsAdopt + ":" + arg, FixCommand: cmd})
	}
	if info.MoreAdoptable > 0 {
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".adopt.more", Title: "more files",
			Status: StatusInfo, Summary: fmt.Sprintf("and %d more workflow file(s) with no stored copy (or with names that are not safe to print)", info.MoreAdoptable)})
	}
	unsafe += appendIDRows(&res, info.ImportedInactive, ".imported.", StatusInfo, "imported from an outside file, inactive; activate it to use it")
	if unsafe > 0 {
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".unsafe", Title: "more files",
			Status: StatusInfo, Summary: fmt.Sprintf("%d workflow file(s) or change(s) with names that are not safe to print or beyond the listing cap; look in the workflows folder", unsafe)})
	}
	switch {
	case unresolved > 0 || stale > 0 || len(info.Unreadable) > 0:
		res.Status = StatusWarn
		var parts []string
		if stale > 0 {
			parts = append(parts, fmt.Sprintf("%d unfinished change(s)", stale))
			res.FixID = FixWorkflowPublicationsRecover
		}
		if unresolved > 0 {
			parts = append(parts, fmt.Sprintf("%d needing a choice", unresolved))
		}
		if n := len(info.Unreadable); n > 0 {
			parts = append(parts, fmt.Sprintf("%d unreadable file(s)", n))
		}
		res.Summary = strings.Join(parts, ", ")
	case len(res.Children) > 0:
		res.Status, res.Summary = StatusInfo, fmt.Sprintf("%d change(s) in progress or waiting", len(res.Children))
	}
	return res
}

// safeWorkflowID is what a workflow id may look like to be printed bare or put in a command.
var safeWorkflowID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// maxListedIDs bounds how many rows one kind of finding adds to the report.
const maxListedIDs = 50

// appendIDRows adds one row per id whose name is safe to print (up to maxListedIDs) and returns how
// many it left out, so a folder full of odd names cannot flood or forge the report.
func appendIDRows(res *Result, ids []string, mid string, status Status, summary string) (left int) {
	shown := 0
	for _, id := range ids {
		if !safeWorkflowID.MatchString(id) || shown >= maxListedIDs {
			left++
			continue
		}
		shown++
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + mid + rowID(id), Title: id, Status: status, Summary: summary})
	}
	return left
}

// quotedOrDefault prints text from a record escaped, or def when it is empty.
func quotedOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return fmt.Sprintf("%q", s)
}

// capText escapes text from a record and cuts it at a few hundred bytes.
func capText(s string) string {
	q := fmt.Sprintf("%q", s)
	if len(q) > 300 {
		q = q[:300] + `…"`
	}
	return q
}

// quotedList prints names escaped, so a name from a file cannot move the cursor or fake a line.
func quotedList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}

// quotedLines prints one escaped line per entry, indented.
func quotedLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "  %q\n", l)
	}
	return strings.TrimRight(b.String(), "\n")
}

func fixWorkflowPublications(ctx context.Context, env *Env, progress func(string)) error {
	if env.RecoverWorkflowPublications == nil {
		return fmt.Errorf("not available here")
	}
	progress("putting workflow files back in step with the database")
	n, err := env.RecoverWorkflowPublications(ctx)
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("looked at %d unfinished change(s)", n))
	return nil
}
