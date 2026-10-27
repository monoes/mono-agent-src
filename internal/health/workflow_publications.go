package health

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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
	// Incomplete: a limit stopped a scan (the listing of files with no stored copy, or the comparison
	// of files with the database), so something may be missing from the report.
	Incomplete bool
	// FolderTooLarge: the workflows folder has more files than the scan covers.
	FolderTooLarge bool
	// FocusWhy says why the file asked for with --workflow cannot be shown ("" when it can, or when
	// none was asked for).
	FocusWhy string
	// HiddenAdoptable names (plain ids only) the first few of the files counted in MoreAdoptable.
	HiddenAdoptable []string
}

// AdoptableWorkflowFile is what an operator needs to see before adopting a file.
type AdoptableWorkflowFile struct {
	ID, ProfileID, SHA, Path string
	// NodeTypes and Triggers hold only types the node registry knows; Unknown counts the others.
	NodeTypes, Triggers []string
	Unknown             int
	// Preview is text from the file: it goes only to a person's terminal, never into the JSON
	// report (and so never into the MCP doctor tool).
	Preview                  []string
	Nodes, ConfigBytes, Refs int
	HasSettings              bool
	ErrorWorkflow            bool
	Cut                      bool
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
	// Every record is counted (severity and the recovery fix depend on all of them); only the rows
	// shown are capped.
	// The most urgent records are drawn first, so the row cap never hides one of them.
	open := append([]OpenWorkflowPublication(nil), info.Open...)
	rank := func(o OpenWorkflowPublication) int {
		switch {
		case o.State == "unresolved":
			return 0
		case o.OwnerGone:
			return 1
		}
		return 2
	}
	sort.SliceStable(open, func(i, j int) bool { return rank(open[i]) < rank(open[j]) })
	for _, o := range open {
		switch {
		case o.State == "unresolved":
			unresolved++
		case o.OwnerGone:
			stale++
		}
		if len(res.Children) >= maxListedIDs {
			unsafe++
			continue
		}
		title, plain := o.WorkflowID, openRecordID.MatchString(o.WorkflowID)
		if !plain {
			title = capQuoted(o.WorkflowID, 64)
		}
		child := Result{ID: CheckWorkflowPublications + "." + rowID(cutAtRune(o.WorkflowID, 64)), Title: title}
		switch {
		case o.State == "unresolved":
			child.Status = StatusWarn
			child.Summary = "the file changed while a change was in progress; choose which to keep"
			child.Detail = capQuoted(o.Note, 300)
			if plain {
				child.Detail += "\nkeep the committed rows (rewrites the file): monoagentcli doctor fix workflows.publications.resolve:" + o.WorkflowID + ":keep-rows" +
					"\nkeep the file as it is (the rows follow it): monoagentcli doctor fix workflows.publications.resolve:" + o.WorkflowID + ":keep-file"
			}
			// No default fix: either choice discards something, so the person picks (see Detail).
		case o.OwnerGone:
			child.Status = StatusWarn
			child.Summary = fmt.Sprintf("a %s by %s did not finish (%s ago)", capQuoted(o.Kind, 32), quotedOrDefault(capText64(o.Origin), "an earlier run"), o.Age.Round(time.Second))
			child.FixID = FixWorkflowPublicationsRecover
		default:
			child.Status = StatusInfo
			child.Summary = fmt.Sprintf("a %s is in progress", capQuoted(o.Kind, 32))
		}
		res.Children = append(res.Children, child)
	}
	unsafe += appendIDRows(&res, info.Unreadable, ".unreadable.", StatusWarn, "the workflow file does not parse; it is not used until it is fixed")
	unsafe += appendIDRows(&res, info.Pending, ".pending.", StatusInfo, "edited outside the app; it is picked up by the next command that opens the workflows")
	for _, a := range info.Adoptable {
		arg := a.ID + ":" + a.ProfileID + ":" + a.SHA
		cmd := "monoagentcli --profile " + a.ProfileID + " doctor fix " + FixWorkflowPublicationsAdopt + ":" + arg
		incomplete := ""
		if a.Cut || a.Unknown > 0 {
			incomplete = fmt.Sprintf("INCOMPLETE: read %s before adopting\n", a.Path)
		}
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".adopt." + rowID(a.ID), Title: a.ID,
			Status: StatusInfo,
			Summary: fmt.Sprintf("a workflow file with no stored copy: profile %q, %d node(s), types %s; it is not used until you adopt it (inactive, unapproved)",
				a.ProfileID, a.Nodes, quotedList(a.NodeTypes)),
			Detail: fmt.Sprintf("file %q\nsha256 %s (check with: sha256sum <file>)\n%s%d node(s), %d byte(s) of node configuration, %d unknown node type(s); workflow settings: %t, error workflow: %t, template or secret markers (syntax only, not a verdict): %d\nadopt it only after reading the file; an operator's own terminal is required, with --profile naming the file's profile:\n%s",
				a.Path, a.SHA, incomplete, a.Nodes, a.ConfigBytes, a.Unknown, a.HasSettings, a.ErrorWorkflow, a.Refs, cmd),
			Preview: previewText(a.Preview),
			FixID:   FixWorkflowPublicationsAdopt + ":" + arg, FixCommand: cmd})
	}
	if info.MoreAdoptable > 0 {
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".adopt.more", Title: "more files",
			Status: StatusInfo, Summary: fmt.Sprintf("and %s more workflow file(s) with no stored copy (not shown: names that are not safe to print, too big, not parsing, or beyond the listing)", moreCount(info.MoreAdoptable, info.Incomplete)),
			Detail: hiddenAdoptDetail(info.HiddenAdoptable)})
	}
	unsafe += appendIDRows(&res, info.ImportedInactive, ".imported.", StatusInfo, "imported from an outside file, inactive; activate it to use it")
	if unsafe > 0 {
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".unsafe", Title: "more files",
			Status: StatusInfo, Summary: fmt.Sprintf("%d workflow file(s) or change(s) with names that are not safe to print or beyond the listing cap; look in the workflows folder", unsafe)})
	}
	if info.FocusWhy != "" {
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".focus", Title: "that file", Status: StatusInfo, Summary: info.FocusWhy})
	}
	if info.Incomplete {
		why := "the scan of the workflow files stopped at a limit, so files may be missing from this report"
		if info.FolderTooLarge {
			why = "the workflows folder has more files than the scan covers: look in the folder itself, files may be missing from this report"
		}
		res.Children = append(res.Children, Result{ID: CheckWorkflowPublications + ".incomplete", Title: "scan incomplete", Status: StatusInfo,
			Summary: why,
			Detail:  "look at one file by its id with: monoagentcli doctor --check " + CheckWorkflowPublications + " --workflow <id>"})
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
	case len(res.Children) > 0 && !(info.Incomplete && len(res.Children) == 1):
		res.Status, res.Summary = StatusInfo, fmt.Sprintf("%d change(s) in progress or waiting", len(res.Children))
	case info.Incomplete:
		res.Status, res.Summary = StatusInfo, "no unfinished changes found, but the scan of the workflow files is incomplete"
	}
	return res
}

