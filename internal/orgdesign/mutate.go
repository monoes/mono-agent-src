package orgdesign

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// NewOrgOptions configures NewOrg. Zero values mean "use the default".
type NewOrgOptions struct {
	Schedule      json.RawMessage // nil -> JSON null
	Runtime       string
	Workspace     string // run_config.workspace
	RootRoleID    string // default "lead"
	RootRoleTitle string // default "Lead"
	RootRoleType  string // default "boss"
	// RestrictFileWrite, when true, gives the root role a default
	// Extra["policy"].fileWrite allowlist scoped to profiledir.TaxonomyFolders
	// (see applyRoleDefaults). Callers should only set this for a
	// default-managed profile (profiledir.IsDefaultManaged) — a profile
	// pointed at an existing coding project via a custom root_dir must keep
	// unrestricted write access to its own structure.
	RestrictFileWrite bool
}

// NewOrg builds a fresh Doc with a single root role. It does not write
// anything to disk — callers pass the result to Save.
func NewOrg(name, goal string, opts NewOrgOptions) *Doc {
	rootID := opts.RootRoleID
	if rootID == "" {
		rootID = "lead"
	}
	rootTitle := opts.RootRoleTitle
	if rootTitle == "" {
		rootTitle = "Lead"
	}
	rootType := opts.RootRoleType
	if rootType == "" {
		rootType = "boss"
	}

	schedule := opts.Schedule
	if schedule == nil {
		schedule = json.RawMessage("null")
	}

	rootRole := Role{
		ID:        rootID,
		Title:     rootTitle,
		Type:      rootType,
		ReportsTo: nil,
	}
	applyRoleDefaults(&rootRole, opts.RestrictFileWrite)

	d := &Doc{
		Name:     name,
		Goal:     goal,
		Status:   "stopped",
		Schedule: schedule,
		Runtime:  opts.Runtime,
		Roles:    []Role{rootRole},
	}
	if opts.Workspace != "" {
		wsJSON, _ := json.Marshal(opts.Workspace)
		d.RunConfig = map[string]json.RawMessage{"workspace": wsJSON}
	}
	return d
}

// defaultFileWritePolicy returns an Extra["policy"] value restricting
// writes to profiledir.TaxonomyFolders plus bare top-level filenames.
func defaultFileWritePolicy() json.RawMessage {
	globs := make([]string, 0, len(profiledir.TaxonomyFolders)+1)
	for _, f := range profiledir.TaxonomyFolders {
		globs = append(globs, f.Name+"/**")
	}
	globs = append(globs, "*") // bare top-level filenames, no subdirectory
	b, _ := json.Marshal(map[string]interface{}{"fileWrite": globs})
	return b
}

// defaultTaxonomyResponsibility describes profiledir.TaxonomyFolders and the
// dot-folder prohibition as a Responsibilities entry, generated from
// profiledir.TaxonomyFolders so it can't drift from the actual taxonomy.
func defaultTaxonomyResponsibility() string {
	names := make([]string, len(profiledir.TaxonomyFolders))
	for i, f := range profiledir.TaxonomyFolders {
		names[i] = f.Name + "/"
	}
	return fmt.Sprintf(
		"Organize new files under the profile's standard folders: %s. Never create files inside a dot-prefixed folder (e.g. .monomind/) — those are reserved for internal tool state.",
		strings.Join(names, ", "))
}

// applyRoleDefaults centralizes what every newly-created role gets, called
// from both AddRole and NewOrg's root role so the two can never drift
// apart. Responsibilities nil-safety and the Type default are unconditional
// (pre-dating this feature, unrelated to it). The taxonomy pieces — both
// the advisory Responsibilities text AND the enforced fileWrite policy —
// are gated together on restrictFileWrite: a custom-root_dir profile (an
// existing coding project, per the plan's correction) must not be told to
// "organize new files under docs/, workingdocs/, ..." either, since that
// guidance is wrong for a profile whose real structure is src/, go.mod,
// etc. — advisory text for a convention that isn't even being enforced
// there would be actively misleading, not harmless. Only a caller that
// hasn't already set Extra["policy"] themselves gets the default policy.
func applyRoleDefaults(r *Role, restrictFileWrite bool) {
	if r.Responsibilities == nil {
		r.Responsibilities = []string{}
	}
	if r.Type == "" {
		r.Type = "specialist"
	}
	if !restrictFileWrite {
		return
	}
	r.Responsibilities = append(r.Responsibilities, defaultTaxonomyResponsibility())
	if _, hasPolicy := r.Extra["policy"]; !hasPolicy {
		if r.Extra == nil {
			r.Extra = map[string]json.RawMessage{}
		}
		r.Extra["policy"] = defaultFileWritePolicy()
	}
}

