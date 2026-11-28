// Package surfaces builds the A2UI surfaces the host itself shows: workflow
// approvals, send approvals, structured questions and mission cards. Every
// builder is pure (no I/O, no database, no UI) and turns one real record into
// raw v1.0 envelopes ready for a2ui.Registry.Handle(raw, a2ui.FromSystem).
//
// Every builder checks its own output through a fresh Registry and
// Surface.Check before returning, so a caller never gets a surface the
// renderer would refuse: it gets an error instead.
//
// A card that offers to approve an action shows everything the person
// approves. When any of it (a value, a row, text the person may edit and send
// back) would have to be cut or left out, the workflow and send approval
// builders return ErrTooLarge and the person decides on the classic card.
// Cards that cannot approve (settled or reject-only sends, notices) and the
// mission and question cards cut over-long read-only text with the marker
// Truncated (on a rune boundary, and the cap shrinks step by step if the
// whole message would otherwise be over the size cap).
package surfaces

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/a2ui"
)

// Truncated is appended to read-only text that was cut to fit a cap.
const Truncated = "… [truncated]"

// ErrTooLarge means the record cannot be shown as an approval surface within
// the caps without hiding something the person would approve.
var ErrTooLarge = errors.New("surfaces: record does not fit the surface caps")

// errOversize is internal: the envelope is over the message cap, retry with
// shorter read-only text.
var errOversize = errors.New("surfaces: envelope over the message cap")

// DigestKey is the context key under which an approval card's buttons carry the
// digest of the item as the card shows it: a press decides the item only while
// it still has it.
const DigestKey = "_digest"

// decisionContext is the context of a button that decides item id.
func decisionContext(id, digest string) map[string]any {
	ctx := map[string]any{"id": id}
	if digest != "" {
		ctx[DigestKey] = digest
	}
	return ctx
}

// Button component ids, fixed per surface kind.
const (
	btnApprove  = "btn-approve"
	btnReject   = "btn-reject"
	btnEdit     = "btn-edit"
	btnStop     = "btn-stop"
	btnClose    = "btn-close"
	btnContinue = "btn-continue"
)

// checkID accepts a short token of letters, digits and _ . : - only.
func checkID(kind, id string) error {
	ok := id != "" && len(id) <= 100
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r)) {
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("surfaces: %s id %q is not a short token", kind, id)
	}
	return nil
}

// elide returns s cut to at most max bytes (marker included) on a rune
// boundary. Invalid UTF-8 is replaced first, since JSON would do it anyway
// and grow the string.
func elide(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= max {
		return s
	}
	cut := max - len(Truncated)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + Truncated
}

type builder struct {
	comps []map[string]any
	data  map[string]any
	cut   bool // fit had to cut something
}

func newBuilder() *builder { return &builder{data: map[string]any{}} }

// fit is elide that remembers a cut, for whole.
func (b *builder) fit(s string, max int) string {
	if len(strings.ToValidUTF8(s, "\uFFFD")) > max {
		b.cut = true
	}
	return elide(s, max)
}

// whole refuses a card that would offer to approve text it does not show.
func (b *builder) whole() error {
	if b.cut {
		return fmt.Errorf("%w: it has text too long to show whole, and a card offers Approve only when it shows everything", ErrTooLarge)
	}
	return nil
}

func (b *builder) add(id, typ string, props map[string]any) {
	c := map[string]any{"id": id, "component": typ}
	for k, v := range props {
		c[k] = v
	}
	b.comps = append(b.comps, c)
}

func (b *builder) text(id, s, variant string) {
	p := map[string]any{"text": s}
	if variant != "" {
		p["variant"] = variant
	}
	b.add(id, "Text", p)
}

func (b *builder) boundText(id, path, variant string) {
	p := map[string]any{"text": bind(path)}
	if variant != "" {
		p["variant"] = variant
	}
	b.add(id, "Text", p)
}

func (b *builder) badge(id, s, tone string) {
	b.add(id, "Badge", map[string]any{"text": s, "tone": tone})
}

func (b *builder) column(id string, children ...string) {
	b.add(id, "Column", map[string]any{"children": strs(children)})
}

func (b *builder) row(id string, children ...string) {
	b.add(id, "Row", map[string]any{"children": strs(children)})
}

// button adds a Button and its label (id+"-l").
func (b *builder) button(id, label, variant, name string, ctx map[string]any) {
	b.text(id+"-l", label, "")
	ev := map[string]any{"name": name}
	if len(ctx) > 0 {
		ev["context"] = ctx
	}
	b.add(id, "Button", map[string]any{"child": id + "-l", "variant": variant, "action": map[string]any{"event": ev}})
}

func bind(path string) map[string]any { return map[string]any{"@path": path} }

func strs(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// pointerKey escapes one key for use inside a JSON Pointer.
func pointerKey(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// create wraps the builder's components and data model in a createSurface
// envelope, then proves it: through a real registry as a system surface and
// through Surface.Check.
func (b *builder) create(surfaceID string) ([][]byte, error) {
	comps := make([]any, len(b.comps))
	for i, c := range b.comps {
		comps[i] = c
	}
	raw, err := marshal(map[string]any{"version": a2ui.Version, "createSurface": map[string]any{
		"surfaceId": surfaceID, "catalogId": a2ui.CatalogID, "components": comps, "dataModel": b.data}})
	if err != nil {
		return nil, fmt.Errorf("surfaces: %w", err)
	}
	return verify([][]byte{raw})
}

// verify runs messages through a fresh registry as a system author and
// checks the resulting surface is renderable.
func verify(msgs [][]byte) ([][]byte, error) {
	reg := a2ui.NewRegistry(a2ui.DefaultLimits)
	var last a2ui.Message
	for _, raw := range msgs {
		if len(raw) > a2ui.DefaultLimits.MaxMessageBytes {
			return nil, errOversize
		}
		m, err := reg.Handle(raw, a2ui.FromSystem)
		if err != nil {
			if strings.Contains(err.Error(), "data model is over") {
				return nil, fmt.Errorf("%w: %v", ErrTooLarge, err)
			}
			return nil, err
		}
		last = m
	}
	if last.Kind == a2ui.KindDeleteSurface {
		return msgs, nil
	}
	if err := reg.Surface(last.SurfaceID).Check(a2ui.DefaultLimits); err != nil {
		return nil, err
	}
	return msgs, nil
}

// shrinking calls build with a read-only text cap, trying shorter caps while
// the envelope is over the message size cap.
func shrinking(build func(textCap int) ([][]byte, error)) ([][]byte, error) {
	var err error
	for _, c := range []int{a2ui.DefaultLimits.MaxStringBytes, 1000, 250, 80} {
		var msgs [][]byte
		if msgs, err = build(c); !errors.Is(err, errOversize) {
			return msgs, err
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrTooLarge, err)
}

// editable checks a string a person may edit and send back.
func editable(what, s string) (string, error) {
	s = strings.ToValidUTF8(s, "�")
	if len(s) > a2ui.DefaultLimits.MaxReturnedString {
		return "", fmt.Errorf("%w: %s is %d bytes, over the %d byte limit on text sent back with an action",
			ErrTooLarge, what, len(s), a2ui.DefaultLimits.MaxReturnedString)
	}
	return s, nil
}

func money(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return math.Round(f*10000) / 10000
}
