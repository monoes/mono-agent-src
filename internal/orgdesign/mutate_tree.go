package orgdesign

import (
	"fmt"
)

// SetReportsTo moves childID under newParentID (empty string means "make
// this the root role"). Rejects: an unknown child, an unknown non-empty
// parent, an edge that would create a cycle (via WouldCycle), and — when
// newParentID is empty — a second root when one already exists.
func (d *Doc) SetReportsTo(childID, newParentID string) error {
	child, idx := d.FindRole(childID)
	if idx == -1 {
		return fmt.Errorf("role not found: %s", childID)
	}
	if err := d.checkReparentInSections(child, newParentID); err != nil {
		return err
	}
	if newParentID == "" {
		if root, ok := d.RootRole(); ok && root.ID != childID {
			return fmt.Errorf("org already has a root role (%q) — reassign or remove it before making %q the root", root.ID, childID)
		}
		child.ReportsTo = nil
		return nil
	}
	if _, pidx := d.FindRole(newParentID); pidx == -1 {
		return fmt.Errorf("role not found: %s", newParentID)
	}
	if WouldCycle(d.Roles, childID, newParentID) {
		return fmt.Errorf("circular reporting: %q already reports (directly or indirectly) to %q — this edge would create a cycle", newParentID, childID)
	}
	child.ReportsTo = strPtr(newParentID)
	return nil
}

// PromoteToRoot makes newRootID the org's root role — the org's single
// "boss" (see UpdateRole's boss-guard: type "boss" is derived from tree
// position, never patched directly). Unlike SetReportsTo(id, ""), which
// simply refuses when a different root already exists, PromoteToRoot
// performs the swap by reversing every edge along the path from the
// current root down to newRootID: each role that used to be newRootID's
// ancestor becomes, in order, its descendant instead (old root -> its
// former child on that path -> ... -> newRootID's former direct parent),
// so "make someone else the boss" is always a single clean action from the
// UI, correct at any depth, rather than a two-step reassign-then-promote
// dance (and rather than the simpler-but-wrong "just swap the two
// endpoints" version, which would mis-parent everyone strictly between old
// root and newRootID for a promotion deeper than one level). Roles outside
// that path are untouched. Also flips `type` so exactly one role ever
// reads "boss": the new root's type becomes "boss"; the old root's type is
// downgraded to "specialist" only if it was "boss" (an old root that had
// been given some other custom label keeps it, since that label was never
// the thing that made it boss — its tree position was).
func (d *Doc) PromoteToRoot(newRootID string) error {
	newRoot, idx := d.FindRole(newRootID)
	if idx == -1 {
		return fmt.Errorf("role not found: %s", newRootID)
	}
	if newRoot.ReportsTo == nil {
		return fmt.Errorf("role %q is already the org root", newRootID)
	}
	if d.SectionsEnabled() {
		return fmt.Errorf("cannot promote %q in a sections org: the root is in no section and the old root would have none — restructure the sections first", newRootID)
	}

	// path = [newRootID, its parent, its grandparent, ..., the old root's id].
	path := []string{newRootID}
	cur := newRoot
	for cur.ReportsTo != nil {
		parentID := *cur.ReportsTo
		path = append(path, parentID)
		parent, pidx := d.FindRole(parentID)
		if pidx == -1 {
			return fmt.Errorf("role %q has reports_to %q, which does not exist — cannot promote", cur.ID, parentID)
		}
		cur = parent
	}
	oldRootID := path[len(path)-1]

	// Reverse each edge on the path: path[i] becomes path[i+1]'s new parent.
	for i := 0; i < len(path)-1; i++ {
		r, _ := d.FindRole(path[i+1])
		r.ReportsTo = strPtr(path[i])
	}

	oldRoot, _ := d.FindRole(oldRootID)
	if oldRoot.Type == "boss" {
		oldRoot.Type = "specialist"
	}
	newRoot.ReportsTo = nil
	newRoot.Type = "boss"
	return nil
}

// SetLayout applies a partial map of role id -> canvas position/icon/color.
// Roles absent from pos keep their existing UI unchanged. Deliberately does
// not run structural Validate — a position-only change cannot break the
// tree, and this is the highest-frequency call (fired on every drag-end),
// so it must stay cheap.
func (d *Doc) SetLayout(pos map[string]RoleUI) error {
	for id, ui := range pos {
		r, idx := d.FindRole(id)
		if idx == -1 {
			continue // silently skip unknown ids — a stale/late drag-end for a role deleted mid-drag shouldn't fail the whole batch
		}
		uiCopy := ui
		r.UI = &uiCopy
	}
	return nil
}

// RemoveStrategy selects what happens to a removed role's direct reports.
type RemoveStrategy string

const (
	// Reparent (default): every direct report of the removed role inherits
	// the removed role's own ReportsTo. Always produces a valid tree except
	// for the root special-case (see RemoveRole).
	Reparent RemoveStrategy = "reparent"
	// Cascade deletes the removed role and its entire subtree. Callers
	// exposing this to an AI tool must additionally gate it behind an
	// explicit confirm — see internal/ai/chat/monoagent_tools.go.
	Cascade RemoveStrategy = "cascade"
)

// RemoveRole removes id from d using strategy, returning every role id that
// was actually removed (more than one under Cascade). Refuses to remove the
// last remaining role. Refuses to remove the root role unless it has
// exactly one direct child (which then becomes the new root) — reparenting
// a root's multiple children to nil would otherwise produce multiple roots,
// an unrepresentable "fix" for RemoveRole to silently perform.
func (d *Doc) RemoveRole(id string, strategy RemoveStrategy) ([]string, error) {
	target, idx := d.FindRole(id)
	if idx == -1 {
		return nil, fmt.Errorf("role not found: %s", id)
	}
	if len(d.Roles) == 1 {
		return nil, fmt.Errorf("cannot remove %q: an org must have at least one role", id)
	}

	isRoot := target.ReportsTo == nil
	children := d.Children(id)

	if isRoot && len(children) > 1 {
		return nil, fmt.Errorf("cannot remove root role %q: it has %d direct reports — re-parent them under a single role first, or delete the org", id, len(children))
	}

	switch strategy {
	case Cascade, "":
		if strategy == "" {
			strategy = Reparent
		}
	}

	endSections, err := d.sectionsForRemoval(id, strategy)
	if err != nil {
		return nil, err
	}

	switch strategy {
	case Reparent:
		var newParent *string
		if isRoot {
			if len(children) == 1 {
				newParent = nil // the sole child becomes the new root
			}
		} else {
			newParent = target.ReportsTo
		}
		for _, c := range children {
			c.ReportsTo = newParent
		}
		d.removeByID(id)
		endSections()
		return []string{id}, nil

	case Cascade:
		toRemove := d.descendantsOf(id)
		toRemove = append(toRemove, id)
		for _, rid := range toRemove {
			d.removeByID(rid)
		}
		endSections()
		return toRemove, nil

	default:
		return nil, fmt.Errorf("unknown remove strategy: %s", strategy)
	}
}

// descendantsOf returns every role id in id's subtree (not including id
// itself), via a simple BFS over Children.
func (d *Doc) descendantsOf(id string) []string {
	var out []string
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range d.Children(cur) {
			out = append(out, c.ID)
			queue = append(queue, c.ID)
		}
	}
	return out
}

func (d *Doc) removeByID(id string) {
	for i, r := range d.Roles {
		if r.ID == id {
			d.Roles = append(d.Roles[:i], d.Roles[i+1:]...)
			return
		}
	}
}
