package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// Tool names the lead gets.
const (
	ToolRoster  = "org_roster"
	ToolSpawn   = "org_spawn"
	ToolWait    = "org_wait"
	ToolMessage = "org_message"
	ToolStop    = "org_stop"
	ToolRate    = "org_rate"

	ToolClearWorker   = "org_clear_worker"
	ToolCompactWorker = "org_compact_worker"
)

// ToolTimeout is the lead's --tool-timeout: above MaxWait, so a wait
// always answers before monomind gives up on the call.
const ToolTimeout = MaxWait + 30*time.Second

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// ToolSpecs are the lead's org tools. appAccess is coder.appAccess: off,
// org_spawn does not offer the workspace access profile.
func ToolSpecs(appAccess bool) []monomind.ToolSpec {
	return toolSpecs((&Staffer{NoAppAccess: !appAccess}).profiles())
}

// toolSpecs are the lead's org tools, offering these access profiles.
func toolSpecs(profiles []string) []monomind.ToolSpec {
	return orgToolSpecs(profiles)
}

// orgToolSpecs are the org tools of every lead.
func orgToolSpecs(profiles []string) []monomind.ToolSpec {
	return []monomind.ToolSpec{
		{Name: ToolRoster, Description: "List what you can staff workers with: the ready models (runtime, model, effort levels, access), the access profiles, the built-in roles, the registry agents by category, cost/quality hints per model, the limits, and the workers so far. Pass brief to get the smallest suggested team for it (an empty team means do it yourself).",
			Schema: obj(map[string]any{"brief": str("Optional: the task, to get a suggested team.")})},
		spawnSpec(true, profiles),
		{Name: ToolWait, Description: "Wait for workers to finish (up to timeout_s, at most 100) and get their reports. Call it again for workers still running.",
			Schema: obj(map[string]any{
				"agent_ids": strList("Workers to wait for; empty = all."),
				"timeout_s": map[string]any{"type": "integer", "description": "Seconds to wait, 1-100 (default 100)."},
			})},
		{Name: ToolMessage, Description: "Send a follow-up to a worker that has finished; it continues where it left off. Then org_wait for its new report.",
			Schema: obj(map[string]any{"agent_id": str("The worker."), "text": str("The follow-up.")}, "agent_id", "text")},
		{Name: ToolStop, Description: "Stop a running worker.",
			Schema: obj(map[string]any{"agent_id": str("The worker.")}, "agent_id")},
		{Name: ToolClearWorker, Description: "Forget a finished worker's conversation: its session is dropped and so is its report, so your next org_message to it briefs it from scratch. Refused while it is working.",
			Schema: obj(map[string]any{"agent_id": str("The worker.")}, "agent_id")},
		{Name: ToolCompactWorker, Description: "Shrink a finished worker's context: it is asked for a summary of its work, which becomes its report and replaces its session, so your next org_message to it re-briefs it from that summary. Refused while it is working.",
			Schema: obj(map[string]any{"agent_id": str("The worker.")}, "agent_id")},
		{Name: ToolRate, Description: "Rate a worker's latest report after you read it: good if it did the job, bad if it didn't. Ratings teach staffing which models fit which kind of work; rate each report once.",
			Schema: obj(map[string]any{"agent_id": str("The worker."), "rating": str("good or bad.")}, "agent_id", "rating")},
	}
}

