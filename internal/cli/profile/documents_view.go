package profile

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capturesummary"
	"github.com/monoes/mono-agent/internal/vault"
)

// documentJSON is `profile documents get --json`: one document of the
// active profile, snake_case (unlike `documents list`, which predates the
// convention and prints vault.DocumentEntry as-is).
type documentJSON struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes"`
	Source        string `json:"source"`
	ApplicationID string `json:"application_id"`
	CreatedAt     string `json:"created_at"`
	Indexed       bool   `json:"indexed"`
	IndexError    string `json:"index_error"`
	Stale         bool   `json:"stale"`
	URL           string `json:"url"`
	CaptureDir    string `json:"capture_dir"`
	SummaryStatus string `json:"summary_status"`
}

// getProfileDocument loads id scoped to the active profile, or a not-found
// error (exit code 2).
func (d Deps) getProfileDocument(cmd *cobra.Command, id string) (*vault.DocumentEntry, error) {
	db, err := d.OpenDB()
	if err != nil {
		return nil, fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()
	doc, err := vault.GetDocument(cmd.Context(), db.DB, d.ProfileID(), id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, d.NotFound("document %s not found", id)
	}
	return doc, nil
}

func (deps Deps) newProfileDocumentsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one profile document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := deps.getProfileDocument(cmd, args[0])
			if err != nil {
				return err
			}
			out := documentJSON{
				ID: d.ID, Filename: d.Filename, Path: d.Path, SizeBytes: d.SizeBytes,
				Source: d.Source, ApplicationID: d.ApplicationID, CreatedAt: d.CreatedAt,
				Indexed: d.Indexed, IndexError: d.IndexError, Stale: d.Stale,
				URL: d.URL, CaptureDir: d.CaptureDir,
				SummaryStatus: capturesummary.StateOf(d.CaptureDir, time.Now()),
			}
			if deps.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n%s\n", out.ID, out.Filename, out.Path)
			return nil
		},
	}
}

// captureView is `profile documents capture --json`: everything a viewer
// shows for one browser capture — the page's readable text and screenshot,
// plus the AI summary and video transcript for captures saved with "Save
// page summary" / "Save video summary". A capture's primary file is often
// page.mhtml, which no viewer can show, so a capture is previewed from its
// parts instead.
type captureView struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	CapturedAt string `json:"captured_at"`
	WordCount  int    `json:"word_count"`
	Readable   string `json:"readable"`   // readable.md, "" when absent
	Screenshot string `json:"screenshot"` // data URL of screenshot.png, "" when absent
	Summary    string `json:"summary"`    // summary.md, "" when absent
	Transcript string `json:"transcript"` // transcript.md, "" when absent
	// SummaryStatus is where the summary is, when one was asked for
	// (summary.json); null for a capture that never asked.
	SummaryStatus *captureSummaryStatus `json:"summary_status"`
}

// captureSummaryStatus is summary.json, as a viewer needs it. Status is
// pending, running, done, error, or stalled (pending/running for longer
// than any bridge could still be working on it).
type captureSummaryStatus struct {
	Status     string `json:"status"`
	Runtime    string `json:"runtime"`
	Error      string `json:"error"`
	FinishedAt string `json:"finished_at"`
}

const (
	// maxCaptureReadableBytes caps each text part; a capture's readable.md
	// is an article, so anything past this is not one.
	maxCaptureReadableBytes = 2 * 1024 * 1024
	// maxCaptureScreenshotBytes caps the inlined screenshot.
	maxCaptureScreenshotBytes = 25 * 1024 * 1024
)

func (deps Deps) newProfileDocumentsCaptureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capture <id>",
		Short: "Show a browser capture's parts: readable text, screenshot, summary, transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := deps.getProfileDocument(cmd, args[0])
			if err != nil {
				return err
			}
			if d.CaptureDir == "" {
				return deps.InvalidInput("document %q is not a browser capture", args[0])
			}
			v, err := readCaptureView(d.CaptureDir)
			if err != nil {
				return err
			}
			if deps.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\n\n%s\n", v.Title, v.URL, v.Readable)
			return nil
		},
	}
}

// readCaptureView reads one capture envelope. Missing parts are left empty
// rather than failing: a capture keeps whatever the page allowed.
func readCaptureView(dir string) (*captureView, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("reading capture: %w", err)
	}
	var meta struct {
		Title      string `json:"title"`
		URL        string `json:"url"`
		CapturedAt string `json:"capturedAt"`
		WordCount  int    `json:"wordCount"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("reading capture meta.json: %w", err)
	}
	v := &captureView{Title: meta.Title, URL: meta.URL, CapturedAt: meta.CapturedAt, WordCount: meta.WordCount}

	for name, dst := range map[string]*string{"readable.md": &v.Readable, capturesummary.SummaryFile: &v.Summary, "transcript.md": &v.Transcript} {
		if text, err := readCapped(filepath.Join(dir, name), maxCaptureReadableBytes); err == nil {
			*dst = string(text)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if st, err := capturesummary.ReadStatus(dir); err == nil {
		v.SummaryStatus = &captureSummaryStatus{
			Status: capturesummary.StateOf(dir, time.Now()), Runtime: st.Runtime, Error: st.Error, FinishedAt: st.FinishedAt,
		}
	} else if v.Summary != "" {
		v.SummaryStatus = &captureSummaryStatus{Status: capturesummary.StateDone}
	}
	if png, err := readCapped(filepath.Join(dir, "screenshot.png"), maxCaptureScreenshotBytes); err == nil {
		v.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return v, nil
}

func readCapped(path string, limit int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is too large to preview (%d MB)", filepath.Base(path), fi.Size()/1024/1024)
	}
	return os.ReadFile(path)
}
