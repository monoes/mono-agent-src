package chatevents

import (
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ComputeTurnStatus maps a turn's outcome to its terminal TurnStatus and a
// human-readable reason, implementing the plan's exact precedence (in
// order):
//
//  1. A Stop accepted before finalization gives cancelled.
//  2. Fatal error, result is_error, or nonzero protocol/process exit (all
//     folded into res.Err by internal/monomind's Exec) gives failed.
//  3. Missing terminal protocol evidence on otherwise clean EOF (no done
//     event ever seen) gives interrupted.
//  4. Valid terminal evidence with no failure gives completed, preserving
//     the protocol's stop reason — max_turns/tool_round_cap are reported as
//     a limit reached, never plain success.
//
// stopRequested must be true for a turn stopped for ANY reason the
// supervisor initiated (an explicit StopChatTurn call or app shutdown
// cancelling registered turns) — this function does not distinguish why
// stop was requested, only that it was, before res arrived.
func ComputeTurnStatus(stopRequested bool, res *monomind.TurnResult) (status TurnStatus, reason string) {
	if stopRequested {
		return StatusCancelled, "stopped"
	}
	if res == nil {
		return StatusInterrupted, "no terminal result was ever produced"
	}
	if res.Err != nil {
		return StatusFailed, res.Err.Error()
	}
	if !res.SawDone {
		return StatusInterrupted, "process ended without a terminal protocol event"
	}
	switch res.StopReason {
	case monomind.StopMaxTurns:
		return StatusCompleted, "limit reached (max_turns)"
	case monomind.StopToolRoundCap:
		return StatusCompleted, "limit reached (tool_round_cap)"
	default:
		return StatusCompleted, res.StopReason
	}
}

// Coalescing bounds (plan §246): text is buffered for at most 50ms or 16KB,
// whichever comes first, before it must be flushed as one assistant.delta
// event — independent of that timer, a tool/session/terminal boundary
// always flushes whatever is pending first.
const (
	CoalesceWindow   = 50 * time.Millisecond
	CoalesceMaxBytes = 16 * 1024
)

// TextCoalescer buffers adjacent assistant text under one text-part ID so a
// token-by-token stream doesn't allocate a sequence number and a store
// write per token. It is a pure, synchronous, single-goroutine type: the
// caller's own event loop drives time and decides when to check
// ShouldFlush/Flush — TextCoalescer never starts a timer or goroutine of
// its own, keeping this leaf package free of any background state.
type TextCoalescer struct {
	partID    string
	buf       []byte
	openSince time.Time
}

// NewTextCoalescer returns an empty coalescer.
func NewTextCoalescer() *TextCoalescer {
	return &TextCoalescer{}
}

// Push appends text under partID and reports at most one flush the push
// itself triggered — the caller need not separately poll ShouldFlush/Flush
// after every Push (though it still must, on its own ticker, for the case
// where the 50ms window elapses with no new text arriving at all).
//
// Two situations trigger a flush, checked in order: (1) a different partID
// is already open (a tool call or other boundary should have flushed
// first, but this is defensive rather than trusting every caller path) —
// the previously open text flushes before the new text starts a fresh
// part, never silently concatenating two different parts under one ID; (2)
// after appending, the buffer alone (time or size) already crosses the
// coalescing bound. Only one of these can be reported per call — the rare
// case of a single push both interrupting a pending part AND alone
// exceeding the size bound self-heals on the caller's next Push/tick
// rather than needing this to return two flushes at once.
func (c *TextCoalescer) Push(partID, text string, now time.Time) (flushedPartID, flushedText string, flushed bool) {
	if len(c.buf) > 0 && c.partID != partID {
		flushedPartID, flushedText, flushed = c.forceFlush()
	}
	if len(c.buf) == 0 {
		c.partID = partID
		c.openSince = now
	}
	c.buf = append(c.buf, text...)
	if !flushed && c.ShouldFlush(now) {
		flushedPartID, flushedText, flushed = c.forceFlush()
	}
	return flushedPartID, flushedText, flushed
}

// ShouldFlush reports whether the coalescing window or size bound has been
// reached for whatever is currently buffered.
func (c *TextCoalescer) ShouldFlush(now time.Time) bool {
	if len(c.buf) == 0 {
		return false
	}
	return len(c.buf) >= CoalesceMaxBytes || now.Sub(c.openSince) >= CoalesceWindow
}

// Flush returns and clears the buffered text if ShouldFlush(now); a
// boundary event (tool start, session bound, terminal) must call
// ForceFlush instead, which flushes unconditionally.
func (c *TextCoalescer) Flush(now time.Time) (partID, text string, ok bool) {
	if !c.ShouldFlush(now) {
		return "", "", false
	}
	return c.forceFlush()
}

// ForceFlush flushes whatever is buffered regardless of the time/size
// bound — used at tool/session/terminal boundaries, which must always
// close out any pending text first (plan §246: "Tool/session/terminal
// boundaries flush pending text").
func (c *TextCoalescer) ForceFlush() (partID, text string, ok bool) {
	return c.forceFlush()
}

func (c *TextCoalescer) forceFlush() (partID, text string, ok bool) {
	if len(c.buf) == 0 {
		return "", "", false
	}
	partID, text = c.partID, string(c.buf)
	c.buf = nil
	c.partID = ""
	return partID, text, true
}

// Bounded output limits (plan §258-262).
const (
	MaxToolPreviewBytes = 16 * 1024
	MaxEventBytes       = 64 * 1024
)

// RedactAndBoundJSON applies workflow.RedactItems to a JSON object (tool
// call arguments or a parsed JSON result) and caps the result at
// MaxToolPreviewBytes. Non-object input (a JSON array, scalar, or invalid
// JSON) is not a redaction target — workflow.RedactItems only recognizes
// object keys — and is returned as bounded plain text instead via
// BoundText. A nil/empty input returns it unchanged.
func RedactAndBoundJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Not a JSON object — bound as text instead of attempting redaction
		// on a shape RedactItems does not recognize.
		bounded, _, _ := BoundText(string(raw), MaxToolPreviewBytes)
		return json.RawMessage(mustMarshalString(bounded))
	}
	redacted := workflow.RedactItems([]map[string]any{obj})[0]
	b, err := json.Marshal(redacted)
	if err != nil {
		return raw
	}
	if len(b) <= MaxToolPreviewBytes {
		return b
	}
	bounded, _, _ := BoundText(string(b), MaxToolPreviewBytes)
	return json.RawMessage(mustMarshalString(bounded))
}

