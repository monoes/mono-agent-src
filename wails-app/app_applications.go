// wails-app/app_applications.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// runMonoCLI runs monoagentcli with --profile <active> --json prefixed to
// args, capturing stdout into result (nil to ignore). Mirrors
// app_vault.go's runVaultCLI, generalized to any subcommand (this file
// spans `application`, `profile`) rather than one hardcoded subcommand.
// This file intentionally does not import internal/applications,
// internal/discovery, internal/matching, internal/documents, or
// internal/apply -- the CLI's --json output is the contract.
func (a *App) runMonoCLI(stdin string, result interface{}, args ...string) error {
	fullArgs, err := a.scopedArgs(args...)
	if err != nil {
		return err
	}
	return a.runCLIArgs(stdin, result, fullArgs)
}

// longCLITimeout bounds the AI-backed application jobs (evaluate, apply),
// which legitimately outlast defaultCLITimeout.
const longCLITimeout = 30 * time.Minute

// runMonoCLILong is runMonoCLI under longCLITimeout.
func (a *App) runMonoCLILong(stdin string, result interface{}, args ...string) error {
	fullArgs, err := a.scopedArgs(args...)
	if err != nil {
		return err
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, longCLITimeout)
	defer cancel()
	return a.runCLIArgsCtx(ctx, stdin, result, fullArgs)
}

