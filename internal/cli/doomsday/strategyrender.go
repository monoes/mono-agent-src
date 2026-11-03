package doomsday

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The read-only views of the strategy layer (`doomsday strategy status|epochs|directions`). They are plain data with no
// import of the strategy packages, so they exist in every build; only the tagged wiring can fill them. They are redacted
// by construction: a direction shows a capped title and no rationale, an epoch shows hashes and enums, never a judgment
// body or a note. Every list is bounded.

// Output bounds.
const (
	StrategyDefaultLimit = 20
	StrategyMaxLimit     = 100
	strategyTitleCap     = 80
	strategyPinCap       = 80
	strategyNoteCap      = 160
)

// StrategyStatusView summarises the strategy state of one generation.
type StrategyStatusView struct {
	Generation   int64          `json:"generation"`
	RunState     string         `json:"run_state,omitempty"`
	Epochs       int            `json:"epochs"`
	LatestEpoch  *EpochView     `json:"latest_epoch,omitempty"`
	Directions   map[string]int `json:"directions,omitempty"` // state -> count
	ParkedFinish int            `json:"parked_finish_rounds"`
	Criteria     map[string]int `json:"criteria,omitempty"` // judge reading -> count, from the latest judgment
	Tampered     bool           `json:"tampered,omitempty"`
	Note         string         `json:"note,omitempty"`
}

// EpochView is one epoch without its judgment text.
type EpochView struct {
	Seq          int64  `json:"seq"`
	Trigger      string `json:"trigger"`
	State        string `json:"state"`
	StartedAt    string `json:"started_at"`
	ModelPin     string `json:"model_pin,omitempty"`
	JudgmentHash string `json:"judgment_hash,omitempty"` // first 12 hex
}

// StrategyEpochsView is the newest-last list of epochs, at most Limit.
type StrategyEpochsView struct {
	Generation int64       `json:"generation"`
	Total      int         `json:"total"`
	Epochs     []EpochView `json:"epochs"`
	Truncated  bool        `json:"truncated,omitempty"`
	Tampered   bool        `json:"tampered,omitempty"`
	Note       string      `json:"note,omitempty"`
}

// DirectionView is one direction with a capped title and no rationale.
type DirectionView struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	Title        string `json:"title"`
	Proposer     string `json:"proposer,omitempty"`
	Session      string `json:"session,omitempty"`
	CreatedEpoch int64  `json:"created_epoch"`
	Key          string `json:"key"` // first 12 hex of the key hash
}

// StrategyDirectionsView is the list of directions, at most Limit.
type StrategyDirectionsView struct {
	Generation int64           `json:"generation"`
	Total      int             `json:"total"`
	Directions []DirectionView `json:"directions"`
	Truncated  bool            `json:"truncated,omitempty"`
	Tampered   bool            `json:"tampered,omitempty"`
	Note       string          `json:"note,omitempty"`
}

// StrategyReader is the whole surface of the verbs: three reads. It has no method that writes; a guard test pins that.
// gen 0 means the generation of the open run (or the newest generation when none is open).
type StrategyReader interface {
	ReadStatus(ctx context.Context, gen int64) (StrategyStatusView, error)
	ReadEpochs(ctx context.Context, gen int64, limit int) (StrategyEpochsView, error)
	ReadDirections(ctx context.Context, gen int64, limit int) (StrategyDirectionsView, error)
}

// StrategyDeps is what the verbs need. A nil Reader (the default build, or a build whose engine carries no strategy
// layer) is the layer being off: every verb says so and reads nothing.
type StrategyDeps struct {
	Reader     func() StrategyReader
	JSONOutput func() bool
}

func (d StrategyDeps) reader() StrategyReader {
	if d.Reader == nil {
		return nil
	}
	return d.Reader()
}

func (d StrategyDeps) json() bool { return d.JSONOutput != nil && d.JSONOutput() }

// StrategyTamperedNote is shown when a stored row failed its MAC. The rows are not shown, and the run is not affected.
const StrategyTamperedNote = "a stored strategy row failed verification: its content is not shown and the strategy layer is parked; the run itself is not affected"

