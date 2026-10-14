package chat

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// MonoagentTools gives an AI chat turn tool access into the rest of a
// running monoagent installation — workflows (including building one from
// scratch via create_workflow/add_workflow_node, e.g. for social-platform
// automation node types like instagram.like_posts), the vault, people,
// communications, lists/templates — the same way CanvasTools scopes tool
// access to one workflow's canvas. It follows CanvasTools' exact shape
// (ToolDefs/Execute, raw SQL, profile-guarded reads/writes) rather than
// going through storage.Database/WorkflowStore, because several of those
// repository methods are not profile-scoped (see checkPersonOwnership
// below) — raw, explicitly-scoped SQL here is safer than relying on
// callers to remember to scope every call site.
type MonoagentTools struct {
	db      *sql.DB
	selfBin string // resolved monoagentcli binary path; empty disables run_workflow

	mu        sync.RWMutex
	profileID string
	store     WorkflowStore // nil: workflows only in SQLite (SetWorkflowStore)
	// allowRuns is the mechanical, session-start gate for run_workflow —
	// a model-supplied confirm:true argument can never flip
	// it — only the chat session's explicit opt-in (CLI --tools
	// monoagent,runs; GUI persisted setting) sets it at construction.
	allowRuns bool
	// sawUntrustedContent records that content nobody vouches for entered
	// this session's context: get_message/list_messages returned synced
	// communications, or the turn carries text from a web page. Gated
	// tools refuse afterwards — both are proven prompt-injection vectors.
	sawUntrustedContent bool
	// untrustedSource names what was seen ("synced communications" or "web
	// page"), for the refusal's wording.
	untrustedSource string
	// pageReadOnly restricts the session to PageReadToolNames: the mode of a
	// turn that carries page text from the browser extension.
	pageReadOnly bool
	// orgProjectRoot is a test/explicit override for profileRoot(); empty
	// means resolve normally.
	orgProjectRoot string
}

// NewMonoagentTools creates a MonoagentTools backed by db. selfBin is the
// path to the currently-running monoagentcli binary (via os.Executable()),
// used only by run_workflow to shell back into the full,
// already-wired execution path (engine/scheduler/browser session DI lives
// in cmd/monoagentcli, not duplicated here) — pass "" to disable it.
func NewMonoagentTools(db *sql.DB, selfBin string) *MonoagentTools {
	return &MonoagentTools{db: db, selfBin: selfBin, profileID: "default"}
}

// WorkflowStore is the part of the workflow store the tools need when
// workflows are also kept as files (workflow.HybridWorkflowStore): writes that
// must reach the file, not only the SQLite rows.
type WorkflowStore interface {
	SetWorkflowActive(ctx context.Context, id string, active bool) error
	DeleteWorkflow(ctx context.Context, id string) error
}

// SetWorkflowStore makes set_workflow_active and delete_workflow go through
// st, so a workflow saved as a file is activated or removed there too.
func (mt *MonoagentTools) SetWorkflowStore(st WorkflowStore) {
	mt.mu.Lock()
	mt.store = st
	mt.mu.Unlock()
}

func (mt *MonoagentTools) workflowStore() WorkflowStore {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.store
}

// SetProfileID sets the active profile every tool call is scoped to.
func (mt *MonoagentTools) SetProfileID(id string) {
	if id == "" {
		id = "default"
	}
	mt.mu.Lock()
	mt.profileID = id
	mt.mu.Unlock()
}

// SetAllowRuns opts this session in to run_workflow execution.
// It must only be called from the session's explicit start-time opt-in
// (CLI --tools monoagent,runs; GUI persisted setting), never from anything
// a model turn can influence.
func (mt *MonoagentTools) SetAllowRuns(allow bool) {
	mt.mu.Lock()
	mt.allowRuns = allow
	mt.mu.Unlock()
}

func (mt *MonoagentTools) runsAllowed() bool {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.allowRuns
}

func (mt *MonoagentTools) markSyncedCommsSeen() {
	mt.markUntrusted("synced communications")
}

// MarkPageContextSeen records that this turn carries text from a web page.
// It trips the same injection gate synced messages do.
func (mt *MonoagentTools) MarkPageContextSeen() {
	mt.markUntrusted("web page")
}

func (mt *MonoagentTools) markUntrusted(source string) {
	mt.mu.Lock()
	if !mt.sawUntrustedContent {
		mt.untrustedSource = source
	}
	mt.sawUntrustedContent = true
	mt.mu.Unlock()
}

func (mt *MonoagentTools) untrustedContentSeen() (string, bool) {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.untrustedSource, mt.sawUntrustedContent
}

// PageReadToolNames is the whole tool set of a page-read session (the
// extension's chat): read-only, and none of it reaches credentials, the
// vault, profile documents, synced messages, people, publications, orgs or
// anything that writes. Hidden text on a page can steer what these show; it
// cannot make them change or leak anything private.
var PageReadToolNames = []string{"list_workflows", "get_workflow", "list_node_types"}

