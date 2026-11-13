package main

import (
	"encoding/json"
	"strconv"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// ─── Coder session settings (the pane) ──────────────────────────────────────
//
// The settings of a coder session (check-ins, autonomy, limits, mesh) and
// whether it may run unattended live in the CLI: `coder sessions settings`.
// These two bindings only run it; the CLI validates and enforces who may change
// what.

// GetCoderSessionSettings returns `coder sessions settings get`: the settings,
// the unattended flag, the access level, which settings are the operator's and
// the values each choice allows.
func (a *App) GetCoderSessionSettings(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "settings", "get", "--", conversationID)
}

// paneSettingKeys are the only settings the settings pane sends (frontend/src/components/bubbles/SessionSettings.jsx). Anything else
// in a patch is refused here: the authority-raising settings (agent_approval and its ceiling, mission, chat_mode, max_depth, ...)
// are written only through the bindings that ask the person in a native dialog (app_mission_authority.go) or the CLI itself.
var paneSettingKeys = map[string]bool{
	"checkin_minutes": true, "skip_if_unchanged": true, "checkin_limit": true, "autonomy_preset": true, "autonomy_classes": true,
	"max_children": true, "max_concurrent": true, "mesh_visible": true, "mesh_alias": true, "mesh_accept": true,
}

const maxSettingsPatchBytes = 16 << 10

// checkSettingsPatch validates a patch from the page: a JSON object of at most maxSettingsPatchBytes whose every key is in
// paneSettingKeys. It returns the patch re-encoded from what was checked (so the CLI never sees a different reading of the same
// text, e.g. a repeated key), whether it names an operator-only setting, and the refusal document or "".
func checkSettingsPatch(patch string) (clean string, operatorOnly bool, refusal string) {
	if len(patch) > maxSettingsPatchBytes {
		return "", false, taskRefusal("the settings patch is too large")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patch), &m); err != nil || m == nil {
		return "", false, taskRefusal("the settings patch must be a JSON object")
	}
	for k := range m {
		if !paneSettingKeys[k] {
			return "", false, taskRefusal("the app does not change the setting " + strconv.Quote(k) + " from here (authority settings are changed by their own confirmed switch or by the CLI)")
		}
		for _, o := range coderhost.OperatorOnly {
			operatorOnly = operatorOnly || o == k
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", false, taskRefusal("the settings patch could not be read")
	}
	return string(raw), operatorOnly, ""
}

// SetCoderSessionSettings runs `coder sessions settings set`: patch is a JSON
// object naming only the settings to change (or ""), unattended is "true",
// "false" or "". Only the settings of the pane are accepted (paneSettingKeys);
// the operator ones among them need the operator gate. It returns the settings
// after the change.
func (a *App) SetCoderSessionSettings(conversationID, patch, unattended string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"coder", "sessions", "settings", "set"}
	if patch != "" {
		clean, operatorOnly, bad := checkSettingsPatch(patch)
		if bad != "" {
			return bad
		}
		if operatorOnly {
			if why := opgate.Refusal(); why != "" {
				return taskRefusal("only the user can change these settings: " + why + " (operator_only)")
			}
		}
		args = append(args, "--patch="+clean)
	}
	if unattended != "" {
		if unattended != "true" && unattended != "false" {
			return taskRefusal("unattended is true or false")
		}
		args = append(args, "--unattended="+unattended)
	}
	return a.jsonResult(append(args, "--", conversationID)...)
}