// RolePatch is a shallow, patch-style update for UpdateRole. A nil field is
// left unchanged. A non-nil pointer to an empty string (or empty slice)
// explicitly clears the field — needed so the UI/AI can return an optional
// value (e.g. budget_usd) back to "inherit" rather than being unable to
// ever unset it once set. Model, Icon, and Color patch nested fields
// (AdapterConfig["model"], UI.Icon, UI.Color respectively).
type RolePatch struct {
	Title            *string
	Type             *string
	Responsibilities *[]string
	Runtime          *string // "" clears role.Extra["runtime"]
	Model            *string // "" clears AdapterConfig["model"]
	Icon             *string
	Color            *string
	// MaxTurnsPerMessage, BudgetTokens, BudgetUSD patch role.Extra's
	// like-named keys (max_turns_per_message, budget_tokens, budget_usd —
	// all RoleSchema-optional, unset means "inherit the org-wide default").
	// Known limitation: since JSON.stringify drops undefined-valued object
	// keys, the frontend inspector cannot currently distinguish "user
	// cleared this field back to inherit" from "field untouched" for these
	// three — both arrive as the key simply being absent from patchJSON, so
	// nil here always means "leave unchanged," never "clear." Unlike
	// Runtime/Model above, there is no in-band empty-value sentinel for a
	// number (0 is a real, different value from "unset"). Clearing an
	// already-set budget currently requires editing the org's JSON file
	// directly; revisit if that turns out to matter in practice.
	MaxTurnsPerMessage *int
	BudgetTokens       *int
	BudgetUSD          *float64
	// Policy and Provider replace the role's whole `policy`/`provider`
	// object wholesale (the frontend always sends the complete object, not
	// a deep partial) — nil leaves it unchanged, a RawMessage of `null`
	// clears it. Both are opaque here (they live in role.Extra); monomind's
	// own schema (RolePolicySchema/ProviderSchema) is the source of truth
	// for their shape, this package only round-trips them.
	Policy   *json.RawMessage
	Provider *json.RawMessage
	// InstructionsFile patches role.Extra["instructions_file"]; "" clears it.
	InstructionsFile *string
	// AdapterConfigProvider/AdapterConfigMaxTokens patch
	// AdapterConfig["provider"]/["max_tokens"], parallel to Model above.
	AdapterConfigProvider  *string
	AdapterConfigMaxTokens *int
}

// AddRole appends a new role to d. If r.ID is empty, one is derived from
// r.Title via UniqueRoleID. If r.ID is non-empty and already exists, AddRole
// returns an error rather than silently renaming it — a caller-supplied id
// (in particular from an AI tool call that references it in the same turn,
// e.g. for reports_to) must be honored or explicitly rejected, never
// changed underneath the caller.
//
// restrictFileWrite is a trailing optional bool (omit it entirely, or pass
// one value) so every pre-existing call site keeps compiling unchanged and
// keeps its exact old behavior (unrestricted, no default fileWrite policy)
// — only a caller that explicitly opts in with true gets the taxonomy
// default. See applyRoleDefaults and NewOrgOptions.RestrictFileWrite.
func (d *Doc) AddRole(r Role, restrictFileWrite ...bool) (*Role, error) {
	return d.addRole(r, "", len(restrictFileWrite) > 0 && restrictFileWrite[0])
}

// addRole is AddRole and AddRoleToSection (sections_mutate.go): in a sections
// org the new role joins `section`, else its parent's section, or the add is
// refused.
func (d *Doc) addRole(r Role, section string, restrict bool) (*Role, error) {
	if r.ID == "" {
		r.ID = UniqueRoleID(d, r.Title)
	} else if _, idx := d.FindRole(r.ID); idx != -1 {
		return nil, fmt.Errorf("duplicate role id: %s", r.ID)
	}
	applyRoleDefaults(&r, restrict)
	// "boss" is derived from tree position (see UpdateRole's matching guard,
	// PromoteToRoot) — silently downgrade rather than error here, since a
	// non-root add with type "boss" is never a deliberate user choice (there
	// is no UI path to type it directly anymore) but can arrive incidentally
	// — e.g. the palette's category->type suggestion map happens to say
	// "boss" for a "management"/"sparc" archetype, and every palette-driven
	// add is non-root by construction (resolveDefaultParent always assigns
	// an existing role as parent once one exists).
	if r.Type == "boss" && r.ReportsTo != nil {
		r.Type = "specialist"
	}
	home, err := d.sectionForNewRole(&r, section)
	if err != nil {
		return nil, err
	}
	d.Roles = append(d.Roles, r)
	d.joinSection(home, r.ID)
	if home != "" {
		d.raiseAgentCap()
	}
	added, _ := d.FindRole(r.ID)
	return added, nil
}