// spawnSpec is org_spawn's spec: the lead's has allow_spawn, a spawning
// worker's doesn't (its sub-workers can't spawn).
func spawnSpec(lead bool, profiles []string) monomind.ToolSpec {
	props := map[string]any{
		"brief":       str("What the worker should do, with everything it needs to know. It can't see this conversation. Optional with task_id: it then adds to the brief built from the task."),
		"task_id":     map[string]any{"type": "integer", "description": "A Ready task of the mission's task session: the worker takes it (claimed under its own name), its brief is built from the task (acceptance, earlier results, files), its report is attached to the task, and the task goes to review."},
		"role":        str("A role: a monomind agent id (e.g. engineering-code-reviewer) or a built-in role (coder, reviewer, tester, researcher, planner, task-manager, doc-writer, people-researcher, vault-keeper, workflow-builder, automation-operator, browser-operator, org-designer, data-analyst, security-reviewer)."),
		"skills":      strList("Skill names to give the worker."),
		"runtime":     str("Runtime to run on (e.g. claude, codex). Must have a ready model; see org_roster."),
		"model":       str("Model id on that runtime; see org_roster."),
		"effort":      str("Reasoning effort, one of the model's effort levels."),
		"access":      str(accessDesc(profiles)),
		"files":       strList("Files the worker should keep to."),
		"needs_write": map[string]any{"type": "boolean", "description": "The worker will edit files (it then waits its turn for the write lease)."},
		"wait":        map[string]any{"type": "boolean", "description": "Wait for the worker to finish (up to 100s) before returning."},
	}
	desc := "Start a worker agent on a brief. It runs in the background in this chat's folder; use org_wait to read its report. Leave role, skills, model, effort or access out to have them picked for you."
	if !lead {
		delete(props, "task_id") // only the lead hands out mission tasks
	}
	if lead {
		props["allow_spawn"] = map[string]any{"type": "boolean", "description": "Let this worker start sub-workers of its own (they can't spawn further, and their access never exceeds the worker's)."}
	} else {
		desc = "Start a sub-worker on a brief. Its access can't exceed yours, and it can't start workers of its own. Use org_wait to read its report."
	}
	if !lead {
		return monomind.ToolSpec{Name: ToolSpawn, Description: desc, Schema: obj(props, "brief")}
	}
	return monomind.ToolSpec{Name: ToolSpawn, Description: desc, Schema: obj(props)} // brief or task_id
}

// accessDesc is org_spawn's access argument, naming the profiles offered.
func accessDesc(profiles []string) string {
	notes := map[string]string{ProfileWorkspace: " (coding plus every monoagent surface; sends need approval)", ProfileResearch: " (read only)"}
	parts := make([]string, len(profiles))
	for i, p := range profiles {
		parts[i] = p + notes[p]
	}
	parts[len(parts)-1] = "or " + parts[len(parts)-1]
	return "Access profile: " + strings.Join(parts, ", ") + "."
}

// childToolSpecs are the org tools of a worker the lead let spawn: its own
// sub-workers only, offering these access profiles.
func childToolSpecs(profiles []string) []monomind.ToolSpec {
	var out []monomind.ToolSpec
	for _, t := range toolSpecs(profiles) {
		switch t.Name {
		case ToolSpawn:
			out = append(out, spawnSpec(false, profiles))
		case ToolWait, ToolMessage:
			out = append(out, t)
		}
	}
	return out
}

// Handle runs one of the lead's org tool calls. A refusal (a limit, an
// unknown model) is returned as an error, which the lead reads as the
// tool's failed result.
func (c *Conductor) Handle(ctx context.Context, name string, args json.RawMessage) (string, error) {
	out, err := c.handle(ctx, nil, name, args)
	// Edits the lead made while a writer held the write lease (LeadEvent)
	// are reported with its next org tool call, which it reads.
	if warnings := c.takeLeadWarnings(); len(warnings) > 0 {
		if err != nil {
			return "", fmt.Errorf("%w (warning: %s)", err, strings.Join(warnings, " "))
		}
		return marshal(map[string]any{"warnings": warnings, "result": json.RawMessage(out)})
	}
	return out, err
}

