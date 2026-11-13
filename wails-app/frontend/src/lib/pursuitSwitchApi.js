import * as GoApp from '../wailsjs/go/main/App'

// The pursuit switch bindings (wails-app/app_pursuit_switch.go): each one runs a CLI verb and returns its document as an object.
// The page decides nothing: the notice, the missing items and their fixes are the CLI's words. A refusal is {error, code}.
const parse = s => {
  try { return JSON.parse(s) } catch { return { error: String(s || '') } }
}
const call = fn => Promise.resolve().then(fn).then(parse)

export const pursuitSwitchApi = {
  // `coder host pursuit status`: notice, pauseCommand, savedOperatorOff, savedJudge, pendingRestart, enabled, missing, reason.
  hostStatus: () => call(() => GoApp.PursuitHostStatus()),
  // `task session pursuit readiness ID`: items [{id, status: ok|missing, detail, fix?}], pursuit {started}.
  readiness: sessionId => call(() => GoApp.PursuitReadiness(sessionId)),
  // `task session pursuit pause ID`
  pause: sessionId => call(() => GoApp.PursuitPause(sessionId)),
  // `coder sessions mission-children list` (missionChildrenEnabled) and the switch's enable|disable.
  childrenState: () => call(() => GoApp.MissionChildrenState()),
  setChildren: on => call(() => GoApp.SetMissionChildren(on)),
  // The chat's settings (agent_approval, approval_ceiling_usd) and the lead's approval of child sessions (ceiling above 0 to turn on).
  chatSettings: conversationId => call(() => GoApp.GetChatMissionToggle(conversationId)),
  setApproval: (conversationId, on, ceilingUsd) => call(() => GoApp.SetAgentApproval(conversationId, on, ceilingUsd)),
}
