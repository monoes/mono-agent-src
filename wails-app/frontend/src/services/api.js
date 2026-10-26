// Thin wrapper around Wails Go bindings with error handling.
import * as GoApp from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { isInactiveRunError } from '../lib/runFailure.js'

// Global error bus. Read methods degrade to safe defaults ([]/null/0) so pages
// keep rendering, but every failure is also broadcast on `api:error` so a toast
// can surface it — otherwise a dead backend is indistinguishable from empty data.
const errorBus = new EventTarget()

export function onApiError(callback) {
  const handler = (e) => callback(e.detail)
  errorBus.addEventListener('api:error', handler)
  return () => errorBus.removeEventListener('api:error', handler)
}

function reportError(op, e) {
  const message = e?.message || String(e)
  console.warn(`API error (${op}):`, e)
  errorBus.dispatchEvent(new CustomEvent('api:error', { detail: { op, message, code: e?.code || '' } }))
}

// notify surfaces a message on the same toast bus for non-API failures, so UI
// code can report errors without a blocking native alert(). code is the
// CLI's error code, when it gave one (e.g. agent_not_setup, which the toast
// answers with a link to the AI agents page).
export function notify(op, message, code = '') {
  errorBus.dispatchEvent(new CustomEvent('api:error', { detail: { op, message: String(message), code: code || message?.code || '' } }))
}

// startedRun turns a run binding's promise into { ok } / { ok: false, error }.
const startedRun = (p) => Promise.resolve(p).then(
  () => ({ ok: true }),
  (e) => ({ ok: false, error: e?.message || String(e) }),
)

// reportRunFailure toasts a refused run. A workflow that is switched off gets
// an "Activate & run" button: activating stays the user's click, never ours.
export function reportRunFailure(error, { activateAndRun } = {}) {
  const action = activateAndRun && isInactiveRunError(error)
    ? { label: 'Activate & run', run: activateAndRun }
    : null
  errorBus.dispatchEvent(new CustomEvent('api:error', { detail: { op: 'run workflow', message: String(error), code: '', action } }))
}

// Before the first profile exists every profile-scoped read answers "no profile
// selected". That is a state the dashboard shows, not a failure to warn about.
export const isNoProfileError = (e) => /no profile selected/i.test(e?.message || String(e))
export const NO_PROFILE = Object.freeze({ no_profile: true })

// Wrap a binding call so failures are reported and fall back to `fallback`.
const guard = (op, fallback) => (e) => { reportError(op, e); return fallback }

// Mutation fallback: a thrown binding call (or unparseable output) becomes the
// same {error} shape the Go side returns, so callers have one check.
const asError = (e) => ({ error: e?.message || String(e) })

// Parses a chat binding's result, throwing on the synchronous
// {"error": "..."} shape they return when the call never even started —
// turns that JSON payload into an actual promise rejection so callers'
// existing catch blocks run.
function parseStreamResult(s) {
  const r = JSON.parse(s)
  if (r?.error) throw codedError(r.error, r.code)
  return r
}

// An Error carrying the CLI's error code (e.g. agent_not_setup) as .code.
function codedError(message, code) {
  const err = new Error(message)
  if (code) err.code = code
  return err
}

// Bindings that return the CLI's stdout verbatim yield either the payload or
// {"error": "..."}; turn the latter into a rejection so guard() reports it.
const parseCLIJSON = (label) => (raw) => {
  const v = typeof raw === 'string' ? JSON.parse(raw) : raw
  if (v && typeof v === 'object' && !Array.isArray(v) && typeof v.error === 'string' && v.v === undefined) {
    throw codedError(`${label}: ${v.error}`, v.code)
  }
  return v
}

