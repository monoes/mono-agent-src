package main

import (
	"encoding/json"
	"strconv"
	"strings"

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

// wideningsOf says, for a checked patch, what it changes toward fewer asks or a wider audience: the pane keys that need the person's
// native confirmation. The autonomy keys are judged by their RESULT, not by the patch text: a patched autonomy_classes map
// REPLACES the stored one, and a preset change leaves stored overrides in place, so the effective decision of every kind of
// question after the change is computed from the stored settings (read through the CLI) and compared with the one before. If the
// stored settings cannot be read, the change counts as widening. Turning mesh_accept to "none" needs none. The other
// operator-only pane keys (checkin_limit, max_children, max_concurrent) are bounded by the session's budget and need only the
// operator gate.
func (a *App) wideningsOf(conversationID, clean string) []string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(clean), &m)
	var out []string
	_, preset := m["autonomy_preset"]
	_, classes := m["autonomy_classes"]
	if (preset || classes) && a.autonomyWidens(conversationID, m) {
		out = append(out, "fewer asks: the session decides more questions itself, or takes defaults, than it does now")
	}
	if raw, ok := m["mesh_accept"]; ok {
		var accept string
		if json.Unmarshal(raw, &accept) != nil || accept != "none" {
			out = append(out, "accept messages from other sessions (any session of the project may message this one)")
		}
	}
	return out
}

// autonomyWidens says whether applying the autonomy keys of m to the stored settings of the session makes any kind of question
// decided by someone less strict than before (user < master < model, park < take_default), in any combination of reversibility and
// master. Unreadable stored settings, or a value that does not parse, widen.
func (a *App) autonomyWidens(conversationID string, m map[string]json.RawMessage) bool {
	var cur struct {
		Settings coderhost.Settings `json:"settings"`
	}
	if json.Unmarshal([]byte(a.jsonResult("coder", "sessions", "settings", "get", "--", conversationID)), &cur) != nil || cur.Settings.AutonomyPreset == "" {
		return true
	}
	next := cur.Settings
	if raw, ok := m["autonomy_preset"]; ok && json.Unmarshal(raw, &next.AutonomyPreset) != nil {
		return true
	}
	if raw, ok := m["autonomy_classes"]; ok {
		next.AutonomyClasses = map[string]coderhost.AutonomyChoice{}
		if string(raw) != "null" && json.Unmarshal(raw, &next.AutonomyClasses) != nil {
			return true
		}
	}
	rank := func(d coderhost.Decision) int {
		r := map[string]int{coderhost.WhoUser: 0, coderhost.WhoMaster: 1, coderhost.WhoModel: 2}[d.Answerer]*2 + map[string]int{coderhost.FallbackPark: 0, coderhost.FallbackTakeDefault: 1}[d.Fallback]
		return r
	}
	for _, class := range coderhost.GapClasses {
		for _, reversible := range []bool{false, true} {
			for _, hasMaster := range []bool{false, true} {
				if rank(coderhost.Decide(next, class, reversible, hasMaster)) > rank(coderhost.Decide(cur.Settings, class, reversible, hasMaster)) {
					return true
				}
			}
		}
	}
	return false
}

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
		if wide := a.wideningsOf(conversationID, clean); len(wide) > 0 {
			if bad := authorityConfirm(a, "Change this session's settings? "+strings.Join(wide, "; ")+"."); bad != "" {
				return bad
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
