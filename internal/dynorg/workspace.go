package dynorg

import (
	"context"
	"fmt"
	"strings"
)

// ProfileContext builds the active profile's context block (name, folder,
// connected accounts, counts and recent items of tasks, documents, people,
// workflows, orgs, vault item names): internal/profilectx. The caller wires
// it once at startup; nil skips the block. With app access on it is included
// in every worker's system prompt (read once per turn, see profileContext; the
// coder lead has it in its own prompt), and it must never contain secret
// values.
var ProfileContext func(ctx context.Context) string

// ProfileID is the chat's mono-agent profile id. The caller wires it with
// ProfileContext; nil or "" leaves the profile rule out. Every profile-scoped
// monoagentcli command needs it as --profile (internal/cli/profilegate), and
// there is deliberately no environment variable for it.
var ProfileID func(ctx context.Context) string

// ProfileRule tells an agent the profile it works in and to name it on every
// monoagentcli command; "" for no id.
func ProfileRule(id string) string {
	if id = strings.TrimSpace(id); id == "" {
		return ""
	}
	return fmt.Sprintf("Your mono-agent profile id is %s: pass `--profile %s` on every monoagentcli command "+
		"(the machine-wide ones, such as `ref`, `browser permissions`, `browser allow`, `browser browsers` and `browser diff`, don't need it, and it does no harm there).", id, id)
}

// profileRuleOf is ProfileRule for the chat's profile, "" with no hook.
func profileRuleOf(ctx context.Context) string {
	if ProfileID == nil {
		return ""
	}
	return ProfileRule(ProfileID(ctx))
}

// profileSectionOf is the rule and the context as a prompt section, or "".
func profileSectionOf(rule, text string) string {
	var parts []string
	if rule != "" {
		parts = append(parts, rule)
	}
	if t := strings.TrimSpace(text); t != "" {
		parts = append(parts, clipText(t, maxProfileContext))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n## Profile context\n\n" + strings.Join(parts, "\n\n") + "\n"
}

const maxProfileContext = 6000

// profileSection is a worker's profile section: the profile rule always, and
// with app access the profile's context (profileContext); off, the workers
// are not given the profile summary, only the id to pass as --profile.
func (c *Conductor) profileSection() string {
	text := ""
	if c.cfg.Staffer.appAccess() {
		text = c.profileContext()
	}
	return profileSectionOf(profileRuleOf(c.ctx), text)
}

// profileContext is ProfileContext for this turn's workers: built at the first
// spawn that gets one and reused until org_roster (Roster) asks for a fresh
// one. Built under the turn's context, not one worker's, so a worker stopped
// mid-build can't leave a half-read block for the rest; an empty result (the
// hook failed: a locked database gives "") is not kept, so the next spawn
// asks again.
func (c *Conductor) profileContext() string {
	c.profileMu.Lock()
	defer c.profileMu.Unlock()
	if c.profileFresh {
		return c.profileText
	}
	if ProfileContext == nil {
		return ""
	}
	if t := strings.TrimSpace(ProfileContext(c.ctx)); t != "" {
		c.profileText, c.profileFresh = t, true
		return t
	}
	return ""
}

// workspaceRules are the surface rules of the workspace access profile,
// shared by the worker prompt and the lead's.
const workspaceRules = "You have full access to this folder and the web, and to every part of the user's mono-agent through " +
	"`monoagentcli --profile <your profile id>` on your PATH (`monoagentcli ref commands` lists what it can do). Use the surface that owns the work: " +
	"an app being run (job applications, social, browser) -> work on that app's tab with `browser` (or `node run` for its nodes); " +
	"to-dos -> `task`; files and documents -> `profile documents`; contacts -> `people`; " +
	"credentials -> the vault (use item names and paths, never print or write a value); " +
	"repeatable work -> build or reuse a WORKFLOW first (`workflow list`, `workflow validate`, `workflow run`) and only then bring in org roles. " +
	"Other writes (tasks, documents, people records, workflows) need no approval."

// sendGateRule is what every worker without the workspace profile is told
// about sending, whatever app access says: it has a shell, so monoagentcli's
// send paths are in reach, and the send-approval queue still holds them.
const sendGateRule = "NEVER finalize a send: anything outbound (email, DM, comment, post, publish, connection request) " +
	"waits in the send-approval queue for the user. Do not try to bypass it (no direct API calls, scripts or browser clicks that send)."

// sendRule replaces the no-messages rule for the workspace profile:
// preparing is free, finalizing a send never is.
const sendRule = "Prepare freely, but NEVER finalize a send. Everything outbound (email, DM, comment, post, publish, connection request) " +
	"must go through the send-approval queue: queue it with `monoagentcli hil ...` or the send nodes, which create a pending approval " +
	"the user answers. Do not try to bypass it (no direct API calls, scripts or browser clicks that send), and do not read the user's " +
	"private messages unless the brief asks you to."
