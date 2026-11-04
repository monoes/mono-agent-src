package chat

import "testing"

// The chat's Bash allow-list is what keeps a chat from running a single node outside any workflow
// (`node run`), where the send gate sees none of the engine's approval state (issue 1407). The chat's
// sends are queued through the engine path of `workflow run`; this pins that no prefix reaches the
// commands that run one node, read secrets, or settle approvals from a shell.
func TestChatBashAllowlistExcludesTheCommandsThatRunOneNode(t *testing.T) {
	for _, denied := range []string{
		"monoagentcli node run comm.telegram --config {}",
		"monoagentcli node",
		"monoagentcli --profile p node run comm.telegram",
		"monoagentcli publication register",
		"monoagentcli hil resolve",
		"monoagentcli secret reveal x",
		"monoagentcli login gemini",
		"monoagentcli browser allow add x",
		"monoagentcli automation trust x",
		"monomind agent exec",
	} {
		if allowedBashCommand(denied) {
			t.Errorf("the chat's Bash allowlist admits %q", denied)
		}
	}
	// the allowed shapes still pass, so the test cannot pass by an empty allowlist
	for _, allowed := range []string{"monoagentcli workflow run abc", "monoagentcli org list --project /p"} {
		if !allowedBashCommand(allowed) {
			t.Errorf("the chat's Bash allowlist denies %q", allowed)
		}
	}
}
