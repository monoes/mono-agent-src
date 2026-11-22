// Package capturesummary writes an AI summary beside a browser capture.
//
// A capture asks for one by carrying `summarize` in its meta.json (the
// extension's "Save page summary" and "Save video summary" menu items set
// {"kind": "page"} or {"kind": "video"}). The bridge writes the envelope
// and acknowledges the capture exactly as it always has; only then, in the
// background and one at a time, is the configured agent runtime asked for a
// summary, which lands as summary.md in the same envelope directory.
//
// The capture never depends on the summary. Whatever happens to it —
// the runtime is missing, the model times out, the process dies — is
// recorded in summary.json next to it (see Status), so the app can say
// "pending", "done" or what went wrong instead of showing nothing.
package capturesummary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// File names inside an envelope directory.
const (
	SummaryFile = "summary.md"
	StatusFile  = "summary.json"
)

// Summary kinds, from meta.summarize.kind.
const (
	KindPage  = "page"
	KindVideo = "video"
)

// Status values recorded in summary.json.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
)

// Status is summary.json: where a capture's summary is, and how it got
// there. Times are RFC 3339 UTC.
type Status struct {
	Status      string  `json:"status"`
	Kind        string  `json:"kind"`
	Runtime     string  `json:"runtime,omitempty"`
	Model       string  `json:"model,omitempty"` // --model passed; empty is the runtime's default
	RequestedAt string  `json:"requestedAt,omitempty"`
	StartedAt   string  `json:"startedAt,omitempty"`
	FinishedAt  string  `json:"finishedAt,omitempty"`
	Error       string  `json:"error,omitempty"`
	Source      string  `json:"source,omitempty"`     // the artifact summarized, e.g. transcript.md
	InputChars  int     `json:"inputChars,omitempty"` // characters of it sent
	Truncated   bool    `json:"truncated,omitempty"`  // whether it was cut to fit
	CostUSD     float64 `json:"costUsd,omitempty"`
}

// Requested reports whether a capture's meta asks for a summary, and of
// which kind. A bare `true` means a page summary; an unknown kind falls
// back to page rather than being ignored.
func Requested(meta capture.Meta) (string, bool) {
	req, ok := RequestOf(meta)
	return req.Kind, ok
}

// Request is a capture's meta.summarize: the kind of summary, and
// optionally which runtime and model should write it (the extension's
// "AI for summaries" choice). Runtime and Model are as the browser sent
// them — unchecked until Summarizer.resolve.
type Request struct {
	Kind    string
	Runtime string
	Model   string
}

// RequestOf parses meta.summarize. ok is false when no summary was asked for.
func RequestOf(meta capture.Meta) (Request, bool) {
	raw, ok := meta.Extra["summarize"]
	if !ok {
		return Request{}, false
	}
	var flag bool
	if json.Unmarshal(raw, &flag) == nil {
		if flag {
			return Request{Kind: KindPage}, true
		}
		return Request{}, false
	}
	var obj struct {
		Kind    string `json:"kind"`
		Runtime string `json:"runtime"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Request{}, false
	}
	req := Request{Kind: KindPage, Runtime: strings.TrimSpace(obj.Runtime), Model: strings.TrimSpace(obj.Model)}
	if strings.TrimSpace(obj.Kind) == KindVideo {
		req.Kind = KindVideo
	}
	return req, true
}

// ReadStatus reads a capture's summary.json. os.ErrNotExist (wrapped) means
// no summary was ever asked for.
func ReadStatus(dir string) (*Status, error) {
	blob, err := capture.ReadCapped(filepath.Join(dir, StatusFile), capture.MaxMetaFileBytes)
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(blob, &st); err != nil {
		return nil, fmt.Errorf("decode %s: %w", StatusFile, err)
	}
	return &st, nil
}

// writeStatus replaces summary.json atomically, so a reader never sees half
// of one.
func writeStatus(dir string, st Status) error {
	blob, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(dir, StatusFile, append(blob, '\n'))
}

// writeAtomic writes name inside dir via a dot-prefixed temp file and a
// rename. Dot-prefixed, so a capture listing never picks up the temp file.
func writeAtomic(dir, name string, data []byte) error {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("capture %s is gone", dir)
		}
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// StaleAfter is how long a summary may sit pending or running before a
// reader should treat it as abandoned (the bridge that owned it exited).
const StaleAfter = 30 * time.Minute

// Stalled reports whether a pending or running status has outlived any
// bridge that could still be working on it.
func (s *Status) Stalled(now time.Time) bool {
	if s == nil || (s.Status != StatePending && s.Status != StateRunning) {
		return false
	}
	at := s.StartedAt
	if at == "" {
		at = s.RequestedAt
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return false
	}
	return now.Sub(t) > StaleAfter
}

// StateStalled is what StateOf reports for a pending or running summary
// whose bridge can no longer be working on it (see Status.Stalled).
const StateStalled = "stalled"

// StateOf is a capture directory's summary state for a listing: "" when no
// summary was asked for, else one of the State* values, with an abandoned
// pending/running one reported as StateStalled. A summary.md with no
// summary.json (written by hand, or by an older bridge) counts as done.
func StateOf(dir string, now time.Time) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	st, err := ReadStatus(dir)
	if err != nil {
		if _, serr := os.Stat(filepath.Join(dir, SummaryFile)); serr == nil {
			return StateDone
		}
		return ""
	}
	if st.Stalled(now) {
		return StateStalled
	}
	return st.Status
}