// RedactText makes untrusted text safe to print on a terminal: control and format characters become spaces, runs of
// space collapse, and the result is capped at max runes with an ellipsis.
func RedactText(s string, max int) string {
	var out []rune
	space := false
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			space = len(out) > 0
			continue
		}
		if space {
			out = append(out, ' ')
			space = false
		}
		out = append(out, r)
		if len(out) > max {
			break
		}
	}
	if len(out) > max {
		return string(out[:max]) + "..."
	}
	return string(out)
}

// ClampStrategyLimit applies the default and the maximum.
func ClampStrategyLimit(n int) int {
	switch {
	case n <= 0:
		return StrategyDefaultLimit
	case n > StrategyMaxLimit:
		return StrategyMaxLimit
	}
	return n
}

// ShortHex is the first n hex characters of h.
func ShortHex(h string, n int) string {
	if len(h) <= n {
		return h
	}
	return h[:n]
}

// StampTime renders unix seconds in UTC.
func StampTime(sec int64) string { return time.Unix(sec, 0).UTC().Format(time.RFC3339) }

func writeJSON(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

func renderOff(w io.Writer, asJSON bool) error {
	if asJSON {
		return writeJSON(w, map[string]any{"off": true})
	}
	_, err := fmt.Fprintln(w, "off")
	return err
}

func renderStrategyStatus(w io.Writer, v StrategyStatusView) error {
	if v.Tampered {
		_, err := fmt.Fprintf(w, "generation %d: TAMPERED. %s\n", v.Generation, StrategyTamperedNote)
		return err
	}
	fmt.Fprintf(w, "generation %d (run %s): %d epochs, %d parked finish rounds\n", v.Generation, orDash(v.RunState), v.Epochs, v.ParkedFinish)
	if e := v.LatestEpoch; e != nil {
		fmt.Fprintf(w, "latest epoch %d: %s, %s, started %s\n", e.Seq, e.Trigger, e.State, e.StartedAt)
	}
	fmt.Fprintf(w, "directions: %s\n", counts(v.Directions))
	if len(v.Criteria) > 0 {
		fmt.Fprintf(w, "criteria (judge reading, not verified): %s\n", counts(v.Criteria))
	}
	if v.Note != "" {
		fmt.Fprintln(w, RedactText(v.Note, strategyNoteCap))
	}
	return nil
}

func renderStrategyEpochs(w io.Writer, v StrategyEpochsView) error {
	if v.Tampered {
		_, err := fmt.Fprintf(w, "generation %d: TAMPERED. %s\n", v.Generation, StrategyTamperedNote)
		return err
	}
	fmt.Fprintf(w, "generation %d: %d epochs%s\n", v.Generation, v.Total, truncNote(v.Truncated))
	for _, e := range v.Epochs {
		fmt.Fprintf(w, "  %4d  %-20s %-13s %s  %s %s\n", e.Seq, e.Trigger, e.State, e.StartedAt, orDash(e.JudgmentHash), RedactText(e.ModelPin, strategyPinCap))
	}
	return nil
}

func renderStrategyDirections(w io.Writer, v StrategyDirectionsView) error {
	if v.Tampered {
		_, err := fmt.Fprintf(w, "generation %d: TAMPERED. %s\n", v.Generation, StrategyTamperedNote)
		return err
	}
	fmt.Fprintf(w, "generation %d: %d directions%s\n", v.Generation, v.Total, truncNote(v.Truncated))
	for _, d := range v.Directions {
		fmt.Fprintf(w, "  %s  %-9s epoch %d  %s  %s\n", d.ID, d.State, d.CreatedEpoch, d.Key, RedactText(d.Title, strategyTitleCap))
	}
	return nil
}

func truncNote(t bool) string {
	if t {
		return " (showing the newest only; use --limit)"
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", RedactText(k, 24), m[k])
	}
	return strings.Join(parts, ", ")
}