// BoundText truncates s to at most maxBytes, cutting on a UTF-8 rune
// boundary so the retained prefix is never invalid UTF-8 (per plan §262:
// "a UTF-8-safe prefix"). Returns the (possibly unmodified) text, whether
// truncation happened, and the original byte count — callers surface the
// original count alongside truncated:true rather than silently discarding
// how much was cut.
func BoundText(s string, maxBytes int) (bounded string, truncated bool, originalBytes int) {
	originalBytes = len(s)
	if originalBytes <= maxBytes {
		return s, false, originalBytes
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true, originalBytes
}

// mustMarshalString JSON-encodes a Go string. json.Marshal on a string
// value cannot fail (no cycles, no unsupported types), so a returned error
// is unreachable; the empty-object fallback exists only so this never
// panics if that invariant is ever violated by a future Go change.
func mustMarshalString(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte(`""`)
	}
	return b
}

// MaxToolFieldBytes caps each string field of a native tool's input, so a
// large Edit or Write keeps its object shape (file_path and all) instead of
// collapsing into one truncated string the way RedactAndBoundJSON would.
const MaxToolFieldBytes = 6 * 1024

// RedactAndBoundFields is RedactAndBoundJSON for tool input objects: it
// redacts, then bounds each top-level string field on its own. truncated
// reports whether any field was cut. Input that still does not fit, or is
// not an object, falls back to RedactAndBoundJSON.
func RedactAndBoundFields(raw json.RawMessage) (out json.RawMessage, truncated bool) {
	if len(raw) == 0 {
		return raw, false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return RedactAndBoundJSON(raw), len(raw) > MaxToolPreviewBytes
	}
	redacted := workflow.RedactItems([]map[string]any{obj})[0]
	for k, v := range redacted {
		if str, ok := v.(string); ok {
			if bounded, cut, _ := BoundText(str, MaxToolFieldBytes); cut {
				redacted[k] = bounded
				truncated = true
			}
		}
	}
	b, err := json.Marshal(redacted)
	if err != nil || len(b) > MaxToolPreviewBytes {
		return RedactAndBoundJSON(raw), true
	}
	return b, truncated
}

// ModelUsageOf is a result's per-model counters in the journal's form; nil when it had none.
func ModelUsageOf(models map[string]monomind.ModelUsage) map[string]CoderModelUsage {
	if len(models) == 0 {
		return nil
	}
	out := make(map[string]CoderModelUsage, len(models))
	for name, m := range models {
		out[name] = CoderModelUsage{Input: m.Input, Output: m.Output, CacheRead: m.CacheRead, CacheCreation: m.CacheCreation, Partial: m.Partial}
	}
	return out
}