export const api = {
  // Preserve failures so Publication can distinguish an unavailable history from an empty one.
  listPublications: (f = {}) => GoApp.ListPublications(f.search || '', f.platform || '', f.kind || '', f.workflow || '', f.agent || '', f.since || '', f.until || '', f.limit ?? 51, f.offset ?? 0).then(parseCLIJSON('publication list')),
  getPublication: (id) => GoApp.GetPublication(id).then(parseCLIJSON('publication')),
  getPublicationStats: () => GoApp.GetPublicationStats().then(parseCLIJSON('publication stats')),
  getDashboardStats:    () => GoApp.GetDashboardStats().catch(guard('dashboard stats', null)),
  getSummary:           () => GoApp.GetSummary().then(parseCLIJSON('summary')).catch(e => (isNoProfileError(e) ? NO_PROFILE : guard('summary', null)(e))),
  getSummarySections:   (csv) => GoApp.GetSummarySections(csv).then(parseCLIJSON('summary')).catch(guard('summary', null)),
  // `extension status --json`: is a bridge running, and does it accept this
  // client's pairing token. Polled by the dashboard, so failures stay quiet.
  getExtensionStatus:   () => Promise.resolve(GoApp.GetExtensionStatus?.()).then(raw => (raw ? parseCLIJSON('extension status')(raw) : null)).catch(() => null),
  getOrgSummary:        (fast = true) => GoApp.GetOrgSummary(fast).then(parseCLIJSON('org summary')).catch(e => (isNoProfileError(e) ? null : guard('org summary', null)(e))),
  // The dashboard's All profiles view: the same reads across every profile,
  // each row tagged with profile_id/profile_name.
  getGlobalSummary:       () => GoApp.GetGlobalSummary().then(parseCLIJSON('summary')).catch(guard('summary', null)),
  getGlobalOrgSummary:    (fast = true) => GoApp.GetGlobalOrgSummary(fast).then(parseCLIJSON('org summary')).catch(guard('org summary', null)),
  listAllWorkflows:       () => GoApp.ListAllProfilesWorkflows().catch(guard('list workflows', [])),
  getAllRecentExecutions: (limit = 20) => GoApp.GetAllProfilesRecentExecutions(limit).catch(guard('recent executions', [])),
  listWorkflows:        () => GoApp.ListWorkflows().catch(e => (isNoProfileError(e) ? [] : guard('list workflows', [])(e))),
  // Starting a run resolves { ok: true } or { ok: false, error } at once — the
  // caller decides how to report it (an inactive workflow gets an "Activate &
  // run" toast, see reportRunFailure), and never waits on a run that was
  // refused before it started.
  runWorkflow:          (id) => startedRun(GoApp.RunWorkflow(id)),
  runWorkflowWithInput: (id, input) => startedRun(GoApp.RunWorkflowWithInput(id, input || '')),
  // `workflow inputs --json`: which trigger fields a workflow reads, plus a
  // skeleton payload. Returns null rather than throwing — a run must not be
  // blocked because the question could not be asked.
  getWorkflowTriggerInputs: async (id) => {
    try {
      const raw = await GoApp.GetWorkflowTriggerInputs(id)
      const parsed = JSON.parse(raw)
      return parsed?.error ? null : parsed
    } catch (e) {
      console.warn('workflow inputs unavailable', e)
      return null
    }
  },
  setWorkflowActive:    (id, active) => GoApp.SetWorkflowActive(id, active),
  getRecentExecutions:  (limit = 20) => GoApp.GetRecentExecutions(limit).catch(e => (isNoProfileError(e) ? [] : guard('recent executions', [])(e))),
  getWorkflowExecutions:(id, limit = 20) => GoApp.GetWorkflowExecutions(id, limit).catch(guard('workflow executions', [])),
  getExecutionDetail:   (id) => GoApp.GetExecutionDetail(id).catch(guard('execution detail', null)),
  cancelWorkflow:       (id) => GoApp.CancelWorkflow(id).catch(e => { reportError('cancel workflow', e); return `error: ${e}` }),
  getPeople:        (platform = '', search = '', limit = 50, offset = 0) => GoApp.GetPeople(platform, search, limit, offset).catch(guard('get people', null)),
  getPeopleCount:   (platform = '', search = '') => GoApp.GetPeopleCount(platform, search).catch(guard('people count', 0)),
  getSessions:      () => GoApp.GetSessions().catch(guard('get sessions', null)),
  deleteSession:    (id) => GoApp.DeleteSession(id),
  getSocialLists:   () => GoApp.GetSocialLists().catch(guard('social lists', null)),
  getTemplates:     () => GoApp.GetTemplates().catch(guard('templates', null)),
  getLogs:          () => GoApp.GetLogs().catch(guard('logs', [])),
  clearLogs:        () => GoApp.ClearLogs(),
  getDBPath:        () => GoApp.GetDBPath().catch(guard('db path', '')),
  exportData:       () => GoApp.ExportData(),
  isDBConnected:    () => GoApp.IsDBConnected().catch(() => false),
  isReady:          () => GoApp.IsReady().catch(() => false),
  openURL:          (url) => GoApp.OpenURL(url).catch(console.warn),
  getPersonDetail:      (id) => GoApp.GetPersonDetail(id).catch(guard('person detail', null)),
  getPersonInteractions:(id) => GoApp.GetPersonInteractions(id).catch(guard('person interactions', [])),
  getPersonPosts:   (personId) => GoApp.GetPersonPosts(personId).catch(guard('person posts', [])),
  getPersonMessages:(personId) => GoApp.GetPersonMessages(personId).catch(guard('person messages', [])),
  getAllPersonMessages:(limit) => GoApp.GetAllPersonMessages(limit ?? 200).catch(guard('all messages', [])),
  markPersonMessagesRead: (personId, ids = []) => GoApp.MarkPersonMessagesRead(personId || '', ids).catch(guard('mark messages read', null)),
  markPersonMessageUnread: (id) => GoApp.MarkPersonMessageUnread(id).catch(guard('mark message unread', null)),
  composePersonMessage:(personId, connectionId, subject, body, asDraft) => GoApp.ComposePersonMessage(personId, connectionId, subject, body, asDraft),
  getDraftPersonMessages: () => GoApp.GetDraftPersonMessages().catch(guard('draft messages', [])),
  sendDraftPersonMessage: (id) => GoApp.SendDraftPersonMessage(id),
  rejectDraftPersonMessage: (id) => GoApp.RejectDraftPersonMessage(id),
  getPendingPeopleApprovals: () => (GoApp.GetPendingPeopleApprovals ? GoApp.GetPendingPeopleApprovals() : Promise.resolve([])).catch(guard('pending people', [])),
  approvePendingPerson: (id, intro = '', sendNow = false) => GoApp.ApprovePendingPerson(id, intro, sendNow),
  deletePeople: (ids) => GoApp.DeletePeople(ids),
  rejectPendingPerson: (id) => GoApp.RejectPendingPerson(id),
  getLatestPersonStatus: (personId) => GoApp.GetLatestPersonStatus(personId).catch(guard('latest status', null)),
  addPersonStatus:       (personId, text) => GoApp.AddPersonStatus(personId, text).catch(guard('add status', null)),
  getPersonStatusHistory:(personId, limit) => GoApp.GetPersonStatusHistory(personId, limit ?? 0).catch(guard('status history', [])),
  getPostDetail:    (postId)   => GoApp.GetPostDetail(postId).catch(guard('post detail', null)),
  getPostComments:  (postId)   => GoApp.GetPostComments(postId).catch(guard('post comments', [])),
  getAllTags:            ()  => GoApp.GetAllTags().catch(guard('tags', [])),
  getPersonTags:        (personId) => GoApp.GetPersonTags(personId).catch(guard('person tags', [])),
  addPersonTag:         (personId, name, color) => GoApp.AddPersonTag(personId, name, color).catch(guard('add tag', null)),
  updateTagColor:       (tagId, color) => (GoApp.UpdateTagColor ? GoApp.UpdateTagColor(tagId, color) : Promise.resolve(false)).catch(() => false),
  removePersonTag:      (personId, tagId) => GoApp.RemovePersonTag(personId, tagId).catch(e => reportError('remove tag', e)),
  getPeopleTagsMap:     (ids) => GoApp.GetPeopleTagsMap(ids).catch(guard('tags map', {})),
  listConnections:      (platform = '') => GoApp.ListConnections(platform).catch(guard('list connections', [])),
  platformNames:        () => GoApp.PlatformNames().catch(guard('platform names', {})),
  listPlatforms:        (connectVia = '') => GoApp.ListPlatformsJSON(connectVia).then(s => JSON.parse(s)).catch(guard('list platforms', [])),
  testConnection:       (id) => GoApp.TestConnection(id).catch(e => { reportError('test connection', e); return `error: ${e}` }),
  testSession:          (id) => GoApp.TestSession(id).catch(e => { reportError('test session', e); return `error: ${e}` }),
  removeConnection:     (id) => GoApp.RemoveConnection(id).catch(e => { reportError('remove connection', e); return `error: ${e}` }),
  getConnectionsForPlatform: (platformID) => GoApp.GetConnectionsForPlatform(platformID).catch(guard('connections for platform', [])),
  nodeConfigFromCurl:   (command) => GoApp.NodeConfigFromCurl(command).catch(e => ({ config: null, notes: [], error: String(e?.message || e) })),
  saveConnectionDirect: (platformID, method, fieldValues) =>
    GoApp.SaveConnectionDirect(platformID, method, JSON.stringify(fieldValues))
      .catch(e => { reportError('save connection', e); return `error: ${e}` }),
  // `connect show --json`: a platform's methods, fields and OAuth connect fields.
  showPlatform:           (platformID) => GoApp.ShowPlatform(platformID).then(s => (s ? JSON.parse(s) : null)).catch(guard('show platform', null)),
  // fieldPairs go to `connect oauth` as --field key=value; secretJSON (the
  // secret fields as a JSON object) goes on its stdin.
  connectPlatformOAuth:   (platformID, fieldPairs = [], secretJSON = '') => GoApp.ConnectPlatformOAuthFields(platformID, fieldPairs, secretJSON),
  cancelPlatformOAuth:    (platformID)                       => GoApp.CancelPlatformOAuth(platformID),
  loginSocial:            (platform)                         => GoApp.LoginSocial(platform),
  confirmSocialLogin:     (platform)                         => GoApp.ConfirmSocialLogin(platform),
  getOAuthCredentials:    (platformID)                       => GoApp.GetOAuthCredentials(platformID).catch(guard('oauth credentials', '')),
  setOAuthCredentials:    (platformID, clientID, clientSecret) => GoApp.SetOAuthCredentials(platformID, clientID, clientSecret),
  // Agent Chat (monomind delegation — local AI agent runtimes)
  scanAgentRuntimes:  () => GoApp.ScanAgentRuntimes().then(s => JSON.parse(s)).catch(guard('scan agent runtimes', null)),
  // binary is a runtime's ScanEntry.binary (from scanAgentRuntimes) — required
  // for antigravity/codex, which discover their own model catalog by shelling
  // out to themselves; harmless to omit for claude (curated list, ignores it).
  getAgentRuntimeModels: (runtimeID, binary) => GoApp.GetAgentRuntimeModels(runtimeID, binary || '').then(s => JSON.parse(s)).catch(guard('agent runtime models', [])),
  // Validated agent roster (#225): `agent roster|validate --json`. Errors
  // come back as {error} objects, never rejections.
  agentRoster:          () => GoApp.AgentRoster().then(parseRosterJSON).catch(e => ({ error: String(e) })),
  agentValidatePlan:    (runtimes = [], models = [], staleOnly = false) => GoApp.AgentValidatePlan(runtimes, models, staleOnly).then(parseRosterJSON).catch(e => ({ error: String(e) })),
  startAgentValidation: (runtimes = [], models = [], staleOnly = false) => GoApp.StartAgentValidation(runtimes, models, staleOnly).then(parseRosterJSON).catch(e => ({ error: String(e) })),
  stopAgentValidation:  () => GoApp.StopAgentValidation().then(parseRosterJSON).catch(e => ({ error: String(e) })),
  agentRosterAdd:       (runtimeID, model) => GoApp.AgentRosterAdd(runtimeID, model).then(parseRosterJSON).catch(e => ({ error: String(e) })),
  agentRosterRemove:    (runtimeID, model) => GoApp.AgentRosterRemove(runtimeID, model).then(parseRosterJSON).catch(e => ({ error: String(e) })),
  // Automatic re-validation (#230): `agent roster auto-revalidate status|on|off --json`.
  agentRosterAutoRevalidate:    () => GoApp.AgentRosterAutoRevalidate().then(parseRosterJSON).catch(e => ({ error: String(e) })),
  setAgentRosterAutoRevalidate: on => GoApp.SetAgentRosterAutoRevalidate(!!on).then(parseRosterJSON).catch(e => ({ error: String(e) })),
  // New chat bindings (interactive-agent-chat plan §"Proposed Wails
  // bindings"). Every call goes through parseStreamResult: a synchronous
  // {"error":...} shape
  // must become a real rejection, never a resolved value the caller has to
  // remember to check. A business-status reply (e.g. StartChatTurn's
  // {ok:false,status:"busy"}) is NOT that shape, so it passes through as a
  // normal value for the caller to branch on.
  createChatConversation: (workflowID, runtimeID, model, effort = '') =>
    GoApp.CreateChatConversation(workflowID, runtimeID, model, effort).then(parseStreamResult),
  // attachments: file paths for a coder turn (see saveChatAttachment).
  startChatTurn: (conversationID, turnID, message, tools, allowRuns, attachments = []) =>
    GoApp.StartChatTurn(conversationID, turnID, message, tools, allowRuns, attachments || []).then(parseStreamResult),
  // A coder chat's attachment, base64 (≤ 20 MB) → a spool file path; and an
  // image inside the chat's folder (≤ 10 MB) → a data URL. Both reject on
  // error.
  saveChatAttachment: (conversationID, name, base64) => GoApp.SaveChatAttachment(conversationID, name, base64),
  readWorkspaceImage: (conversationID, path) => GoApp.ReadWorkspaceImage(conversationID, path),
  // A browser submit approval's screenshot (under ~/.monoagent/browser/hil/)
  // → a data URL; rejects for anything else.
  readHILScreenshot: (path) => GoApp.ReadHILScreenshot(path),
  // Agent browser permissions (machine-wide, `browser permissions …`):
  // {levels, sites, kinds, catalog, you}; a refusal rejects with its code.
  browserPermissions:   () => GoApp.BrowserPermissions().then(parseStreamResult),
  setBrowserPermission: (kind, level) => GoApp.SetBrowserPermission(kind, level).then(parseStreamResult),
  setBrowserSiteRule:   (origin, rule) => GoApp.SetBrowserSiteRule(origin, rule).then(parseStreamResult),
  stopChatTurn: (conversationID, turnID) =>
    GoApp.StopChatTurn(conversationID, turnID).then(parseStreamResult),
  // One worker of a dynamic-org turn; the lead and the other workers keep
  // running (#255).
  stopChatAgent: (conversationID, turnID, agentID) =>
    GoApp.StopChatAgent(conversationID, turnID, agentID).then(parseStreamResult),
  listChatConversations: (cursor = '', limit = 50) =>
    GoApp.ListChatConversations(cursor, limit).then(parseStreamResult),
  getChatTurns: (conversationID, cursor = '', limit = 50) =>
    GoApp.GetChatTurns(conversationID, cursor, limit).then(parseStreamResult),
  getChatEvents: (conversationID, turnID, afterSeq = 0, limit = 200) =>
    GoApp.GetChatEvents(conversationID, turnID, afterSeq, limit).then(parseStreamResult),
  deleteChatConversation: (conversationID) =>
    GoApp.DeleteChatConversation(conversationID).then(parseStreamResult),
  // Coder mode (#203): `monoagentcli coder …` and coder conversations. Same
  // parseStreamResult contract, so a refusal rejects with the CLI's code
  // (coder_disabled, needs_monomind_update) on err.code.
  coderStatus:        () => GoApp.CoderStatus().then(parseStreamResult),
  coderEnable:        () => GoApp.CoderEnable().then(parseStreamResult),
  coderDisable:       () => GoApp.CoderDisable().then(parseStreamResult),
  // budgetUsd 0 clears the per-turn budget; orgRouter is auto|jev|model, ''
  // leaves it alone. See coderSetArgs (app_coder.go).
  coderSet:           ({ workspaceRoot = '', maxTurns = 0, timeout = '', budgetUsd = -1, orgRouter = '' }) =>
    GoApp.CoderSet(workspaceRoot, maxTurns, timeout, budgetUsd, orgRouter).then(parseStreamResult),
  // {stopped, gone, refused} pids of a turn's leftover background processes.
  // Dynamic org (#226): 'solo' or 'dynamic', from the next turn on.
  // Answer a dynamic-org worker's question (#256).
  answerAgentQuestion: (conversationID, turnID, agentID, questionID, text) =>
    GoApp.AnswerAgentQuestion(conversationID, turnID, agentID, questionID, text).then(parseStreamResult),
  setChatOrgMode: (conversationID, mode) =>
    GoApp.SetChatOrgMode(conversationID, mode).then(parseStreamResult),
  getCoderSessionSettings: (conversationID) =>
    GoApp.GetCoderSessionSettings(conversationID).then(parseStreamResult),
  setCoderSessionSettings: (conversationID, patch, unattended = '') =>
    GoApp.SetCoderSessionSettings(conversationID, patch ? JSON.stringify(patch) : '', unattended).then(parseStreamResult),
  coderStopBackground: (conversationID, turnID) =>
    GoApp.CoderStopBackground(conversationID, turnID).then(parseStreamResult),
  // The active profile's own folder, set up for the runtime: {path, profile:{id,name}, source, init}.
  coderWorkspaceDefault: (runtimeID = '') => GoApp.CoderWorkspaceDefault(runtimeID).then(parseStreamResult),
  getActiveProfile: () => GoApp.GetActiveProfile(),
  getProfiles: () => GoApp.GetProfiles(),
  // Send approval (coder-bubble-v2 point 3): an agent's outbound send waits as a
  // HIL item of kind "send" (also in getHILItems as item.send). Payload shape:
  // internal/hilsend/types.go. hilSendResolve performs the previewed payload
  // once on approve (editText/editSubject/alwaysAllow optional).
  hilSendList:         (state = '') => GoApp.HilSendList(state).then(parseStreamResult),
  hilSendShow:         (id) => GoApp.HilSendShow(id).then(parseStreamResult),
  hilSendEdit:         (id, text = '', subject = '') => GoApp.HilSendEdit(id, text, subject).then(parseStreamResult),
  hilSendResolve:      (id, approve, { editText = '', editSubject = '', alwaysAllow = false } = {}) =>
    GoApp.HilSendResolve(id, approve, editText, editSubject, alwaysAllow).then(parseStreamResult),
  hilSendAttachment:     (id, n) => GoApp.HilSendAttachment(id, n).then(parseStreamResult),
  hilSendAttachmentData: (id, n) => GoApp.HilSendAttachmentData(id, n),
  hilSendRecipient:    (id) => GoApp.HilSendRecipient(id).then(parseStreamResult),
  hilSendAllowList:    () => GoApp.HilSendAllowList().then(parseStreamResult),
  hilSendAllowAdd:     (action, platform, account, recipient) =>
    GoApp.HilSendAllowAdd(action, platform, account, recipient).then(parseStreamResult),
  hilSendAllowRemove:  (ruleID) => GoApp.HilSendAllowRemove(ruleID).then(parseStreamResult),
  hilSendSetApproval:  (on) => GoApp.HilSendSetApproval(on).then(parseStreamResult),
  // `coder set --app-access on|off` (operator only): whether a coder chat's
  // workers get the workspace profile. Returns coder status ({appAccess}).
  coderSetDefaultAccess: (level) => GoApp.CoderSetDefaultAccess(level).then(parseStreamResult),
  coderSetAppAccess:   (on) => GoApp.CoderSetAppAccess(!!on).then(parseStreamResult),
  coderWorkspaceRoot: (runtimeID = '') => GoApp.CoderWorkspaceRoot(runtimeID).then(parseStreamResult),
  coderWorkspaceList: () => GoApp.CoderWorkspaceList().then(parseStreamResult),
  // Sandbox fallback setting: `monoagentcli sandbox fallback`; a bad value rejects with the CLI's message.
  sandboxFallbackGet: () => GoApp.SandboxFallbackGet().then(parseStreamResult),
  sandboxFallbackSet: (value) => GoApp.SandboxFallbackSet(value).then(parseStreamResult),
  createCoderConversation: (runtimeID, model, effort, cwd, newWorkspace = false) =>
    GoApp.CreateCoderConversation(runtimeID, model, effort || '', cwd, newWorkspace).then(parseStreamResult),
  // Native folder picker; resolves '' when cancelled.
  pickCoderFolder:    () => GoApp.PickCoderFolder(),
  // Queued coder chats (C5): the browser extension queues them, Start here
  // runs the first turn. `coder queue …` through the CLI, with the same
  // parseStreamResult contract.
  coderQueueList:     () => GoApp.QueueList().then(parseStreamResult),
  coderQueueStart:    (id, cwd = '') => GoApp.QueueStart(id, cwd).then(parseStreamResult),
  coderQueueDismiss:  (id) => GoApp.QueueDismiss(id).then(parseStreamResult),
  coderQueueImage:    (id) => GoApp.QueueImage(id).then(parseStreamResult),
  // coder:queue fires when a chat was queued, started or dismissed (the app
  // watches the queue folder); the payload is empty, so list again. Returns
  // the unsubscribe.
  onCoderQueue:       (callback) => subscribeEvent('coder:queue', callback),
  openPathWithOS:     (path) => GoApp.OpenPathWithOS(path),
  // Unified chat and org-run history (ListHistory pages, DeleteChatConversations).
  listHistory:        (cursor, limit, filter) => GoApp.ListHistory(cursor || '', limit, filter || {}),
  deleteChatConversations: (ids) => GoApp.DeleteChatConversations(ids),
  // Orgs (monomind Org Runtime v2)
  listOrgs:           () => GoApp.ListOrgs().then(s => JSON.parse(s)).catch(guard('list orgs', null)),
  getOrgStatus:       (name = '') => GoApp.GetOrgStatus(name).then(s => JSON.parse(s)).catch(guard('org status', null)),
  getOrgLogs:         (name, run = '') => GoApp.GetOrgLogs(name, run).then(s => JSON.parse(s)).catch(guard('org logs', null)),
  getOrgReport:       (name, all = false, run = '') => GoApp.GetOrgReport(name, all, run).then(s => JSON.parse(s)).catch(guard('org report', null)),
  getOrgBudget:       (name, run = '') => GoApp.GetOrgBudget(name, run).then(s => JSON.parse(s)).catch(guard('org budget', null)),
  getOrgEstimate:     (name) => GoApp.GetOrgEstimate(name).then(s => JSON.parse(s)).catch(guard('org estimate', null)),
  getOrgCosts:        (name, run = '') => GoApp.GetOrgCosts(name, run).then(s => JSON.parse(s)).catch(guard('org costs', null)),
  getOrgFlow:         (name, run = '') => GoApp.GetOrgFlow(name, run).then(s => JSON.parse(s)).catch(guard('org flow', null)),
  getOrgQuestions:    (name) => GoApp.GetOrgQuestions(name).then(s => JSON.parse(s)).catch(guard('org questions', null)),
  getOrgApprovals:    (name) => GoApp.GetOrgApprovals(name).then(s => JSON.parse(s)).catch(guard('org approvals', null)),
  getOrgGates:        (name) => GoApp.GetOrgGates(name).then(s => JSON.parse(s)).catch(guard('org gates', null)),
  getOrgDecisions:    (name, run = '') => GoApp.GetOrgDecisions(name, run).then(s => JSON.parse(s)).catch(guard('org decisions', null)),
  getOrgDocuments:    (name, run = '') => GoApp.GetOrgDocuments(name, run).then(s => JSON.parse(s)).catch(guard('org documents', null)),
  getOrgScheduleAudit: (name) => GoApp.GetOrgScheduleAudit(name).then(s => JSON.parse(s)).catch(guard('org schedule audit', null)),
  setOrgSchedule:     (name, schedule) => GoApp.SetOrgSchedule(name, schedule).then(s => JSON.parse(s)).catch(guard('org schedule', { error: 'Could not save the schedule.' })),
  getOrgMemoryStats:  (name) => GoApp.GetOrgMemoryStats(name).then(s => JSON.parse(s)).catch(guard('org memory stats', null)),
  answerOrgQuestion:  (name, questionID, answer) => GoApp.AnswerOrgQuestion(name, questionID, answer).then(s => JSON.parse(s)),
  approveOrgAction:   (name, role, action) => GoApp.ApproveOrgAction(name, role, action).then(s => JSON.parse(s)),
  denyOrgAction:      (name, role, action) => GoApp.DenyOrgAction(name, role, action).then(s => JSON.parse(s)),
  gateApproveOrgAction: (name, gateID, resolution = '') => GoApp.GateApproveOrgAction(name, gateID, resolution).then(s => JSON.parse(s)),
  gateRejectOrgAction:  (name, gateID, resolution = '') => GoApp.GateRejectOrgAction(name, gateID, resolution).then(s => JSON.parse(s)),
  // streamId (newOrgEventsStreamId) names one tail, so the stop that ends it
  // can't be lost to a race with the start or end a newer tail by mistake.
  streamOrgEvents:    (orgName, streamId) => GoApp.StreamOrgEvents(orgName, streamId).then(s => JSON.parse(s)),
  stopOrgEvents:      (orgName, streamId) => GoApp.StopOrgEvents(orgName, streamId).then(s => JSON.parse(s)).catch(guard('stop org events', null)),
  runOrg:             (orgName, task = '') => GoApp.RunOrg(orgName, task).then(s => JSON.parse(s)),
  // Org bubbles (#229): `org chat …` and `org stop|pause|resume`. Each
  // resolves to the CLI's JSON or {error}; they never reject.
  getOrgChatHistory:  (org, run = '') => GoApp.GetOrgChatHistory(org, run).then(s => JSON.parse(s)).catch(asError),
  sendOrgChat:        (org, text) => GoApp.SendOrgChat(org, text).then(s => JSON.parse(s)).catch(asError),
  answerOrgChat:      (org, questionID, answer) => GoApp.AnswerOrgChat(org, questionID, answer).then(s => JSON.parse(s)).catch(asError),
  dismissOrgChat:     (org, questionID, reason = '') => GoApp.DismissOrgChat(org, questionID, reason).then(s => JSON.parse(s)).catch(asError),
  resolveOrgChat:     (org, ref, approve, note = '') => GoApp.ResolveOrgChat(org, ref, approve, note).then(s => JSON.parse(s)).catch(asError),
  controlOrg:         (org, verb) => GoApp.ControlOrg(org, verb).then(s => JSON.parse(s)).catch(asError),
  // Bubble asks (`monoagentcli hil …`, app_hil_ui.go): only services/hilApi.js calls these.
  hilList:            () => GoApp.HILList().then(s => JSON.parse(s)).catch(asError),
  hilResolve:         (id, { approve, editText = '', data } = {}) => GoApp.HILResolve(id, !!approve, editText || '', data ? JSON.stringify(data) : '').then(s => JSON.parse(s)).catch(asError),
  hilAttachment:      (id, n) => GoApp.HILAttachment(id, n).then(s => JSON.parse(s)).catch(asError),
  // Org Designer — direct config-file read/write, distinct from the org
  // observe/action surface above (which proxies `monoagentcli org <sub>`,
  // read-only + question/gate actions). See wails-app/app_orgs_design.go.
  getOrgDesign:        (name) => GoApp.GetOrgDesign(name).then(s => JSON.parse(s)).catch(guard('org design', null)),
  listOrgDesigns:      () => GoApp.ListOrgDesigns().then(s => JSON.parse(s)).catch(guard('org designs', null)),
  createOrgDesign:     (spec) => GoApp.CreateOrgDesign(JSON.stringify(spec)).then(s => JSON.parse(s)),
  deleteOrgDesign:     (name) => GoApp.DeleteOrgDesign(name).then(s => JSON.parse(s)),
  addOrgRole:          (name, role) => GoApp.AddOrgRole(name, JSON.stringify(role)).then(s => JSON.parse(s)),
  updateOrgRole:       (name, roleID, patch) => GoApp.UpdateOrgRole(name, roleID, JSON.stringify(patch)).then(s => JSON.parse(s)),
  removeOrgRole:       (name, roleID, strategy = 'reparent') => GoApp.RemoveOrgRole(name, roleID, strategy).then(s => JSON.parse(s)),
  setOrgRoleReportsTo: (name, roleID, parentID = '') => GoApp.SetOrgRoleReportsTo(name, roleID, parentID).then(s => JSON.parse(s)),
  promoteRoleToRoot:   (name, roleID) => GoApp.PromoteRoleToRoot(name, roleID).then(s => JSON.parse(s)),
  chooseInstructionsFile: () => GoApp.ChooseInstructionsFile(),
  saveOrgLayout:       (name, layout) => GoApp.SaveOrgLayout(name, JSON.stringify(layout)).then(s => JSON.parse(s)),
  // Sections (monomind org-runtime §6.7): app_orgs_sections.go. Each call
  // resolves {ok, rev, org} or {error} (the refusal reason).
  addOrgSection:       (name, section, spec) => GoApp.AddOrgSection(name, section, JSON.stringify(spec)).then(s => JSON.parse(s)),
  updateOrgSection:    (name, section, patch) => GoApp.UpdateOrgSection(name, section, JSON.stringify(patch)).then(s => JSON.parse(s)),
  deleteOrgSection:    (name, section, moveTo = '') => GoApp.DeleteOrgSection(name, section, moveTo).then(s => JSON.parse(s)),
  assignOrgRole:       (name, roleID, section) => GoApp.AssignOrgRole(name, roleID, section).then(s => JSON.parse(s)),
  addOrgRoleToSection: (name, section, role) => GoApp.AddOrgRoleToSection(name, section, JSON.stringify(role)).then(s => JSON.parse(s)),
  addOrgDocumentEdge:  (name, from, to, docType) => GoApp.AddOrgDocumentEdge(name, from, to, docType).then(s => JSON.parse(s)),
  removeOrgDocumentEdge: (name, from, to, docType) => GoApp.RemoveOrgDocumentEdge(name, from, to, docType).then(s => JSON.parse(s)),
  saveOrgDesign:       (name, doc) => GoApp.SaveOrgDesign(name, JSON.stringify(doc)).then(s => JSON.parse(s)),
  // Full-access roles (#205): grant ('full', only after the confirm dialog)
  // or revoke ('scoped'); a refusal rejects with the CLI's text verbatim.
  orgRoleSetAccess:    (name, roleID, access) => GoApp.OrgRoleSetAccess(name, roleID, access).then(parseStreamResult),
  // Signed org definitions (#288, monomind 2.21): the state
  // {supported, state, detail, sha256, message}, monomind's review (signs
  // nothing), and signing the reviewed definition (the review's hash,
  // instructions files included). Refusals reject
  // with the CLI's text.
  orgSignatureStatus:  (name) => GoApp.OrgSignatureStatus(name).then(parseStreamResult),
  orgSignatureReview:  (name) => GoApp.OrgSignatureReview(name).then(parseStreamResult),
  orgSign:             (name, hash) => GoApp.OrgSign(name, hash).then(parseStreamResult),
  // `org validate`'s report {valid, error?, warnings}, taint problems
  // included. An invalid org's report has "error" too, so only a reply
  // without "valid" is a failure.
  validateOrgReport:   (name) => GoApp.ValidateOrgReport(name).then(s => {
    const r = JSON.parse(s)
    if (typeof r?.valid !== 'boolean' && r?.error) throw codedError(r.error, r.code)
    return r
  }),
  // Runtimes a sections org refuses or flags unverified, with monomind's reasons.
  orgSectionsRuntimes: () => GoApp.OrgSectionsRuntimes().then(s => {
    const r = JSON.parse(s)
    if (r?.error) throw codedError(r.error, r.code)
    return r
  }),
  validateOrgDesign:   (name) => GoApp.ValidateOrgDesign(name).then(s => JSON.parse(s)).catch(guard('validate org design', null)),
  reloadOrg:           (name) => GoApp.ReloadOrg(name).then(s => JSON.parse(s)),
  // Org × workflow unification — grants, automations, automation roles,
  // autonomy, holding groups. Every call shells `monoagentcli org …` (see
  // wails-app/app_org_unification.go). Reads fall back to null and toast;
  // mutations resolve to the CLI's JSON or {error} so callers check res.error.
  listOrgAutomations:        (org) => GoApp.ListOrgAutomations(org).then(s => JSON.parse(s)).catch(guard('org automations', null)),
  listUnassignedAutomations: () => GoApp.ListUnassignedAutomations().then(s => JSON.parse(s)).catch(guard('unassigned automations', null)),
  addOrgAutomation:          (org, workflowID, alias) => GoApp.AddOrgAutomation(org, workflowID, alias).then(s => JSON.parse(s)).catch(asError),
  removeOrgAutomation:       (org, alias) => GoApp.RemoveOrgAutomation(org, alias).then(s => JSON.parse(s)).catch(asError),
  listOrgGrants:             (org) => GoApp.ListOrgGrants(org).then(s => JSON.parse(s)).catch(guard('org grants', null)),
  setOrgGrant:               (org, spec) => GoApp.SetOrgGrant(org, JSON.stringify(spec)).then(s => JSON.parse(s)).catch(asError),
  removeOrgGrant:            (org, role, alias) => GoApp.RemoveOrgGrant(org, role, alias).then(s => JSON.parse(s)).catch(asError),
  addAutomationRole:         (org, spec) => GoApp.AddAutomationRole(org, JSON.stringify(spec)).then(s => JSON.parse(s)).catch(asError),
  removeAutomationRole:      (org, roleID) => GoApp.RemoveAutomationRole(org, roleID).then(s => JSON.parse(s)).catch(asError),
  getEffectiveTools:         (org, roleID) => GoApp.GetEffectiveTools(org, roleID).then(s => JSON.parse(s)).catch(guard('effective tools', null)),
  getOrgAutonomy:            (org) => GoApp.GetOrgAutonomy(org).then(s => JSON.parse(s)).catch(guard('org autonomy', null)),
  setOrgAutonomy:            (org, spec) => GoApp.SetOrgAutonomy(org, JSON.stringify(spec)).then(s => JSON.parse(s)).catch(asError),
  pauseOrgAutonomy:          (org, duration = '') => GoApp.PauseOrgAutonomy(org, duration).then(s => JSON.parse(s)).catch(asError),
  resumeOrgAutonomy:         (org) => GoApp.ResumeOrgAutonomy(org).then(s => JSON.parse(s)).catch(asError),
  listOrgDecisionLog:        (org, run = '') => GoApp.ListOrgDecisionLog(org, run).then(s => JSON.parse(s)).catch(guard('decision log', null)),
  listNeedsYou:              (org) => GoApp.ListNeedsYou(org).then(s => JSON.parse(s)).catch(guard('needs you', null)),
  // C-35: messages queued for the org's next start (read-only `org queued`).
  listOrgQueuedMessages:     (org) => GoApp.ListOrgQueuedMessages(org).then(s => JSON.parse(s)).catch(asError),
  startOrgGroup:             (holding) => GoApp.StartOrgGroup(holding).then(s => JSON.parse(s)).catch(asError),
  stopOrgGroup:              (holding) => GoApp.StopOrgGroup(holding).then(s => JSON.parse(s)).catch(asError),
  orgGroupStatus:            (holding) => GoApp.OrgGroupStatus(holding).then(s => JSON.parse(s)).catch(guard('org group status', null)),
  sendOrgMessage:            (org, spec) => GoApp.SendOrgMessage(org, JSON.stringify(spec)).then(s => JSON.parse(s)).catch(asError),
  getDaemonStatus:           () => GoApp.GetDaemonStatus().then(s => JSON.parse(s)).catch(guard('daemon status', null)),
  // Per-profile monomind setup — see wails-app/app_monomind_init.go.
  isMonomindInitialized:   () => GoApp.IsMonomindInitialized().catch(guard('monomind init status', false)),
  initializeMonomindProfile: () => GoApp.InitializeMonomindProfile().then(s => JSON.parse(s)),
  // Chat result artifacts (chat/chatArtifacts.js) — both already existed as
  // typed Go methods (GetWorkflow returns a real profile_id-checked error
  // for a deleted/cross-profile id; ListProfileDocuments is already
  // profile-scoped) with generated Wails bindings, just no api.js wrapper
  // yet. Typed struct returns, not JSON strings — no .then(JSON.parse).
  getWorkflow:            (id) => GoApp.GetWorkflow(id).catch(guard('get workflow', null)),
  listProfileDocuments:   () => GoApp.ListProfileDocuments().catch(guard('list profile documents', [])),
  getProfileDocument:     (id) => GoApp.GetProfileDocument(id).catch(guard('get profile document', null)),
  // Workflow import screen: `workflow import --json` verbatim (status,
  // bundled automations, missingAutomations, installCommand) or {error}.
  // opts: {asNew, yes}. See wails-app/app_workflow_import.go.
  importWorkflowFull:     (input, opts = {}) => GoApp.ImportWorkflowFull(input, JSON.stringify(opts || {})).then(JSON.parse).catch(asError),
  chooseWorkflowFile:     () => GoApp.ChooseWorkflowFile().catch(guard('choose workflow file', '')),
  // Browser automation packages and recordings (Connections page). Every
  // call shells `monoagentcli automation|action|record … --json` (see
  // wails-app/app_automations.go) and resolves to the CLI's JSON or to
  // {error}, so callers check res.error and render it inline.
  listAutomations:        () => GoApp.ListAutomations().then(JSON.parse).catch(asError),
  showAutomation:         (id) => GoApp.ShowAutomation(id).then(JSON.parse).catch(asError),
  installAutomationDryRun:(path) => GoApp.InstallAutomationDryRun(path).then(JSON.parse).catch(asError),
  // spec: {expectSha256 (from the dry run), replaceBuiltin}
  installAutomation:      (path, spec = {}) => GoApp.InstallAutomation(path, JSON.stringify(spec || {})).then(JSON.parse).catch(asError),
  exportAutomation:       (id, path) => GoApp.ExportAutomation(id, path).then(JSON.parse).catch(asError),
  exportAction:           (ref, path) => GoApp.ExportAction(ref, path).then(JSON.parse).catch(asError),
  uninstallAutomation:    (id) => GoApp.UninstallAutomation(id).then(JSON.parse).catch(asError),
  restoreAutomation:      (id) => GoApp.RestoreAutomation(id).then(JSON.parse).catch(asError),
  enableAutomation:       (id) => GoApp.EnableAutomation(id).then(JSON.parse).catch(asError),
  disableAutomation:      (id) => GoApp.DisableAutomation(id).then(JSON.parse).catch(asError),
  rollbackAutomation:     (id) => GoApp.RollbackAutomation(id).then(JSON.parse).catch(asError),
  // flag: scripts | no-scripts | live | no-live
  setAutomationTrust:     (id, flag) => GoApp.SetAutomationTrust(id, flag).then(JSON.parse).catch(asError),
  validateAutomation:     (path) => GoApp.ValidateAutomation(path).then(JSON.parse).catch(asError),
  testAutomation:         (id, action = '', live = false) => GoApp.TestAutomation(id, action, live).then(JSON.parse).catch(asError),
  // Opens the site and replaces one selector with the element the user clicks.
  rerecordSelector:       (id, key) => GoApp.RerecordSelector(id, key).then(JSON.parse).catch(asError),
  doctorAutomations:      (id = '') => GoApp.DoctorAutomations(id).then(JSON.parse).catch(asError),
  listRecordings:         () => GoApp.ListRecordings().then(JSON.parse).catch(asError),
  showRecording:          (id) => GoApp.ShowRecording(id).then(JSON.parse).catch(asError),
  deleteRecording:        (id) => GoApp.DeleteRecording(id).then(JSON.parse).catch(asError),
  analyzeRecording:       (id, automation = '', advanced = false) => GoApp.AnalyzeRecording(id, automation, advanced).then(JSON.parse).catch(asError),
  // inputs: {name: value} for values the recording could not hold (secrets).
  verifyDraft:            (draftDir, full = false, inputs = {}) => GoApp.VerifyDraft(draftDir, full, JSON.stringify(inputs || {})).then(JSON.parse).catch(asError),
  // spec: {as, automation, new, name, renameInputs: {aiName: userName}}
  saveDraft:              (draftDir, spec) => GoApp.SaveDraft(draftDir, JSON.stringify(spec || {})).then(JSON.parse).catch(asError),
  // Native pickers; resolve to '' when cancelled.
  chooseAutomationPackage:    () => GoApp.ChooseAutomationPackage().catch(guard('choose package', '')),
  chooseAutomationExportPath: (defaultName = '') => GoApp.ChooseAutomationExportPath(defaultName).catch(guard('choose export path', '')),
  getVaultImage:          (id) => GoApp.GetVaultImage(id).catch(guard('get vault image', null)),
  getVaultImages:         (limit = 200) => GoApp.GetVaultImages(limit).catch(guard('get vault images', [])),
}

