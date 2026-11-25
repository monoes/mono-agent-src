package a2ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// MaxSurfaces caps the live surfaces one Registry holds.
const MaxSurfaces = 8

// Surface is the server-side state of one live surface: the merged component
// list and the data model. It exists to validate updates and to answer "did
// this surface declare that action".
type Surface struct {
	ID            string
	Prov          Provenance
	SendDataModel bool
	Components    map[string]map[string]any
	Data          any
}

// Registry tracks the live surfaces of one conversation.
type Registry struct {
	mu   sync.Mutex
	lim  Limits
	surf map[string]*Surface
}

// NewRegistry returns an empty registry with the given limits.
func NewRegistry(lim Limits) *Registry {
	return &Registry{lim: lim, surf: map[string]*Surface{}}
}

// Handle validates one raw message from an author of the given provenance and
// applies it. On any error the registry is unchanged.
func (r *Registry) Handle(raw []byte, prov Provenance) (Message, error) {
	m, err := ParseMessage(raw, r.lim)
	if err != nil {
		return Message{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.surf[m.SurfaceID]
	switch m.Kind {
	case KindCreateSurface:
		if s != nil {
			return Message{}, fmt.Errorf("a2ui: surface %q already exists", m.SurfaceID)
		}
		if len(r.surf) >= MaxSurfaces {
			return Message{}, fmt.Errorf("a2ui: more than %d live surfaces", MaxSurfaces)
		}
		s = &Surface{ID: m.SurfaceID, Prov: prov, SendDataModel: m.SendDataModel,
			Components: map[string]map[string]any{}, Data: map[string]any{}}
	case KindDeleteSurface:
		if s == nil {
			return Message{}, fmt.Errorf("a2ui: unknown surface %q", m.SurfaceID)
		}
		delete(r.surf, m.SurfaceID)
		return m, nil
	default:
		if s == nil {
			return Message{}, fmt.Errorf("a2ui: surface %q was not created", m.SurfaceID)
		}
		if s.Prov != prov {
			return Message{}, fmt.Errorf("a2ui: surface %q belongs to the %s author", m.SurfaceID, s.Prov)
		}
		s = s.clone()
	}
	if err := s.apply(m, r.lim); err != nil {
		return Message{}, err
	}
	r.surf[s.ID] = s
	return m, nil
}

// Surface returns a copy of a live surface, or nil.
func (r *Registry) Surface(id string) *Surface {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.surf[id]; s != nil {
		return s.clone()
	}
	return nil
}

func (s *Surface) clone() *Surface {
	c := *s
	c.Components = make(map[string]map[string]any, len(s.Components))
	for k, v := range s.Components {
		c.Components[k] = v
	}
	c.Data = deepCopy(s.Data)
	return &c
}

func (s *Surface) apply(m Message, lim Limits) error {
	if m.Components != nil {
		for _, c := range m.Components {
			if err := validateComponent(c, s.Prov, lim); err != nil {
				return fmt.Errorf("a2ui: %w", err)
			}
			s.Components[c["id"].(string)] = c
		}
		if len(s.Components) > lim.MaxComponents {
			return fmt.Errorf("a2ui: more than %d components", lim.MaxComponents)
		}
	}
	switch m.Kind {
	case KindCreateSurface:
		if m.DataModel != nil {
			s.Data = m.DataModel
		}
	case KindUpdateDataModel:
		d, err := setPointer(s.Data, m.Path, m.Value, m.HasValue)
		if err != nil {
			return fmt.Errorf("a2ui: %w", err)
		}
		s.Data = d
	}
	if b, err := json.Marshal(s.Data); err != nil || len(b) > lim.MaxDataModelBytes {
		return fmt.Errorf("a2ui: data model is over the %d byte cap", lim.MaxDataModelBytes)
	}
	return nil
}

// Check verifies the surface is renderable: a root, every reference
// resolves, the components form a tree (no cycles, and none referenced twice:
// shared children would make walking the surface exponential), depth within the
// cap. Call it when a turn's updates are complete, not between streamed messages.
func (s *Surface) Check(lim Limits) error {
	if _, ok := s.Components["root"]; !ok {
		return errors.New("a2ui: surface has no component with id \"root\"")
	}
	return s.walk("root", map[string]bool{}, map[string]bool{}, 0, lim)
}

// walk visits id once: path holds its ancestors, seen every component visited.
func (s *Surface) walk(id string, path, seen map[string]bool, depth int, lim Limits) error {
	if depth > lim.MaxDepth {
		return fmt.Errorf("a2ui: nesting deeper than %d", lim.MaxDepth)
	}
	if path[id] {
		return fmt.Errorf("a2ui: component %q contains itself", id)
	}
	if seen[id] {
		return fmt.Errorf("a2ui: component %q is referenced more than once", id)
	}
	seen[id] = true
	c, ok := s.Components[id]
	if !ok {
		return fmt.Errorf("a2ui: component %q is referenced but not defined", id)
	}
	path[id] = true
	defer delete(path, id)
	for _, ref := range childRefs(c) {
		if err := s.walk(ref, path, seen, depth+1, lim); err != nil {
			return err
		}
	}
	return nil
}

func childRefs(c map[string]any) []string {
	var refs []string
	if s, ok := c["child"].(string); ok {
		refs = append(refs, s)
	}
	if arr, ok := c["children"].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				refs = append(refs, s)
			}
		}
	}
	return refs
}

// DeclaresAction reports whether the component is a Button of this surface
// whose action event carries that name.
func (s *Surface) DeclaresAction(componentID, name string) bool {
	c := s.Components[componentID]
	if c == nil || c["component"] != "Button" {
		return false
	}
	a, _ := c["action"].(map[string]any)
	ev, _ := a["event"].(map[string]any)
	return ev["name"] == name
}

func deepCopy(v any) any {
	b, _ := json.Marshal(v)
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	var out any
	_ = d.Decode(&out)
	return out
}

// setPointer sets (or, when set is false, deletes) the value at an RFC 6901
// pointer, creating objects along the way. Arrays are not traversed.
func setPointer(root any, ptr string, val any, set bool) (any, error) {
	if ptr == "" || ptr == "/" {
		if !set {
			return map[string]any{}, nil
		}
		return val, nil
	}
	cur, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("data model root is not an object")
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		if i == len(parts)-1 {
			if set {
				cur[p] = val
			} else {
				delete(cur, p)
			}
			return root, nil
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			if cur[p] != nil {
				return nil, fmt.Errorf("path %q crosses a non-object", ptr)
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return root, nil
}
