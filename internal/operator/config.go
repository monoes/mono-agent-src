package operator

import "strings"

// reservedConfigs are the configs-table rows (`config get|set|delete`) that
// gate approvals, permissions or sandboxing. Only the operator's own terminal
// writes them: `config set` and `config delete` refuse anything else
// (internal/cli/configcli), as the commands made for them do.
//
//   - browser_agent_permissions: each kind of agent's level in the user's
//     browser and the site rules (browserctl.PermissionsSetting; `browser
//     permissions set|site`).
//   - browser_agent_allowlist: the older per-site allow rules, still read as
//     allow rules (browserctl.AllowlistSetting; `browser allow`).
//   - coder_mode: coder mode on or off, sendApproval: whether an agent's
//     sends wait for approval, appAccess: what coder agents are told and offered
//     about MonoAgent (the profile summary, the workspace profile), and the org's router, model picker and writers
//     (internal/cli/chat, internal/hilsend; `coder enable`, `coder set`, every
//     flag of which is the operator's).
//   - sandbox.fallback: what an agent turn does when its runtime has no
//     sandbox for it (`sandbox fallback --set`).
//   - automation.autoRepair: the recorder applies selector repairs without
//     asking (automation.AutoRepairConfig).
//   - applications.max_parked: how many filled application tabs may wait for
//     the person's submit at once (internal/applypark; `application fill queue`).
//   - coder_projects: the folder a site's queued coder chats work in, with
//     full access (`coder projects map|unmap`).
//   - every row under ReservedConfigPrefixes: families of rows named by a
//     prefix. coder-mission-confirm/ holds the operator's per-send mission
//     confirmations, which the coder host and its chat turns accept as proof
//     that the person asked for a mission; a write by anything else would be
//     a forged confirmation. Their values are also hidden from `config get`,
//     `config list` and whatever else shows rows to a non-operator
//     (HiddenConfig).
//
// Not reserved: memory.runs (the run-memory opt-out), workflow.error_workflow
// (the default error workflow, which any workflow can also name for itself)
// and the social actions' selector configs. The task board's agent
// access, the send always-allow rules and org grants live in other tables,
// which `config` does not reach.
var reservedConfigs = []string{
	"browser_agent_permissions",
	"browser_agent_allowlist",
	"coder_mode",
	"sandbox.fallback",
	"automation.autoRepair",
	"coder_projects",
	"applications.max_parked",
}

// ReservedConfigPrefixes are the reserved families of config rows: a name that
// starts with one (in any case, with spaces before it) is reserved.
var ReservedConfigPrefixes = []string{
	"coder-mission-confirm/",
}

// ReservedConfig reports whether name is a reserved config row, in any case
// and with spaces around it, or is under a reserved prefix.
func ReservedConfig(name string) bool {
	name = strings.TrimSpace(name)
	for _, r := range reservedConfigs {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return HiddenConfig(name)
}

// HiddenConfig reports whether name is under a reserved prefix: such rows are
// reserved, and their values are not shown to a non-operator either (they hold
// live single-use material).
func HiddenConfig(name string) bool {
	name = strings.TrimSpace(name)
	for _, p := range ReservedConfigPrefixes {
		// Compare as runes with case folding, so a differently cased or
		// folded spelling is the same name here as it is for a reader that folds.
		if rs := []rune(name); len(rs) >= len([]rune(p)) && strings.EqualFold(string(rs[:len([]rune(p))]), p) {
			return true
		}
	}
	return false
}