// safeWorkflowID is what a workflow id may look like to be printed bare or put in a command.
var safeWorkflowID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// openRecordID is what a workflow id of a change record may look like to be printed bare and put
// in a command (ids in the database may contain dots, unlike file names).
var openRecordID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// capQuoted escapes s and cuts the result at n bytes, on a character boundary.
func capQuoted(s string, n int) string {
	q := fmt.Sprintf("%q", s)
	if len(q) > n {
		q = cutAtRune(q, n) + `…"`
	}
	return q
}

// capText64 cuts a record field before it is quoted.
func capText64(s string) string { return strings.ToValidUTF8(cutAtRune(s, 64), "") }

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
func capText(s string) string { return capQuoted(s, 300) }

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

// maxPreviewBytes bounds the file text one row shows to a person.
const maxPreviewBytes = 2048

// previewText escapes the lines taken from a file and cuts them at maxPreviewBytes in all.
func previewText(lines []string) string {
	out := quotedLines(lines)
	if len(out) > maxPreviewBytes {
		out = cutAtRune(out, maxPreviewBytes) + "\n  (cut)"
	}
	return out
}

// moreCount prints a count that is a lower bound as "N+".
func moreCount(n int, lowerBound bool) string {
	if lowerBound {
		return fmt.Sprintf("%d+", n)
	}
	return fmt.Sprintf("%d", n)
}

// hiddenAdoptDetail names files that were not shown and says how one of them is still adopted.
func hiddenAdoptDetail(ids []string) string {
	var safe []string
	for _, id := range ids {
		if safeWorkflowID.MatchString(id) {
			safe = append(safe, id)
		}
	}
	if len(safe) == 0 {
		return ""
	}
	return "not shown: " + strings.Join(safe, ", ") + "\nto adopt one anyway, read the file, take its sha256 (sha256sum) and run: monoagentcli --profile <the file's profile> doctor fix " + FixWorkflowPublicationsAdopt + ":<id>:<profile>:<sha256>"
}

// cutAtRune cuts s at most n bytes long without splitting a UTF-8 character.
func cutAtRune(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