// handle runs an org tool call of the lead (parent nil) or of a worker
// the lead let spawn (parent), which has org_spawn, org_wait and
// org_message for its own sub-workers only.
func (c *Conductor) handle(ctx context.Context, parent *worker, name string, args json.RawMessage) (string, error) {
	if parent != nil && name != ToolSpawn && name != ToolWait && name != ToolMessage {
		return "", fmt.Errorf("unknown org tool %q", name)
	}
	switch name {
	case ToolRoster:
		var a struct {
			Brief string `json:"brief"`
		}
		_ = json.Unmarshal(orEmpty(args), &a)
		v := c.Roster()
		if strings.TrimSpace(a.Brief) != "" {
			v.Suggested = c.suggestTeam(ctx, a.Brief)
		}
		return marshal(v)
	case ToolSpawn:
		var req SpawnRequest
		if err := json.Unmarshal(orEmpty(args), &req); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		info, err := c.spawn(ctx, req, parent)
		if err != nil {
			return "", err
		}
		return marshal(info)
	case ToolWait:
		var a struct {
			AgentIDs []string `json:"agent_ids"`
			Timeout  int      `json:"timeout_s"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		return marshal(map[string]any{"workers": c.waitFor(ctx, parent, a.AgentIDs, time.Duration(a.Timeout)*time.Second)})
	case ToolMessage:
		var a struct {
			AgentID string `json:"agent_id"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		info, err := c.message(ctx, parent, a.AgentID, a.Text)
		if err != nil {
			return "", err
		}
		return marshal(info)
	case ToolStop:
		var a struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		info, err := c.Stop(a.AgentID)
		if err != nil {
			return "", err
		}
		return marshal(info)
	case ToolClearWorker, ToolCompactWorker:
		var a struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		reset := c.ClearWorker
		if name == ToolCompactWorker {
			reset = c.CompactWorker
		}
		info, err := reset(a.AgentID)
		if err != nil {
			return "", err
		}
		return marshal(info)
	case ToolRate:
		var a struct {
			AgentID string `json:"agent_id"`
			Rating  string `json:"rating"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		res, err := c.Rate(a.AgentID, a.Rating)
		if err != nil {
			return "", err
		}
		return marshal(res)
	// --- mission execution tools (mission_exec_tools.go) ---
	case ToolMissionNext, ToolMissionAssign, ToolMissionReport:
		return c.handleMissionExec(ctx, name, args)
	// --- end of mission execution tools ---
	// --- mission verification and closing tools (mission_verify.go, mission_final.go) ---
	case ToolMissionVerify:
		return c.handleMissionVerify(ctx, args)
	case ToolMissionFinish:
		return c.handleMissionFinish(ctx, args)
	// --- end of mission verification and closing tools ---
	case ToolMerge:
		var a struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		res, err := c.Merge(ctx, a.AgentID)
		if err != nil {
			return "", err
		}
		return marshal(res)
	// --- mission planning tools (mission_tools_plan.go) ---
	case ToolMissionPropose, ToolMissionPlan, ToolMissionAddTasks, ToolMissionSplit, ToolMissionStatus, ToolMissionReplan:
		return c.handleMission(ctx, name, args)
	}
	return "", fmt.Errorf("unknown org tool %q", name)
}

// RosterView is org_roster's result.
type RosterView struct {
	Models   []Model           `json:"models"`
	Profiles map[string]string `json:"access_profiles"`
	Roles    []map[string]any  `json:"builtin_roles"`
	RoleNote string            `json:"roles_note"`
	Limits   map[string]any    `json:"limits"`
	Workers  []WorkerInfo      `json:"workers"`
	// Registry agents by category, one line each; ModelHints say which
	// ready model suits which work; Suggested is the smallest team for the
	// brief passed to org_roster (empty: do it yourself).
	Agents     map[string][]AgentInfo `json:"registry_agents,omitempty"`
	ModelHints []ModelHint            `json:"model_hints"`
	Suggested  []SuggestedWorker      `json:"suggested_team,omitempty"`
}

// Roster describes what the lead can staff with, and the workers so far.
// It is how the lead asks for the current state, so the workers' profile
// section is rebuilt at the next spawn.
func (c *Conductor) Roster() RosterView {
	c.profileMu.Lock()
	c.profileFresh = false
	c.profileMu.Unlock()
	s := c.cfg.Staffer
	v := RosterView{
		Models:     s.Roster,
		Profiles:   map[string]string{},
		RoleNote:   "role accepts any registry agent id listed here (monomind's agents come first) or a built-in role; leave it empty to have one picked for the brief. Smallest sufficient role, at most 3 skills",
		Agents:     c.registryAgents(context.Background()),
		ModelHints: s.ModelHints(),
	}
	for _, p := range s.profiles() {
		v.Profiles[p] = profileRules[p]
	}
	for _, r := range builtinRoles {
		v.Roles = append(v.Roles, map[string]any{"id": r.ID, "title": r.Title, "category": r.Category, "default_access": s.fitAccess(r.Profile), "does": firstSentence(r.Body)})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v.Limits = map[string]any{
		"max_agents": c.cfg.Limits.MaxAgents, "max_concurrent": c.cfg.Limits.MaxConcurrent,
		"spawned": c.spawned, "budget_usd": c.cfg.Limits.BudgetUSD, "spent_usd": c.cost,
		"spent_estimated": c.costEstimated,
	}
	v.Workers = []WorkerInfo{}
	for _, id := range c.order {
		v.Workers = append(v.Workers, c.infoLocked(c.workers[id], false))
	}
	return v
}

func marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func orEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}