// The Wails runtime (window.runtime / window.go) only exists inside the desktop
// shell. In a plain browser (vite dev server) EventsOn would throw, so no-op
// after one warning instead of crashing the page.
export function hasWails() {
  return typeof window !== 'undefined' && !!(window.runtime || window.go)
}

let warnedNoWails = false

// Subscribe via EventsOn and return its cancel function — removing by event
// name would kill ALL listeners for the event, including App-level ones.
export function subscribeEvent(name, callback) {
  if (!hasWails()) {
    if (!warnedNoWails) {
      warnedNoWails = true
      console.warn('Wails runtime not found — event subscriptions disabled (plain browser dev mode?)')
    }
    return () => {}
  }
  return EventsOn(name, callback)
}

export function onLogEntry(callback) {
  return subscribeEvent('log:entry', callback)
}

export function onConnectionProgress(callback) {
  return subscribeEvent('conn:progress', callback)
}

export function onConnectionDone(callback) {
  return subscribeEvent('conn:done', callback)
}

export function onConnectionOpened(callback) {
  return subscribeEvent('conn:opened', callback)
}

export function onAIChunk(callback) {
  return subscribeEvent('ai:chunk', callback)
}

export function onAITool(callback) {
  return subscribeEvent('ai:tool', callback)
}

