package operator

import (
	"testing"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/browserctl"
)

// The rows that gate approvals, permissions or sandboxing are reserved, by
// the names their readers use; other rows are not.
func TestReservedConfig(t *testing.T) {
	for _, name := range []string{browserctl.PermissionsSetting, browserctl.AllowlistSetting, "coder_mode", "sandbox.fallback",
		automation.AutoRepairConfig, "coder_projects", "applications.max_parked", "memory.runs", " Memory.Runs ", "workflow.error_workflow", "coder.mission_children_enabled", " Coder.Mission_Children_Enabled ", "WORKFLOW.ERROR_WORKFLOW", " Coder_Mode ", "BROWSER_AGENT_PERMISSIONS",
		"coder-mission-confirm/default/c/t/1", " Coder-Mission-CONFIRM/x", "\tCODER-MISSION-CONFIRM/", "coder-mission-confirm/",
		"file-cleanup/default/c1", " File-Cleanup/x", "FILE-CLEANUP/", "file-cleanup/",
		"agent_purposes.p1.coding", " Agent_Purposes.x", "AGENT_PURPOSES.", "agent_purposes."} {
		if !ReservedConfig(name) {
			t.Errorf("%q is not reserved", name)
		}
	}
	for _, name := range []string{"memory.runs.state", "workflow.error_workflow2", "api_key", "coder_mode.x", "", "coder-mission-confirm", "x/coder-mission-confirm/y", "coder-mission-confirms", "file-cleanup", "x/file-cleanup/y", "file-cleanups", "agent_purposes", "x.agent_purposes.y", "agent_purposesx"} {
		if ReservedConfig(name) {
			t.Errorf("%q is reserved", name)
		}
	}
}
