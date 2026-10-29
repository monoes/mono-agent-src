import * as GoApp from '../wailsjs/go/main/App'

// The access-level bindings (app_history.go), kept apart from services/api.js.
// Each call starts inside a promise, so a synchronous throw (no Wails bridge)
// lands in the caller's catch. A refusal rejects with the CLI's message; the
// CLI's code, when the message names one, is on .code.
function failure(e) {
  const message = String(e?.message || e || '')
  const err = new Error(message)
  err.code = e?.code || (/operator[_ ]only/i.test(message) ? 'operator_only' : '')
  return err
}
const call = fn => Promise.resolve().then(fn).catch(e => { throw failure(e) })

export const chatAccessApi = {
  // A conversation at an access level: scoped has no folder; any other level
  // works in cwd, or in the profile's folder when cwd is ''. Resolves the
  // Conversation ({id, runtimeId, model, effort, access, cwd, orgMode, …}).
  create: (workflowID, runtimeID, model, effort, access, cwd = '') =>
    call(() => GoApp.CreateChatConversationAccess(workflowID, runtimeID, model, effort, access, cwd)),
  // The conversation's authoritative record: {access, mode, runtimeId, model,
  // effort, cwd, orgMode, title (redacted), workflowContext}. The pane never
  // decides what a change means from a cached level; it asks here first.
  record: id => call(() => GoApp.GetChatConversationRecord(id)),
  // Gives the chat a name of the person's own; '' takes it back. Resolves the stored name (cleaned,
  // redacted, cut to length), '' when the name was cleared.
  rename: (id, title) => call(() => GoApp.RenameChatConversation(id, title)),
  // {conversation, previousAccess, lowered: null | {stopped, survivors:[{pid,
  // command, reason}], workersDropped, taskRevoked}}, or a refusal that is not
  // an error: {ok:false, code:'access_changed'|'expected_required', current}.
  // expectedCurrent is the level the caller resolved; confirmedRaise is true
  // only after the person confirmed a raise in the dialog.
  setAccess: (id, level, expectedCurrent, confirmedRaise = false) =>
    call(() => GoApp.SetChatAccess(id, level, expectedCurrent, confirmedRaise)),
}