export function onAIError(callback) {
  return subscribeEvent('ai:error', callback)
}

export function onOrgEvent(callback) {
  return subscribeEvent('org:event', callback)
}

export function onAgentSession(callback) {
  return subscribeEvent('agent:session', callback)
}

// onChatEvent streams the new GUI chat supervisor's journal — one call per
// chat:event envelope ({version,profileId,conversationId,turnId,seq,at,type,
// payload}). useChatStream.js
// is the sole consumer; it filters by conversationId/turnId itself rather
// than this helper doing it, so multiple independent subscribers (this
// panel's live turn view, a future activity/detail view) never fight over
// one disposer (see the EventsOff footgun this file's onLogEntry-style
// helpers already avoid).
export function onChatEvent(callback) {
  return subscribeEvent('chat:event', callback)
}

// parseRosterJSON parses a roster binding's reply; unparseable text becomes
// an {error} object.
function parseRosterJSON(s) {
  try { return JSON.parse(s) } catch { return { error: String(s || 'empty reply') } }
}

// onAgentValidate streams `agent validate --json` progress lines.
export function onAgentValidate(callback) {
  return subscribeEvent('agents:validate', callback)
}

export function onAgentValidateClosed(callback) {
  return subscribeEvent('agents:validateClosed', callback)
}

