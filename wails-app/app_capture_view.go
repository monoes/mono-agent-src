// wails-app/app_capture_view.go
package main

// CaptureView is everything the Documents page shows for one browser
// capture: the page's readable text and its screenshot, side by side, plus
// the AI summary and the video transcript for captures saved with "Save
// page summary" / "Save video summary".
// A capture's primary file is often page.mhtml (no readable text was
// found), which no in-app viewer can show and which the OS tends to hand
// to a text editor — so the capture is previewed from its parts instead.
type CaptureView struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	CapturedAt string `json:"captured_at"`
	WordCount  int    `json:"word_count"`
	Readable   string `json:"readable"`   // readable.md, "" when absent
	Screenshot string `json:"screenshot"` // data URL of screenshot.png, "" when absent
	Summary    string `json:"summary"`    // summary.md, "" when absent
	Transcript string `json:"transcript"` // transcript.md, "" when absent
	// SummaryStatus is where the summary is, when one was asked for
	// (summary.json); nil for a capture that never asked.
	SummaryStatus *CaptureSummaryStatus `json:"summary_status"`
}

// CaptureSummaryStatus is summary.json, as the viewer needs it. Status is
// pending, running, done, error, or stalled (pending/running for longer
// than any bridge could still be working on it).
type CaptureSummaryStatus struct {
	Status     string `json:"status"`
	Runtime    string `json:"runtime"`
	Error      string `json:"error"`
	FinishedAt string `json:"finished_at"`
	Sandbox    string `json:"sandbox,omitempty"` // set when the summary's turn ran without a sandbox
}

// GetCaptureView loads a capture document's parts, scoped to the active
// profile like every other document read (`profile documents capture`).
func (a *App) GetCaptureView(id string) (*CaptureView, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var v CaptureView
	if err := a.cliJSON(profileCLITimeout, &v, "profile", "documents", "capture", id); err != nil {
		return nil, err
	}
	return &v, nil
}