// cliTokenPattern is what a positional CLI argument from the frontend (an
// id, tag, status or runtime) may look like: it cannot start with '-', so it
// can never be read as a flag.
var cliTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9 _.:@/+-]{0,127}$`)

// cliToken returns an error unless v is safe to pass as a positional argument.
func cliToken(label, v string) error {
	if !cliTokenPattern.MatchString(v) {
		return fmt.Errorf("invalid %s %q", label, v)
	}
	return nil
}

// runGlobalCLI is runMonoCLI for a machine-wide command (`profile list`,
// `profile create`, ...), which names no profile.
func (a *App) runGlobalCLI(stdin string, result interface{}, args ...string) error {
	return a.runCLIArgs(stdin, result, unscopedCLIArgs(args...))
}

// defaultCLITimeout bounds every runCLIArgs call.
var defaultCLITimeout = 2 * time.Minute

// runCLIArgs runs monoagentcli with the finished argv fullArgs.
func (a *App) runCLIArgs(stdin string, result interface{}, fullArgs []string) error {
	ctx := a.ctx
	if ctx == nil { // before startup, and in tests (which keep ctx nil so emitLog skips the Wails runtime)
		ctx = context.Background()
	}
	// A hung CLI must not block its binding forever. Long jobs call
	// runCLIArgsCtx with their own deadline instead.
	ctx, cancel := context.WithTimeout(ctx, defaultCLITimeout)
	defer cancel()
	return a.runCLIArgsCtx(ctx, stdin, result, fullArgs)
}

// runCLIArgsCtx is runCLIArgs under ctx, for callers that need a deadline.
func (a *App) runCLIArgsCtx(ctx context.Context, stdin string, result interface{}, fullArgs []string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, cliBin, fullArgs...)
	suppressConsole(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && cliStderr(ee.Stderr) != "" {
			return fmt.Errorf("%s", cliStderr(ee.Stderr))
		}
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		cmdline := strings.Join(fullArgs, " ")
		if len(cmdline) > 60 {
			cmdline = cmdline[:60]
		}
		return fmt.Errorf("monoagentcli %s: unexpected output: %w (%.120s)", cmdline, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ── Raw CLI shapes (unmarshal targets only, PascalCase, no tags -- these
// match internal/applications.Application/JobDetails/TenderDetails/
// StatusLogEntry exactly, which have no json tags of their own) ─────────

type rawJobDetails struct {
	Title, Company, URL, Location, Description string
	CompensationMin, CompensationMax           *float64
	Currency, JobType                          string
	IsRemote                                   *bool
	Source, PostedAt                           string
}

type rawTenderDetails struct {
	Title, IssuingOrg, URL, Description, SubmissionDeadline string
	EstimatedValue                                          *float64
	Currency, RequiredCertifications, BidDocumentsRequired  string
	Source, PublishedAt                                     string
}

type rawApplication struct {
	ID, ProfileID, Kind, Status string
	Tags                        []string
	CreatedAt, UpdatedAt        string
	Job                         *rawJobDetails
	Tender                      *rawTenderDetails
}

type rawStatusLogEntry struct {
	ID, FromStatus, ToStatus, Actor, Note, CreatedAt string
}

// ── Clean DTOs returned to the frontend (explicit json tags -- the
// GUI's own stable, consistently-lowercase contract) ────────────────────

// ApplicationSummary is one row in the Applications list.
type ApplicationSummary struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Title     string   `json:"title"`
	Company   string   `json:"company"` // company (job) or issuing org (tender)
	URL       string   `json:"url"`
	UpdatedAt string   `json:"updated_at"`
	Tags      []string `json:"tags"`
}

func summarize(raw rawApplication) ApplicationSummary {
	s := ApplicationSummary{ID: raw.ID, Kind: raw.Kind, Status: raw.Status, UpdatedAt: raw.UpdatedAt, Tags: raw.Tags}
	if s.Tags == nil {
		s.Tags = []string{}
	}
	if raw.Job != nil {
		s.Title, s.Company, s.URL = raw.Job.Title, raw.Job.Company, raw.Job.URL
	}
	if raw.Tender != nil {
		s.Title, s.Company, s.URL = raw.Tender.Title, raw.Tender.IssuingOrg, raw.Tender.URL
	}
	return s
}

// GetApplications lists applications, optionally filtered. An empty
// string argument means "no filter on that dimension" (mirrors
// `application list`'s own flag defaults).
func (a *App) GetApplications(kind, status, tag string) ([]ApplicationSummary, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	args := []string{"application", "list"}
	if kind != "" {
		args = append(args, "--kind", kind)
	}
	if status != "" {
		args = append(args, "--status", status)
	}
	if tag != "" {
		args = append(args, "--tag", tag)
	}
	var raws []rawApplication
	if err := a.runMonoCLI("", &raws, args...); err != nil {
		return nil, err
	}
	out := make([]ApplicationSummary, 0, len(raws))
	for _, r := range raws {
		out = append(out, summarize(r))
	}
	return out, nil
}

// StatusLogEntry is one row of an application's transition history.
type StatusLogEntry struct {
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	Actor      string `json:"actor"`
	Note       string `json:"note"`
	CreatedAt  string `json:"created_at"`
}

// ApplicationDetail is one application's full detail. Job/Tender fields
// are flattened onto the same struct (Description/Location/JobType for
// jobs; Description/IssuingOrg/SubmissionDeadline for tenders) -- the
// frontend branches on Kind to decide which subset to show, matching the
// design spec's stated approach.
type ApplicationDetail struct {
	ApplicationSummary
	Description        string           `json:"description"`
	Location           string           `json:"location"`
	JobType            string           `json:"job_type"`
	IssuingOrg         string           `json:"issuing_org"`
	SubmissionDeadline string           `json:"submission_deadline"`
	StatusLog          []StatusLogEntry `json:"status_log"`
}

// GetApplication returns one application's full detail and status history.
func (a *App) GetApplication(id string) (*ApplicationDetail, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	if err := cliToken("application id", id); err != nil {
		return nil, err
	}
	var result struct {
		Application rawApplication      `json:"application"`
		StatusLog   []rawStatusLogEntry `json:"status_log"`
	}
	if err := a.runMonoCLI("", &result, "application", "get", id); err != nil {
		return nil, err
	}
	detail := &ApplicationDetail{ApplicationSummary: summarize(result.Application)}
	if result.Application.Job != nil {
		detail.Description = result.Application.Job.Description
		detail.Location = result.Application.Job.Location
		detail.JobType = result.Application.Job.JobType
	}
	if result.Application.Tender != nil {
		detail.Description = result.Application.Tender.Description
		detail.IssuingOrg = result.Application.Tender.IssuingOrg
		detail.SubmissionDeadline = result.Application.Tender.SubmissionDeadline
	}
	detail.StatusLog = make([]StatusLogEntry, 0, len(result.StatusLog))
	for _, e := range result.StatusLog {
		detail.StatusLog = append(detail.StatusLog, StatusLogEntry{
			FromStatus: e.FromStatus, ToStatus: e.ToStatus, Actor: e.Actor, Note: e.Note, CreatedAt: e.CreatedAt,
		})
	}
	return detail, nil
}

// AddApplication creates a new job or tender application. kind must be
// "job" or "tender". For "job", company is required in addition to url;
// for "tender", issuingOrg and submissionDeadline are required in
// addition to url -- the CLI's own validation surfaces a clear error if
// a required field is missing, this method does not duplicate that
// validation.
func (a *App) AddApplication(kind, title, company, url, issuingOrg, submissionDeadline string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	args := []string{"application", "add", "--kind", kind, "--title", title, "--url", url}
	if kind == "job" {
		args = append(args, "--company", company)
	}
	if kind == "tender" {
		args = append(args, "--issuing-org", issuingOrg, "--submission-deadline", submissionDeadline)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := a.runMonoCLI("", &result, args...); err != nil {
		return "", err
	}
	return result.ID, nil
}

// SetApplicationStatus transitions an application's status.
//
// "applied" is deliberately rejected here before any CLI call is made:
// every exported *App method is reachable from the renderer's JS namespace
// unconditionally, so without this guard any JS running in the webview
// (devtools console, a compromised dependency) could call
// SetApplicationStatus(id, "applied", "") and flip a pending application
// straight to "applied" -- bypassing the product invariant that only the
// explicit, human-triggered `application send` action (SendApplication
// below) may ever do that. `application status ... set` already rejects
// "applied" itself (cmd/monoagentcli/application.go's
// newApplicationStatusCmd), so this is defense-in-depth in case a future
// caller reaches the CLI a different way.
func (a *App) SetApplicationStatus(id, status, note string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if strings.EqualFold(status, "applied") {
		return fmt.Errorf("\"applied\" is not a valid target here -- use SendApplication, which requires an explicit human action")
	}
	if err := cliToken("application id", id); err != nil {
		return err
	}
	if err := cliToken("status", status); err != nil {
		return err
	}
	args := []string{"application", "status", id, "set", status}
	if note != "" {
		args = append(args, "--note", note)
	}
	return a.runMonoCLI("", nil, args...)
}

// FitVerdictInfo mirrors internal/matching.FitVerdict's own json tags
// exactly (that struct already has them, unlike Application/JobDetails).
type FitVerdictInfo struct {
	EligibilityPass bool    `json:"eligibility_pass"`
	LanguagePass    bool    `json:"language_pass"`
	LocationPass    bool    `json:"location_pass"`
	TechnicalScore  float64 `json:"technical_score"`
	ExperienceScore float64 `json:"experience_score"`
	BehavioralScore float64 `json:"behavioral_score"`
	CareerScore     float64 `json:"career_score"`
	OverallScore    float64 `json:"overall_score"`
	Verdict         string  `json:"verdict"`
	Rationale       string  `json:"rationale"`
	// SandboxStatus is set when the evaluating turn ran without a sandbox.
	SandboxStatus string `json:"sandbox_status,omitempty"`
}

// EvaluateApplication scores one job application's fit via a local AI
// agent. runtime defaults to "claude" if empty.
func (a *App) EvaluateApplication(id, runtime string) (*FitVerdictInfo, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	if runtime == "" {
		runtime = "claude"
	}
	if err := cliToken("application id", id); err != nil {
		return nil, err
	}
	if err := cliToken("runtime", runtime); err != nil {
		return nil, err
	}
	var result FitVerdictInfo
	if err := a.runMonoCLILong("", &result, "application", "evaluate", id, "--runtime", runtime); err != nil {
		return nil, err
	}
	return &result, nil
}

// ApplyResult is ApplyToApplication's return value.
type ApplyResult struct {
	CVHTMLDocumentID          string `json:"cv_html_document_id"`
	CVPDFDocumentID           string `json:"cv_pdf_document_id"`
	CoverLetterHTMLDocumentID string `json:"cover_letter_html_document_id"`
	CoverLetterPDFDocumentID  string `json:"cover_letter_pdf_document_id"`
}

// ApplyToApplication prepares documents (reusing existing ones if already
// generated for this application) and opens the job URL in a real,
// visible browser window. Always invokes `application apply --mode
// auto` -- this subprocess call has no TTY for the CLI's own confirm-mode
// y/N prompt to read from, so confirm-vs-auto UX is implemented entirely
// in the frontend (an upfront confirm() dialog before this method is
// ever called) -- see the design spec's "non-obvious integration detail".
func (a *App) ApplyToApplication(id string, cvData, coverLetterData map[string]interface{}) (*ApplyResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	if err := cliToken("application id", id); err != nil {
		return nil, err
	}
	cvFile, err := writeTempJSON(cvData)
	if err != nil {
		return nil, fmt.Errorf("writing cv data: %w", err)
	}
	defer os.Remove(cvFile)
	letterFile, err := writeTempJSON(coverLetterData)
	if err != nil {
		return nil, fmt.Errorf("writing cover letter data: %w", err)
	}
	defer os.Remove(letterFile)

	var result ApplyResult
	if err := a.runMonoCLILong("", &result, "application", "apply", id, "--mode", "auto",
		"--cv-data-file", cvFile, "--cover-letter-data-file", letterFile); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendApplication records that the user submitted this application
// themselves -- the only path in this entire feature that ever marks an
// application "applied".
func (a *App) SendApplication(id, note string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if err := cliToken("application id", id); err != nil {
		return err
	}
	args := []string{"application", "send", id}
	if note != "" {
		args = append(args, "--note", note)
	}
	return a.runMonoCLI("", nil, args...)
}

// HasGeneratedDocuments reports whether id already has generated (not
// manually-uploaded) documents in the vault -- lets the frontend skip
// re-opening a browser tab for an application a prior "Process Pending"
// run already handled. No dedicated CLI flag exists for this filter (one
// caller doesn't justify adding one yet -- see the design spec's YAGNI
// note); this method filters client-side in Go instead.
func (a *App) HasGeneratedDocuments(id string) (bool, error) {
	if err := a.signedOut(); err != nil {
		return false, err
	}
	var docs []struct {
		ApplicationID string
		Source        string
	}
	if err := a.runMonoCLI("", &docs, "profile", "documents", "list"); err != nil {
		return false, err
	}
	for _, d := range docs {
		if d.ApplicationID == id && d.Source == "generated" {
			return true, nil
		}
	}
	return false, nil
}

// OpenJSONFilePicker opens a native file picker for a JSON file and
// returns the selected path (empty string if cancelled).
func (a *App) OpenJSONFilePicker(title string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   title,
		Filters: []runtime.FileFilter{{DisplayName: "JSON files", Pattern: "*.json"}},
	})
	if err != nil {
		return ""
	}
	a.notePickedJSON(path)
	return path
}