// newOrgEventsStreamId returns an id for api.streamOrgEvents that is unique
// across page reloads too (the Go side outlives them).
let orgEventsStreamSeq = 0
export function newOrgEventsStreamId() {
  orgEventsStreamSeq += 1
  return `${Date.now().toString(36)}-${orgEventsStreamSeq}`
}

export function onOrgEventsClosed(callback) {
  return subscribeEvent('org:eventsClosed', callback)
}

// onOrgRunStatus fires when RunOrg's tracked subprocess starts/exits.
// Payload: {orgName, status: 'running' | 'stopped' | 'error'}.
export function onOrgRunStatus(callback) {
  return subscribeEvent('org:runStatus', callback)
}

// onOrgDesignUpdated fires for ANY change to an org's config file —
// an in-app canvas save, an AI chat tool call, or an external
// `monoagentcli org`/hand edit — via a single event name regardless of
// origin (payload.origin is "ui" | "external", diagnostic only, never
// branch UI behavior on it). Payload:
// {v, orgName, profileID, origin, deleted, valid, errors, org}.
export function onOrgDesignUpdated(callback) {
  return subscribeEvent('org:designUpdated', callback)
}

// onMonomindInitEvent streams InitializeMonomindProfile's progress.
// Payload: {kind: 'line'|'error'|'done', message}.
export function onMonomindInitEvent(callback) {
  return subscribeEvent('monomind:initProgress', callback)
}

