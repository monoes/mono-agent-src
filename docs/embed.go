// Package docs embeds the documents the binary prints offline.
package docs

import _ "embed"

// AgentModeWindows is docs/agent-mode-windows.md, printed by `monoagentcli agent-mode windows`.
//
//go:embed agent-mode-windows.md
var AgentModeWindows string
