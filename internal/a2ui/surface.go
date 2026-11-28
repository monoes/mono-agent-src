package a2ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// MaxSurfaces caps the surfaces of one Registry that can still be acted on (see
// open), counted for the host's and the agents' separately: agent cards never
// take an approval's place. Decided notices, answered questions and button-less
// panels do not count, or a long conversation would stop showing cards.
const MaxSurfaces = 8

// MaxKeptSurfaces bounds all the surfaces one Registry holds of each
// provenance; past it the oldest one of that provenance that cannot be acted on
// is forgotten.
const MaxKeptSurfaces = 200

// Surface is the server-side state of one live surface: the merged component
// list and the data model. It exists to validate updates and to answer "did
// this surface declare that action".
type Surface struct {
	ID   string
	Prov Provenance
	// Author is the agent that created the surface ("" for the lead and for the
	// host). Only it changes or deletes an agent surface.
	Author        string
	SendDataModel bool
	Components    map[string]map[string]any
	Data          any
	// Settled says the user already acted on the surface.
	Settled bool
	born    int // creation order, for eviction
}

// open says whether the surface can still be acted on: not settled, and either
// holding a Button or not drawn yet.
func (s *Surface) open() bool {
	if s.Settled {
		return false
	}
	for _, c := range s.Components {
		if c["component"] == "Button" {
			return true
		}
	}
	return len(s.Components) == 0
}

// Registry tracks the live surfaces of one conversation.
type Registry struct {
	mu   sync.Mutex
	lim  Limits
	surf map[string]*Surface
	born int
}

// NewRegistry returns an empty registry with the given limits.
func NewRegistry(lim Limits) *Registry {
	return &Registry{lim: lim, surf: map[string]*Surface{}}
}

// Handle is HandleFrom for the host, or for the lead agent.
func (r *Registry) Handle(raw []byte, prov Provenance) (Message, error) {
	return r.HandleFrom(raw, prov, "")
}

// HandleFrom validates one raw message from an author of the given provenance
// and applies it. Only a surface's author, with its provenance, may change it;
// the host may also delete any surface. On any error the registry is unchanged.
func (r *Registry) HandleFrom(raw []byte, prov Provenance, author string) (Message, error) {
	m, err := ParseMessage(raw, r.lim)
	if err != nil {
		return Message{}, err
	}
	if prov != FromSystem && IsSystemSurfaceID(m.SurfaceID) {
		return Message{}, fmt.Errorf("a2ui: surface id %q is reserved for system surfaces (%s)", m.SurfaceID, strings.Join(SystemSurfacePrefixes, " "))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.surf[m.SurfaceID]
	switch m.Kind {
	case KindCreateSurface:
		if s != nil {
			return Message{}, fmt.Errorf("a2ui: surface %q already exists", m.SurfaceID)
		}
		r.born++
		s = &Surface{born: r.born, ID: m.SurfaceID, Prov: prov, Author: author, SendDataModel: m.SendDataModel,
			Components: map[string]map[string]any{}, Data: map[string]any{}}
	case KindDeleteSurface:
		if s == nil {
			return Message{}, fmt.Errorf("a2ui: unknown surface %q", m.SurfaceID)
		}
		if prov != FromSystem {
			if err := s.ownedBy(prov, author); err != nil {
				return Message{}, err
			}
		}
		delete(r.surf, m.SurfaceID)
		return m, nil
	default:
		if s == nil {
			return Message{}, fmt.Errorf("a2ui: surface %q was not created", m.SurfaceID)
		}
		if err := s.ownedBy(prov, author); err != nil {
			return Message{}, err
		}
		s = s.clone()
	}
	if err := s.apply(m, r.lim); err != nil {
		return Message{}, err
	}
	opened, kept := 0, 0
	for id, other := range r.surf {
		if id != s.ID && other.Prov == s.Prov {
			kept++
			if other.open() {
				opened++
			}
		}
	}
	if s.open() && opened >= MaxSurfaces {
		return Message{}, fmt.Errorf("a2ui: more than %d live surfaces; delete one first", MaxSurfaces)
	}
	if r.surf[s.ID] == nil && kept >= MaxKeptSurfaces {
		r.evictOldestClosed(s.Prov)
	}
	r.surf[s.ID] = s
	return m, nil
}

// ownedBy says whether an author may change the surface. The host is one author.
func (s *Surface) ownedBy(prov Provenance, author string) error {
	if s.Prov != prov {
		return fmt.Errorf("a2ui: surface %q belongs to the %s author", s.ID, s.Prov)
	}
	if prov != FromSystem && s.Author != author {
		return fmt.Errorf("a2ui: surface %q belongs to another agent", s.ID)
	}
	return nil
}

// evictOldestClosed forgets the oldest surface of prov that cannot be acted on.
// Open surfaces are at most MaxSurfaces, so one always exists at the bound.
func (r *Registry) evictOldestClosed(prov Provenance) {
	var oldest *Surface
	for _, s := range r.surf {
		if s.Prov == prov && !s.open() && (oldest == nil || s.born < oldest.born) {
			oldest = s
		}
	}
	if oldest != nil {
		delete(r.surf, oldest.ID)
	}
}

// Settle marks a live surface as acted on, which frees its slot.
func (r *Registry) Settle(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.surf[id]; s != nil {
		c := s.clone()
		c.Settled = true
		r.surf[id] = c
	}
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