// UpdateRole applies patch to the named role. Returns an error if the role
// doesn't exist.
func (d *Doc) UpdateRole(id string, patch RolePatch) (*Role, error) {
	r, idx := d.FindRole(id)
	if idx == -1 {
		return nil, fmt.Errorf("role not found: %s", id)
	}
	if patch.Title != nil {
		r.Title = *patch.Title
	}
	if patch.Type != nil {
		// "boss" is derived from tree position (see PromoteToRoot), never a
		// freely-typed label — reject here so a stray patch (including from
		// the AI tool path, which also goes through UpdateRole) can't create
		// the exact ambiguous multi-"boss" state PromoteToRoot exists to
		// prevent. Only reject the string itself; everything else stays a
		// free-form cosmetic label.
		if *patch.Type == "boss" && r.ReportsTo != nil {
			return nil, fmt.Errorf(`role %q is not the org root — use "set as org boss" (PromoteToRoot) to make a role type "boss" instead of patching it directly`, id)
		}
		r.Type = *patch.Type
	}
	if patch.Responsibilities != nil {
		r.Responsibilities = *patch.Responsibilities
	}
	if patch.Runtime != nil {
		if *patch.Runtime == "" {
			delete(r.Extra, "runtime")
		} else {
			b, _ := json.Marshal(*patch.Runtime)
			if r.Extra == nil {
				r.Extra = map[string]json.RawMessage{}
			}
			r.Extra["runtime"] = b
		}
	}
	if patch.Model != nil {
		if *patch.Model == "" {
			delete(r.AdapterConfig, "model")
		} else {
			if r.AdapterConfig == nil {
				r.AdapterConfig = map[string]json.RawMessage{}
			}
			b, _ := json.Marshal(*patch.Model)
			r.AdapterConfig["model"] = b
		}
	}
	if patch.Icon != nil || patch.Color != nil {
		if r.UI == nil {
			r.UI = &RoleUI{}
		}
		if patch.Icon != nil {
			r.UI.Icon = *patch.Icon
		}
		if patch.Color != nil {
			r.UI.Color = *patch.Color
		}
	}
	if patch.AdapterConfigProvider != nil {
		if *patch.AdapterConfigProvider == "" {
			delete(r.AdapterConfig, "provider")
		} else {
			if r.AdapterConfig == nil {
				r.AdapterConfig = map[string]json.RawMessage{}
			}
			b, _ := json.Marshal(*patch.AdapterConfigProvider)
			r.AdapterConfig["provider"] = b
		}
	}
	setExtraNumber(r, "max_turns_per_message", patch.MaxTurnsPerMessage)
	setExtraNumber(r, "budget_tokens", patch.BudgetTokens)
	setExtraNumberF(r, "budget_usd", patch.BudgetUSD)
	if patch.AdapterConfigMaxTokens != nil {
		if r.AdapterConfig == nil {
			r.AdapterConfig = map[string]json.RawMessage{}
		}
		b, _ := json.Marshal(*patch.AdapterConfigMaxTokens)
		r.AdapterConfig["max_tokens"] = b
	}
	if patch.Policy != nil {
		setExtraRaw(r, "policy", *patch.Policy)
	}
	if patch.Provider != nil {
		setExtraRaw(r, "provider", *patch.Provider)
	}
	if patch.InstructionsFile != nil {
		if *patch.InstructionsFile == "" {
			delete(r.Extra, "instructions_file")
		} else {
			b, _ := json.Marshal(*patch.InstructionsFile)
			if r.Extra == nil {
				r.Extra = map[string]json.RawMessage{}
			}
			r.Extra["instructions_file"] = b
		}
	}
	return r, nil
}

// setExtraNumber writes an integer field into r.Extra under key, or does
// nothing if v is nil (meaning "leave unchanged" — see RolePatch's own
// doc comment on why a present-but-zero value is intentionally
// indistinguishable from absent for these three fields today).
func setExtraNumber(r *Role, key string, v *int) {
	if v == nil {
		return
	}
	b, _ := json.Marshal(*v)
	if r.Extra == nil {
		r.Extra = map[string]json.RawMessage{}
	}
	r.Extra[key] = b
}

// setExtraNumberF is setExtraNumber for a float64 field (budget_usd).
func setExtraNumberF(r *Role, key string, v *float64) {
	if v == nil {
		return
	}
	b, _ := json.Marshal(*v)
	if r.Extra == nil {
		r.Extra = map[string]json.RawMessage{}
	}
	r.Extra[key] = b
}

// setExtraRaw writes a whole-object json.RawMessage into r.Extra under key
// (used for policy/provider, both replaced wholesale rather than deep-
// patched). A literal JSON `null` clears the key instead of storing it —
// callers send `null` to mean "remove this object entirely."
func setExtraRaw(r *Role, key string, v json.RawMessage) {
	if len(v) == 0 || string(v) == "null" {
		delete(r.Extra, key)
		return
	}
	if r.Extra == nil {
		r.Extra = map[string]json.RawMessage{}
	}
	r.Extra[key] = v
}