// maxJSONFileBytes caps what ReadJSONFile reads.
const maxJSONFileBytes = 5 << 20

// notePickedJSON remembers a path the person chose in OpenJSONFilePicker.
func (a *App) notePickedJSON(path string) {
	if path == "" {
		return
	}
	a.pickedJSONMu.Lock()
	defer a.pickedJSONMu.Unlock()
	if a.pickedJSON == nil {
		a.pickedJSON = map[string]bool{}
	}
	a.pickedJSON[path] = true
}

func (a *App) wasPickedJSON(path string) bool {
	a.pickedJSONMu.Lock()
	defer a.pickedJSONMu.Unlock()
	return a.pickedJSON[path]
}

// ReadJSONFile reads and parses a JSON file, returning it as a generic
// map -- used by the frontend to load CV/cover-letter data files without
// needing browser-side filesystem access (a Wails webview cannot read
// local files directly; only the Go side can).
func (a *App) ReadJSONFile(path string) (map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	if !a.wasPickedJSON(path) {
		return nil, fmt.Errorf("refusing to read a file that was not picked in the file picker")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxJSONFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(raw) > maxJSONFileBytes {
		return nil, fmt.Errorf("%s is too large (over %d MB)", path, maxJSONFileBytes>>20)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parsing %s as JSON: %w", path, err)
	}
	return result, nil
}

// writeTempJSON writes data as a temp .json file and returns its path.
func writeTempJSON(data map[string]interface{}) (string, error) {
	f, err := os.CreateTemp("", "mono-agent-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