// onDocumentsChanged fires when the background document watcher discovers
// or updates files under the active profile's folder. Payload:
// {profileID, added}. Triggers a reload; the event carries no document
// data itself (mirrors onOrgDesignUpdated's payload-is-diagnostic-only
// discipline) -- callers always just re-fetch via listProfileDocuments.
export function onDocumentsChanged(callback) {
  return subscribeEvent('documents:changed', callback)
}

// onImagesChanged fires when the background image watcher discovers
// or updates files under the active profile's folder, or when images are added/deleted.
export function onImagesChanged(callback) {
  return subscribeEvent('images:changed', callback)
}

export const PLATFORMS = ['INSTAGRAM', 'LINKEDIN', 'X', 'TIKTOK']
export const STATES = ['PENDING', 'RUNNING', 'PAUSED', 'COMPLETED', 'FAILED', 'CANCELLED']

export const PLATFORM_COLORS = {
  INSTAGRAM: '#e1306c',
  LINKEDIN:  '#0077b5',
  X:         '#e7e9ea',
  TIKTOK:    '#ff0050',
  EMAIL:     '#6366f1',
  TELEGRAM:  '#26a5e4',
}

export const STATE_COLORS = {
  PENDING:   '#94a3b8',
  RUNNING:   '#00f5d4',
  PAUSED:    '#eab308',
  COMPLETED: '#10b981',
  FAILED:    '#ef4444',
  CANCELLED: '#6b7280',
}