// SetPageReadOnly confines this session to PageReadToolNames and counts the
// turn's page text as untrusted content. Only the CLI's --tools
// monoagent:read start-time choice may call it, never a model turn.
func (mt *MonoagentTools) SetPageReadOnly() {
	mt.mu.Lock()
	mt.pageReadOnly = true
	mt.mu.Unlock()
	mt.MarkPageContextSeen()
}

func (mt *MonoagentTools) pageRead() bool {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.pageReadOnly
}

func pageReadAllows(name string) bool {
	for _, n := range PageReadToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// checkRunGate enforces the mechanical preconditions every run tool must
// pass before doing anything else — session-level runs opt-in, then the
// injection guard. Both refusals are tool errors the model can relay to
// the user; neither can be satisfied by any argument the model supplies.
func (mt *MonoagentTools) checkRunGate(tool string) error {
	if !mt.runsAllowed() {
		return fmt.Errorf("%s refused: run execution is not enabled in this session — restart the chat with runs explicitly enabled (CLI: --tools monoagent,runs; GUI: enable the run-execution setting) to execute", tool)
	}
	return mt.checkInjectionGate(tool)
}

// checkInjectionGate refuses a tool that writes durable, potentially
// executable state (a workflow run, a saved document) once synced
// communications content has entered this session — untrusted content and a
// proven prompt-injection vector. Unlike checkRunGate, it does not require
// the "runs" opt-in: writing a document is not a workflow/action run, so
// save_document stays available whenever MonoagentTools are on, but it must
// not become the write primitive a prompt injection reaches for once
// checkRunGate has already shut that door for run_workflow.
func (mt *MonoagentTools) checkInjectionGate(tool string) error {
	// A refusal here never depends on an argument: a model-supplied
	// confirm:true is not consent, so the gate is checked before confirm is
	// even looked at.
	source, seen := mt.untrustedContentSeen()
	if !seen {
		return nil
	}
	if source == "web page" {
		return fmt.Errorf("%s refused: this turn carries text from a web page (possible prompt-injection vector) — do this from the app's own chat, not from a page", tool)
	}
	return fmt.Errorf("%s refused: synced communications content was read into this session (possible prompt-injection vector) — start a fresh session without reading messages first to execute", tool)
}

// ProfileID returns the active profile id under the read lock.
func (mt *MonoagentTools) ProfileID() string {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.profileID
}

// SetOrgProjectRoot overrides the directory the org-design tools
// (list_orgs/get_org/create_org/...) resolve org configs under, bypassing
// the normal profileID -> profiledir.Root resolution. Used by tests, which
// need an isolated temp directory rather than a real profile's filesystem
// location; production callers should leave this unset.
func (mt *MonoagentTools) SetOrgProjectRoot(root string) {
	mt.mu.Lock()
	mt.orgProjectRoot = root
	mt.mu.Unlock()
}

// orgProjectRootOverride returns the test override, if any.
func (mt *MonoagentTools) orgProjectRootOverride() string {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.orgProjectRoot
}

// profileRoot resolves the filesystem root org-design tools read/write
// under: an explicit SetOrgProjectRoot override if set (tests), otherwise
// the active profile's root via profiledir.Root — "" meaning the "default"
// profile. This is the same folder `monoagentcli org` (no --project), the
// GUI's Orgs tab, and the org.run node resolve, so grants, which are keyed
// by profile, always describe the org file the tool edited (C-31). Before
// the org × workflow unification the "" / "default" case used the legacy
// ~/.monoagent folder; orgs left there are found by `monoagentcli org
// legacy list`.
func (mt *MonoagentTools) profileRoot() string {
	mt.mu.RLock()
	override := mt.orgProjectRoot
	pid := mt.profileID
	mt.mu.RUnlock()
	if override != "" {
		return override
	}
	if pid == "" {
		pid = "default"
	}
	return profiledir.Root(mt.db, pid)
}

// documentRoot returns the directory save_document writes into and must
// equal, byte-for-byte, what the GUI's document watcher scans for this same
// profile — wails-app/App.documentRootForActiveProfile, which is always
// exactly profiledir.Root(db, activeProfileID) with no "" / "default"
// special-casing. profileRoot resolves to
// the same folder now; documentRoot stays a separate function because the
// two answer different questions (where org configs live vs. where the
// document watcher scans), and a past divergence between them made saved
// documents vanish. The orgProjectRoot test override applies to both.
func (mt *MonoagentTools) documentRoot() string {
	mt.mu.RLock()
	override := mt.orgProjectRoot
	pid := mt.profileID
	mt.mu.RUnlock()
	if override != "" {
		return override
	}
	if pid == "" {
		pid = "default"
	}
	return profiledir.Root(mt.db, pid)
}

// restrictFileWriteForOrgs reports whether newly-created org roles should
// get the default taxonomy-scoped fileWrite policy (see
// orgdesign.NewOrgOptions.RestrictFileWrite / applyRoleDefaults) — true for
// a profile with no root_dir override (profiledir.IsDefaultManaged), the
// same rule wails-app's own restrictFileWriteForOrgs applies. An explicit
// orgProjectRoot override (tests) stays unrestricted.
func (mt *MonoagentTools) restrictFileWriteForOrgs() bool {
	mt.mu.RLock()
	override := mt.orgProjectRoot
	pid := mt.profileID
	mt.mu.RUnlock()
	if override != "" {
		return false
	}
	if pid == "" {
		pid = "default"
	}
	return profiledir.IsDefaultManaged(mt.db, pid)
}

// checkWorkflowOwnership mirrors CanvasTools' check — required here too
// since GetWorkflow/DeleteWorkflow-shaped queries take a bare ID.
func (mt *MonoagentTools) checkWorkflowOwnership(workflowID string) error {
	var exists int
	err := mt.db.QueryRow(
		`SELECT 1 FROM workflows WHERE id = ? AND COALESCE(profile_id,'default') = ?`,
		workflowID, mt.ProfileID(),
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("workflow %s not found", workflowID)
	}
	if err != nil {
		return fmt.Errorf("check workflow ownership: %w", err)
	}
	return nil
}

func (mt *MonoagentTools) checkPersonOwnership(personID string) error {
	var exists int
	err := mt.db.QueryRow(
		`SELECT 1 FROM people WHERE id = ? AND COALESCE(profile_id,'default') = ?`,
		personID, mt.ProfileID(),
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("person %s not found", personID)
	}
	if err != nil {
		return fmt.Errorf("check person ownership: %w", err)
	}
	return nil
}

// Execute dispatches a monoagent-domain tool call by name. It derives from
// context.Background — prefer ExecuteContext, which threads the caller's
// context so run-tool subprocesses are cancelled with the turn.
func (mt *MonoagentTools) Execute(name string, args string) (string, error) {
	return mt.ExecuteContext(context.Background(), name, args)
}

// ExecuteContext dispatches a monoagent-domain tool call by name, deriving
// run-tool subprocess lifetimes from ctx (no Background-derived orphans).
func (mt *MonoagentTools) ExecuteContext(ctx context.Context, name string, args string) (string, error) {
	if mt.pageRead() && !pageReadAllows(name) {
		return "", fmt.Errorf("%s is not available in this session", name)
	}
	switch name {
	case "list_workflows":
		return mt.listWorkflows(args)
	case "get_workflow":
		return mt.getWorkflow(args)
	case "delete_workflow":
		return mt.deleteWorkflow(args)
	case "set_workflow_active":
		return mt.setWorkflowActive(args)
	case "run_workflow":
		return mt.runWorkflow(ctx, args)
	case "create_workflow":
		return mt.createWorkflow(args)
	case "list_node_types":
		return mt.listNodeTypes(args)
	case "add_workflow_node":
		return mt.addWorkflowNode(args)
	case "list_vault_items":
		return mt.listVaultItems(args)
	case "get_vault_item_path":
		return mt.getVaultItemPath(args)
	case "search_profile_documents":
		return mt.searchProfileDocuments(args)
	case "save_document":
		return mt.saveDocument(args)
	case "save_image":
		return mt.saveImage(args)
	case "list_secrets":
		return mt.listSecrets(args)
	case "add_secret":
		return mt.addSecret(args)
	case "update_secret":
		return mt.updateSecret(args)
	case "delete_secret":
		return mt.deleteSecret(args)
	case "list_people":
		return mt.listPeople(args)
	case "get_person":
		return mt.getPerson(args)
	case "upsert_person":
		return mt.upsertPerson(args)
	case "delete_person":
		return mt.deletePerson(args)
	case "list_messages":
		return mt.listMessages(args)
	case "get_message":
		return mt.getMessage(args)
	case "list_social_lists":
		return mt.listSocialLists(args)
	case "list_templates":
		return mt.listTemplates(args)
	case "list_orgs":
		return mt.listOrgs(args)
	case "get_org":
		return mt.getOrg(args)
	case "create_org":
		return mt.createOrg(args)
	case "add_org_role":
		return mt.addOrgRole(args)
	case "update_org_role":
		return mt.updateOrgRole(args)
	case "set_role_reports_to":
		return mt.setRoleReportsTo(args)
	case "remove_org_role":
		return mt.removeOrgRole(args)
	case "validate_org":
		return mt.validateOrg(args)
	case "reload_org":
		return mt.reloadOrg(args)
	default:
		if out, ok, err := mt.executePublication(ctx, name, args); ok {
			return out, err
		}
		if out, ok, err := mt.executeOrgUnification(ctx, name, args); ok {
			return out, err
		}
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}
