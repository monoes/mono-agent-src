package surfaces

import "github.com/monoes/mono-agent/internal/a2ui"

// Resolved returns the envelope that removes a surface.
func Resolved(surfaceID string) ([][]byte, error) {
	if err := checkID("surface", surfaceID); err != nil {
		return nil, err
	}
	raw, err := marshal(map[string]any{"version": a2ui.Version, "deleteSurface": map[string]any{"surfaceId": surfaceID}})
	if err != nil {
		return nil, err
	}
	return [][]byte{raw}, nil
}

// ResolvedNotice returns the messages that replace a surface with a
// button-less notice ("Approved by you") so the history stays readable. It
// deletes the surface and creates a new one under the same id, so no old
// button survives for a stale action to match; tone is a Badge tone (an
// unknown one becomes "neutral"). The text is cut to 300 bytes.
func ResolvedNotice(surfaceID, text, tone string) ([][]byte, error) {
	del, err := Resolved(surfaceID)
	if err != nil {
		return nil, err
	}
	switch tone {
	case "neutral", "info", "success", "warning", "danger":
	default:
		tone = "neutral"
	}
	b := newBuilder()
	b.badge("note", elide(text, 300), tone)
	b.add("root", "Card", map[string]any{"child": "note"})
	create, err := b.create(surfaceID)
	if err != nil {
		return nil, err
	}
	return append(del, create...), nil
}
