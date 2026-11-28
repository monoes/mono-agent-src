// Package a2ui implements the agent side of Google's A2UI v1.0 protocol for
// mono-agent: parsing the agent-to-renderer messages, validating them against
// our own small trusted catalog with hard size caps, tracking each live
// surface, and checking that an action coming back really belongs to a button
// the surface declared. It has no UI, database or transport dependencies.
//
// Spec: https://a2ui.org/specification/v1.0-a2ui/ . Only the subset below is
// accepted; everything else is rejected, never ignored.
package a2ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Version is the protocol version carried by every envelope.
const Version = "1.0"

// CatalogID names our catalog. A createSurface that names another one is rejected.
const CatalogID = "https://monoes.me/a2ui/catalogs/mono/v1/catalog.json"

// Kind is the payload key of an agent-to-renderer envelope.
type Kind string

const (
	KindCreateSurface    Kind = "createSurface"
	KindUpdateComponents Kind = "updateComponents"
	KindUpdateDataModel  Kind = "updateDataModel"
	KindDeleteSurface    Kind = "deleteSurface"
)

// Provenance says who authored a surface. System surfaces are built by our
// own code from a real record (a HIL item, a mission); agent surfaces come
// from the model and may not use the reserved action names.
type Provenance string

const (
	FromAgent  Provenance = "agent"
	FromSystem Provenance = "system"
)

// ReservedActionPrefixes are action-name prefixes only system surfaces may declare.
var ReservedActionPrefixes = []string{"hil.", "mission.", "system."}

// SystemSurfacePrefixes start the ids of the cards the host builds
// (internal/a2ui/surfaces: approvals, questions, missions). No agent message
// may name one, so no agent can make, change or remove such a card.
var SystemSurfacePrefixes = []string{"hil-", "send-", "ask-", "mission-"}

// IsSystemSurfaceID says whether id is one of the host's card ids.
func IsSystemSurfaceID(id string) bool {
	return slices.ContainsFunc(SystemSurfacePrefixes, func(p string) bool { return strings.HasPrefix(id, p) })
}

// MaxNextTurnBytes is the longest message a chat takes as its next turn on every
// path (the side panel's chat.send; the app's command line holds more). A press
// that would make a longer one is refused before anything of it is delivered.
const MaxNextTurnBytes = 16 << 10

// Limits bound everything an untrusted surface can make us hold or draw.
type Limits struct {
	MaxMessageBytes   int // one raw envelope
	MaxComponents     int // per surface
	MaxDepth          int // nesting depth below root
	MaxStringBytes    int // any literal string
	MaxOptions        int // ChoicePicker options
	MaxDataModelBytes int // marshalled data model
	MaxChildren       int // per container
	MaxActionBytes    int // one action coming back from the renderer
	MaxSetActions     int // actions, and skipped cards, of one set sent together
	MaxSetBytes       int // one set coming back from the renderer
	MaxReturnedString int // one string value in a returned action (edited text)
}

// DefaultLimits are the production caps.
var DefaultLimits = Limits{
	MaxMessageBytes: 64 << 10, MaxComponents: 80, MaxDepth: 8, MaxStringBytes: 4000,
	MaxOptions: 20, MaxDataModelBytes: 32 << 10, MaxChildren: 40,
	MaxActionBytes: 48 << 10, MaxReturnedString: 24 << 10,
	MaxSetActions: 2 * MaxSurfaces, MaxSetBytes: 192 << 10,
}

// Message is one parsed agent-to-renderer envelope.
type Message struct {
	Kind          Kind
	SurfaceID     string
	CatalogID     string
	SendDataModel bool
	Components    []map[string]any // createSurface (optional) and updateComponents
	DataModel     any              // createSurface (optional)
	Path          string           // updateDataModel, "/" when omitted
	Value         any              // updateDataModel; nil means delete
	HasValue      bool
}

// ParseMessage decodes and validates the envelope shape of one message. It
// does not check components against the catalog; Surface.Apply does that.
func ParseMessage(raw []byte, lim Limits) (Message, error) {
	if len(raw) > lim.MaxMessageBytes {
		return Message{}, fmt.Errorf("a2ui: message is %d bytes, over the %d byte cap", len(raw), lim.MaxMessageBytes)
	}
	env, err := decodeObject(raw)
	if err != nil {
		return Message{}, fmt.Errorf("a2ui: %w", err)
	}
	if v, _ := env["version"].(string); v != Version {
		return Message{}, fmt.Errorf("a2ui: version must be %q", Version)
	}
	var kind Kind
	var body map[string]any
	for k, v := range env {
		if k == "version" {
			continue
		}
		if kind != "" {
			return Message{}, errors.New("a2ui: envelope must carry exactly one payload key")
		}
		kind = Kind(k)
		var ok bool
		if body, ok = v.(map[string]any); !ok {
			return Message{}, fmt.Errorf("a2ui: %s payload must be an object", k)
		}
	}
	m := Message{Kind: kind}
	if m.SurfaceID, err = surfaceID(body); err != nil {
		return Message{}, err
	}
	switch kind {
	case KindCreateSurface:
		err = parseCreate(&m, body)
	case KindUpdateComponents:
		err = onlyKeys(body, "surfaceId", "components")
		if err == nil {
			m.Components, err = componentList(body["components"], true)
		}
	case KindUpdateDataModel:
		err = parseUpdateModel(&m, body)
	case KindDeleteSurface:
		err = onlyKeys(body, "surfaceId")
	default:
		err = fmt.Errorf("unsupported message %q", kind)
	}
	if err != nil {
		return Message{}, fmt.Errorf("a2ui: %w", err)
	}
	return m, nil
}

func parseCreate(m *Message, body map[string]any) error {
	if err := onlyKeys(body, "surfaceId", "catalogId", "sendDataModel", "components", "dataModel"); err != nil {
		return err
	}
	if id, ok := body["catalogId"]; ok && id != CatalogID {
		return fmt.Errorf("catalogId %v is not the supported catalog", id)
	}
	m.CatalogID = CatalogID
	m.SendDataModel, _ = body["sendDataModel"].(bool)
	if c, ok := body["components"]; ok {
		var err error
		if m.Components, err = componentList(c, true); err != nil {
			return err
		}
	}
	if d, ok := body["dataModel"]; ok {
		m.DataModel = d
	}
	return nil
}

func parseUpdateModel(m *Message, body map[string]any) error {
	if err := onlyKeys(body, "surfaceId", "path", "value"); err != nil {
		return err
	}
	m.Path = "/"
	if p, ok := body["path"]; ok {
		s, isStr := p.(string)
		if !isStr || !validPointer(s) {
			return errors.New("path must be a JSON Pointer")
		}
		m.Path = s
	}
	v, has := body["value"]
	if !has {
		return errors.New("updateDataModel needs a value")
	}
	m.Value, m.HasValue = v, v != nil
	return nil
}

func surfaceID(body map[string]any) (string, error) {
	id, _ := body["surfaceId"].(string)
	if id == "" || len(id) > 120 || strings.ContainsAny(id, " \t\r\n/\\") {
		return "", errors.New("a2ui: surfaceId must be a short token with no spaces or slashes")
	}
	return id, nil
}

func componentList(v any, nonEmpty bool) ([]map[string]any, error) {
	arr, ok := v.([]any)
	if !ok || (nonEmpty && len(arr) == 0) {
		return nil, errors.New("components must be a non-empty array")
	}
	out := make([]map[string]any, 0, len(arr))
	for i, it := range arr {
		c, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("components[%d] must be an object", i)
		}
		out = append(out, c)
	}
	return out, nil
}

func onlyKeys(body map[string]any, allowed ...string) error {
outer:
	for k := range body {
		for _, a := range allowed {
			if k == a {
				continue outer
			}
		}
		return fmt.Errorf("unknown field %q", k)
	}
	return nil
}

// decodeObject parses one JSON object, keeping numbers exact and refusing
// trailing data.
func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON object")
	}
	return obj, nil
}

// validPointer reports whether s is an RFC 6901 JSON Pointer ("" or "/a/b").
func validPointer(s string) bool {
	if s == "" {
		return true
	}
	if s[0] != '/' || len(s) > 200 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' && (i+1 >= len(s) || (s[i+1] != '0' && s[i+1] != '1')) {
			return false
		}
	}
	return true
}
