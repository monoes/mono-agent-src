// The org stage model (monoes/mono-agent#228): one coder turn's dynamic org
// as {nodes, edges, flights, feed, quests, scoreboard}, folded from the
// turn's journal by a pure stageReducer. The lead is always node "lead";
// the workers it spawns come from agent.* events (#226), and native
// subagents from their Task tool calls, or from the agent.* events the CLI
// journals for them when monomind reports their lifecycle (#230,
// monomind#387; their text then fills the drawer). Everything is keyed by the event,
// never by the clock, so replaying a recorded journal gives exactly the
// state the live stream built.
//
// The reducer only ever reads the event envelope ({seq, at, type,
// payload}); a running org (#229) can feed it the same shapes.

import { vaultRelated } from '../components/chat/toolLinks.js'
import { maskCredentials, maskSecretShapes } from '../components/chat/credentials.js'

export const LEAD_ID = 'lead'

// How many recent items the stage keeps; older ones fall off so a long
// turn doesn't grow the state (or the render) without bound.
export const MAX_FEED = 200
export const MAX_FLIGHTS = 24
const MAX_MESSAGES = 50
// A node keeps the order of its latest MAX_CALLS tool calls.
export const MAX_CALLS = 400
// ... and its latest MAX_PARTS timeline parts (text and tool calls).
export const MAX_PARTS = 800
// A text part longer than this is cut (the CLI already bounds a worker's
// text; this keeps a journal from elsewhere in check too).
export const MAX_PART_TEXT = 64 * 1024
const MAX_QUESTS = 100
const MAX_FILES = 200

// Statuses a node can be in (agent.status's "to", plus the lead's own).
export const RUNNING = new Set(['queued', 'starting', 'working', 'waiting_lease', 'waiting_user'])
export const FINISHED = new Set(['done', 'failed', 'cancelled'])

const CLAUDE_KINDS = {
  Bash: 'shell', Edit: 'edit', MultiEdit: 'edit', Write: 'write', Read: 'read',
  Glob: 'search', Grep: 'search', WebFetch: 'web', WebSearch: 'web',
  Task: 'task', Agent: 'task', TodoWrite: 'todo', NotebookEdit: 'edit',
}
const WRITE_KINDS = new Set(['edit', 'write', 'patch'])
// A shell command that runs tests (go test, npm test, pytest, vitest, …).
const TEST_COMMAND = /\b(go test|cargo test|pytest|vitest|jest|mocha|rspec|phpunit|unittest|(npm|pnpm|yarn|bun)( run)? test|make (test|check)|gradle(w)? test|mvn test|dotnet test|mix test)\b/

// toolKind is a call's normalized kind: the reported one, else Claude
// Code's tool name mapped (the same rule NativeToolCard uses).
export function toolKind(name, kind) {
  if (kind) return kind
  if (CLAUDE_KINDS[name]) return CLAUDE_KINDS[name]
  if ((name || '').startsWith('mcp__')) return 'mcp'
  if ((name || '').startsWith('org_')) return 'org'
  return 'other'
}

function parseArgs(args) {
  if (typeof args === 'string') {
    try { return JSON.parse(args) || {} } catch { return {} }
  }
  return args && typeof args === 'object' ? args : {}
}

function baseName(p) {
  const s = String(p || '')
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  return i >= 0 ? s.slice(i + 1) : s
}

function clip(s, n) {
  const t = String(s ?? '').replace(/\s+/g, ' ').trim()
  return t.length > n ? t.slice(0, n - 1) + '…' : t
}

function filePathOf(args) {
  return args.file_path || args.path || args.notebook_path || ''
}

function commandOf(args) {
  return Array.isArray(args.command) ? args.command.join(' ') : String(args.command ?? '')
}

// doingOf is what a tool call says the agent is doing: its kind and a short
// target ("chat.go", "go test ./...", "example.com"). The stage words it
// per kind ("editing chat.go", "running go test", "browsing example.com").
// A shell command that may touch the vault says only that (vault: true),
// and a credential in any target is masked (#872 review).
export function doingOf(name, kind, rawArgs) {
  const k = toolKind(name, kind)
  const args = parseArgs(rawArgs)
  let target = ''
  switch (k) {
    case 'edit': case 'write': case 'read': case 'patch':
      target = baseName(filePathOf(args)) || (Array.isArray(args.files) ? args.files.map(baseName).join(', ') : '')
      break
    case 'shell':
      target = commandOf(args)
      if (vaultRelated(target)) return { kind: k, name: name || '', target: '', vault: true }
      break
    case 'web':
      try { target = args.url ? new URL(args.url).host : (args.query || '') } catch { target = args.url || '' }
      break
    case 'search':
      target = args.pattern || args.query || ''
      break
    case 'task':
      target = args.description || args.subagent_type || ''
      break
    default:
      target = ''
  }
  return { kind: k, name: name || '', target: clip(maskCredentials(target), 48) }
}

function newNode(id, patch = {}) {
  return {
    id,
    parentId: id === LEAD_ID ? null : LEAD_ID,
    role: '', agentType: '', native: false, skills: [], runtime: '', model: '', effort: '', access: '', branch: '',
    brief: '', why: '', pickConfidence: null, jevConfidence: null,
    // veteran: a worker of an earlier turn, loaded idle for the lead to
    // message (#230); the stage greys it out until it runs.
    veteran: false,
    status: id === LEAD_ID ? 'idle' : 'queued', statusDetail: '', statusAt: null, leases: [],
    prevModel: null, reassignedSeq: 0, reassignedAt: null,
    doing: null,
    tools: 0, toolsDone: 0, files: [], testsRun: 0, testsPassed: 0,
    tokensIn: null, tokensOut: null, costUsd: null, costEstimated: false,
    needsYou: false, summary: '', outcome: '', durationMs: 0, limited: false, fidelity: '',
    spawnSeq: 0, spawnAt: null, spawnedSeen: false,
    // reported: a native subagent whose lifecycle the runner reports
    // (agent.* events), so its Task call no longer decides its status.
    reported: false,
    // callOrder is the agent's own tool calls, in order; their content is
    // the turn's (chatReducer's agentCalls), never copied here. parts is
    // its timeline in chatReducer's shape: its text ({kind:'text', partId,
    // text}) and tool calls ({kind:'tool', callId}).
    callOrder: [], parts: [], messages: [],
    ...patch,
  }
}

export function initialStage() {
  return {
    nodes: { [LEAD_ID]: newNode(LEAD_ID, { role: '' }) },
    order: [LEAD_ID],
    edges: [],
    flights: [],
    feed: [],
    quests: [],
    scoreboard: null,
    // callId -> the native subagent that made it, and a Task call's id ->
    // the native subagent node it started; tests: callIds of test commands.
    callOwner: {},
    nativeByCall: {},
    testCalls: {},
    startedAt: null,
    finishedAt: null,
    turnStatus: '',
    leadUsage: null,
    lastSeq: 0,
  }
}

const RELEVANT = new Set([
  'assistant.delta', 'turn.started', 'turn.finished', 'session.bound', 'tool.started', 'tool.completed', 'usage.updated',
  'agent.spawned', 'agent.status', 'agent.message', 'agent.reassigned', 'agent.finished',
])

function pushCapped(list, item, max) {
  const next = list.length >= max ? list.slice(list.length - max + 1) : list.slice()
  next.push(item)
  return next
}

// Draft is a copy-on-write view of one stage: only the parts an event
// touches are copied, so a big journal stays cheap to fold.
function draft(s) {
  const d = { ...s }
  const touched = new Set()
  d.node = (id) => {
    if (!touched.has(id)) {
      if (d.nodes === s.nodes) d.nodes = { ...s.nodes }
      d.nodes[id] = d.nodes[id] ? { ...d.nodes[id] } : newNode(id)
      if (!s.nodes[id]) d.order = [...d.order, id]
      touched.add(id)
    }
    return d.nodes[id]
  }
  d.has = (id) => !!(d.nodes[id])
  return d
}

function finishDraft(d) {
  delete d.node
  delete d.has
  return d
}

function addEdge(d, parentId, childId) {
  if (d.edges.some(e => e.to === childId)) return
  d.edges = [...d.edges, { id: `${parentId}->${childId}`, from: parentId, to: childId }]
}

function addFeed(d, ev, agentId, type, text = '', detail = '') {
  d.feed = pushCapped(d.feed, { seq: ev.seq ?? 0, at: ev.at || null, agentId, type, text: clip(text, 240), detail }, MAX_FEED)
}

function addFlight(d, ev, kind, from, to, text = '') {
  d.flights = pushCapped(d.flights, { id: `f${ev.seq ?? d.flights.length}-${kind}-${from}-${to}`, seq: ev.seq ?? 0, at: ev.at || null, kind, from, to, text: clip(text, 280) }, MAX_FLIGHTS)
}

function questStatus(status) {
  if (status === 'done') return 'done'
  if (status === 'failed') return 'failed'
  if (status === 'cancelled') return 'cancelled'
  if (status === 'starting' || status === 'working') return 'active'
  return 'queued'
}

function addQuest(d, ev, agentId, text, kind = 'brief') {
  const status = questStatus(d.nodes[agentId]?.status)
  d.quests = pushCapped(d.quests, { id: `q${ev.seq ?? d.quests.length}-${agentId}`, agentId, text: clip(text, 280), kind, status, seq: ev.seq ?? 0 }, MAX_QUESTS)
}

// syncQuest moves an agent's latest quest to its status.
function syncQuest(d, agentId) {
  const status = questStatus(d.nodes[agentId]?.status)
  for (let i = d.quests.length - 1; i >= 0; i--) {
    if (d.quests[i].agentId !== agentId) continue
    if (d.quests[i].status !== status) {
      d.quests = d.quests.slice()
      d.quests[i] = { ...d.quests[i], status }
    }
    return
  }
}

function setStatus(d, ev, id, to, detail = '', leases = []) {
  const n = d.node(id)
  // A question waits for an answer until the agent moves on.
  if (n.status !== to) n.needsYou = false
  n.status = to
  n.statusDetail = detail || ''
  n.leases = Array.isArray(leases) ? leases : []
  n.statusAt = ev.at || n.statusAt
  syncQuest(d, id)
}

function addFiles(n, paths) {
  if (!paths?.length) return
  const set = new Set(n.files)
  for (const p of paths) if (p && set.size < MAX_FILES) set.add(p)
  if (set.size !== n.files.length) n.files = [...set].sort()
}

// ownerOf is the node a tool event belongs to: a call made inside a native
// subagent belongs to that subagent, else to its worker, else to the lead.
// d is a stage (or its draft); the running-org adapter files calls by it.
export function ownerOf(d, p) {
  if (p.parentCallId && d.nativeByCall[p.parentCallId]) return d.nativeByCall[p.parentCallId]
  if (d.callOwner[p.callId]) return d.callOwner[p.callId]
  return p.agentId || LEAD_ID
}

function onToolStarted(d, ev, p) {
  if (!p.callId) return
  const owner = ownerOf(d, p)
  const n = d.node(owner)
  const kind = toolKind(p.name, p.kind)
  const args = parseArgs(p.arguments)
  const doing = { ...doingOf(p.name, p.kind, args), callId: p.callId, active: true }
  n.doing = doing
  n.tools += 1
  if (WRITE_KINDS.has(kind)) addFiles(n, [filePathOf(args)])
  if (owner === LEAD_ID && n.status === 'idle') n.status = 'working'
  // Only calls made inside a native subagent need remembering: any other
  // call's completion names its owner (agentId, or none for the lead).
  if (owner !== (p.agentId || LEAD_ID)) d.callOwner = { ...d.callOwner, [p.callId]: owner }
  if (kind === 'shell' && TEST_COMMAND.test(commandOf(args))) {
    d.testCalls = { ...d.testCalls, [p.callId]: 'run' }
    n.testsRun += 1
  }
  if (owner !== LEAD_ID) {
    // Latest-N, for the call order and the timeline alike; a call that
    // falls out of the order leaves the timeline with it.
    const dropped = n.callOrder.length >= MAX_CALLS ? n.callOrder[0] : null
    n.callOrder = pushCapped(n.callOrder, p.callId, MAX_CALLS)
    const parts = dropped ? n.parts.filter(x => x.kind !== 'tool' || x.callId !== dropped) : n.parts
    n.parts = pushCapped(parts, { kind: 'tool', callId: p.callId }, MAX_PARTS)
  }
  // A native Task call starts a subagent: its own node under the agent
  // that called it. When the runner reports the subagent (#230), its
  // agent.spawned{agentType:"native"} for the same id follows and fills
  // the node in.
  if (kind === 'task' && !d.nativeByCall[p.callId]) {
    const id = `native:${p.callId}`
    const brief = args.prompt || args.description || ''
    const sub = d.node(id)
    Object.assign(sub, {
      parentId: owner, native: true, agentType: 'native', role: args.subagent_type || args.description || '',
      brief: clip(brief, 2000), status: 'working', spawnSeq: ev.seq ?? 0, spawnAt: ev.at || null,
      runtime: d.nodes[owner]?.runtime || '', model: d.nodes[owner]?.model || '', spawnedSeen: true,
    })
    d.nativeByCall = { ...d.nativeByCall, [p.callId]: id }
    addEdge(d, owner, id)
    addQuest(d, ev, id, brief || args.description || '')
    addFlight(d, ev, 'brief', owner, id, brief)
    addFeed(d, ev, id, 'spawned', sub.role, owner)
  }
}

function onToolCompleted(d, ev, p) {
  if (!p.callId) return
  const owner = ownerOf(d, p)
  const n = d.node(owner)
  n.toolsDone += 1
  if (n.doing?.callId === p.callId) n.doing = { ...n.doing, active: false }
  if (d.testCalls[p.callId] === 'run') {
    d.testCalls = { ...d.testCalls, [p.callId]: p.ok === false ? 'failed' : 'passed' }
    if (p.ok !== false) n.testsPassed += 1
  }
  const nativeId = d.nativeByCall[p.callId]
  // A reported subagent's own agent.finished already ended it; the Task
  // call's end only closes one it never reported finishing.
  if (nativeId && d.has(nativeId) && !(d.nodes[nativeId].reported && FINISHED.has(d.nodes[nativeId].status))) {
    const failed = p.ok === false || p.cancelled || p.denied
    const sub = d.node(nativeId)
    setStatus(d, ev, nativeId, p.cancelled ? 'cancelled' : failed ? 'failed' : 'done')
    sub.outcome = sub.status
    const result = maskSecretShapes(p.result)
    sub.summary = clip(result, 600)
    sub.limited = limitedOf(sub)
    if (sub.spawnAt && ev.at) sub.durationMs = Math.max(0, Date.parse(ev.at) - Date.parse(sub.spawnAt)) || 0
    if (result) addFlight(d, ev, 'result', nativeId, sub.parentId || LEAD_ID, result)
    addFeed(d, ev, nativeId, 'finished', sub.summary, sub.status)
  }
}

// limitedOf says whether an agent's runtime shows its work only partly:
// the fidelity the journal reports (#259), else, once it has finished,
// tool starts that never ended.
function limitedOf(n) {
  if (n.fidelity) return n.fidelity !== 'full'
  return FINISHED.has(n.status) && n.tools > 0 && n.toolsDone === 0
}

function capText(text) {
  return text.length > MAX_PART_TEXT ? text.slice(0, MAX_PART_TEXT) + '…' : text
}

// onText adds a worker's own text (assistant.delta with its agentId, #258)
// to its timeline: a delta for the part it is writing extends that part.
function onText(d, p) {
  if (!p.agentId || !p.text) return
  const n = d.node(p.agentId)
  const last = n.parts[n.parts.length - 1]
  if (last && last.kind === 'text' && last.partId === p.partId) {
    if (last.text.length >= MAX_PART_TEXT) return
    n.parts = [...n.parts.slice(0, -1), { ...last, text: capText(last.text + p.text) }]
  } else {
    n.parts = pushCapped(n.parts, { kind: 'text', partId: p.partId || `t${n.parts.length}`, text: capText(p.text) }, MAX_PARTS)
  }
}

function onSpawned(d, ev, p) {
  const id = p.agentId
  // A native subagent's node from its Task call already sits under its caller.
  const keepParent = p.agentType === 'native' && d.nodes[id]?.native
  const n = d.node(id)
  Object.assign(n, {
    parentId: keepParent ? n.parentId : p.parentId || LEAD_ID,
    role: p.role || n.role, agentType: p.agentType || n.agentType, native: p.agentType === 'native',
    skills: Array.isArray(p.skills) ? p.skills : n.skills,
    runtime: p.runtime || n.runtime, model: p.model ?? n.model, effort: p.effort || n.effort, access: p.access || n.access,
    brief: p.brief || n.brief, why: p.why || n.why,
    pickConfidence: p.pickConfidence ?? n.pickConfidence, jevConfidence: p.jevConfidence ?? n.jevConfidence,
    spawnSeq: n.spawnSeq || ev.seq || 0, spawnAt: n.spawnAt || ev.at || null,
    fidelity: p.fidelity || n.fidelity,
    // An isolated writer's own git branch (#230).
    branch: p.branch || n.branch,
  })
  if (n.native) {
    n.reported = true
    // It runs inside its caller's exec, on its caller's model.
    const caller = d.nodes[n.parentId]
    if (!n.runtime && caller) n.runtime = caller.runtime
    if (!n.model && caller) n.model = caller.model
  }
  n.limited = limitedOf(n)
  if (p.veteran) n.veteran = true
  addEdge(d, n.parentId, id)
  if (!n.spawnedSeen) {
    n.spawnedSeen = true
    addFeed(d, ev, id, n.veteran ? 'veteran' : 'spawned', n.role, [n.runtime, n.model].filter(Boolean).join('/'))
  }
}

function onMessage(d, ev, p) {
  const id = p.agentId
  const n = d.node(id)
  if (!d.edges.some(e => e.to === id)) addEdge(d, n.parentId || LEAD_ID, id)
  const dir = p.direction
  n.messages = pushCapped(n.messages, { seq: ev.seq ?? 0, at: ev.at || null, direction: dir, from: p.from || '', to: p.to || '', text: p.text || '', truncated: !!p.truncated }, MAX_MESSAGES)
  const parent = n.parentId || LEAD_ID
  switch (dir) {
    case 'brief':
      if (!n.brief) n.brief = p.text || ''
      if (!d.quests.some(q => q.agentId === id)) addQuest(d, ev, id, p.text)
      addFlight(d, ev, 'brief', parent, id, p.text)
      addFeed(d, ev, id, 'brief', p.text)
      break
    case 'followup':
      addQuest(d, ev, id, p.text, 'followup')
      addFlight(d, ev, 'followup', parent, id, p.text)
      addFeed(d, ev, id, 'followup', p.text)
      break
    case 'result':
      n.summary = n.summary || clip(p.text, 600)
      addFlight(d, ev, 'result', id, parent, p.text)
      addFeed(d, ev, id, 'result', p.text)
      break
    case 'question':
      n.needsYou = true
      addFlight(d, ev, 'question', id, parent, p.text)
      addFeed(d, ev, id, 'question', p.text)
      break
    default:
      addFeed(d, ev, id, 'message', p.text, dir || '')
      // A running org's role-to-role message (#229) names both ends.
      if (p.flightFrom && p.flightTo && p.flightFrom !== p.flightTo && d.nodes[p.flightFrom] && d.nodes[p.flightTo]) {
        addFlight(d, ev, 'brief', p.flightFrom, p.flightTo, p.text)
      }
  }
}

function num(v) {
  return typeof v === 'number' && isFinite(v) ? v : null
}

function stageApply(d, ev) {
  const p = ev.payload || {}
  switch (ev.type) {
    case 'turn.started':
      d.startedAt = ev.at || d.startedAt
      d.turnStatus = 'running'
      if (d.nodes[LEAD_ID].status === 'idle') setStatus(d, ev, LEAD_ID, 'working')
      break
    case 'session.bound':
      if (p.runtime) d.node(LEAD_ID).runtime = p.runtime
      break
    case 'assistant.delta':
      onText(d, p)
      break
    case 'tool.started':
      onToolStarted(d, ev, p)
      break
    case 'tool.completed':
      onToolCompleted(d, ev, p)
      break
    case 'usage.updated': {
      const usage = { tokensIn: num(p.inputTokens), tokensOut: num(p.outputTokens), costUsd: num(p.costUsd) }
      if (p.agentId && p.agentId !== LEAD_ID) {
        // A worker's cost is estimated from its tokens when its runtime
        // reports none (#230): the stage shows it with "≈".
        Object.assign(d.node(p.agentId), usage, { costEstimated: !!p.costEstimated })
      } else {
        d.leadUsage = usage
        Object.assign(d.node(LEAD_ID), usage)
      }
      break
    }
    case 'agent.spawned':
      if (p.agentId) onSpawned(d, ev, p)
      break
    case 'agent.status':
      if (!p.agentId || !p.to) break
      if (p.branch) d.node(p.agentId).branch = p.branch
      // Per run and absent from other agents: kept once reported.
      if (p.confinement) d.node(p.agentId).confinement = p.confinement
      // A repeat of the same status only updates the leases it holds.
      if (d.nodes[p.agentId]?.status === p.to) {
        setStatus(d, ev, p.agentId, p.to, p.detail, p.leases)
        break
      }
      setStatus(d, ev, p.agentId, p.to, p.detail, p.leases)
      if (p.to === 'waiting_lease' || p.to === 'failed' || p.to === 'cancelled') addFeed(d, ev, p.agentId, 'status', p.to, p.detail || '')
      break
    case 'agent.message':
      if (p.agentId) onMessage(d, ev, p)
      break
    case 'agent.reassigned': {
      if (!p.agentId) break
      const n = d.node(p.agentId)
      n.prevModel = { runtime: p.fromRuntime || n.runtime, model: p.fromModel || '', reason: p.reason || '' }
      n.runtime = p.toRuntime || n.runtime
      n.model = p.toModel ?? n.model
      n.reassignedSeq = ev.seq ?? 0
      n.reassignedAt = ev.at || null
      if (p.fidelity) n.fidelity = p.fidelity
      n.limited = limitedOf(n)
      addFeed(d, ev, p.agentId, 'reassigned', p.reason || '', `${n.prevModel.runtime}/${n.prevModel.model || 'default'} → ${n.runtime}/${n.model || 'default'}`)
      break
    }
    case 'agent.finished': {
      if (!p.agentId) break
      const outcome = p.outcome || 'done'
      setStatus(d, ev, p.agentId, outcome)
      const n = d.node(p.agentId)
      n.outcome = outcome
      if (p.summary) n.summary = clip(p.summary, 600)
      if (num(p.inputTokens) != null) n.tokensIn = p.inputTokens
      if (num(p.outputTokens) != null) n.tokensOut = p.outputTokens
      if (num(p.costUsd) != null) n.costUsd = p.costUsd
      n.costEstimated = !!p.costEstimated
      n.durationMs = num(p.durationMs) || 0
      n.needsYou = false
      if (n.doing) n.doing = { ...n.doing, active: false }
      addFiles(n, p.filesChanged)
      // A runtime that reports tool starts but never their ends shows its
      // activity only partly: the stage says so.
      n.limited = limitedOf(n)
      addFeed(d, ev, p.agentId, 'finished', n.summary, outcome)
      break
    }
    case 'turn.finished': {
      d.finishedAt = ev.at || null
      d.turnStatus = p.status || 'completed'
      const lead = d.node(LEAD_ID)
      lead.status = p.status === 'completed' || !p.status ? 'done' : p.status === 'cancelled' ? 'cancelled' : 'failed'
      if (lead.doing) lead.doing = { ...lead.doing, active: false }
      d.scoreboard = buildScoreboard(d)
      break
    }
    default:
  }
}

// buildScoreboard is the end-of-turn card: agents, time, cost (with "≈"
// when any of it is estimated), files changed, tests run and passed.
export function buildScoreboard(s) {
  // Veterans that stayed idle did nothing this turn.
  const nodes = s.order.map(id => s.nodes[id]).filter(n => !isIdleVeteran(n))
  const agents = nodes.filter(n => n.id !== LEAD_ID)
  const files = new Set()
  let cost = 0
  let hasCost = false
  let estimated = false
  let tools = 0
  let testsRun = 0
  let testsPassed = 0
  for (const n of nodes) {
    n.files.forEach(f => files.add(f))
    tools += n.tools
    testsRun += n.testsRun
    testsPassed += n.testsPassed
    // Native subagents run inside their caller's exec: its cost has them.
    if (!n.native && n.costUsd != null) { cost += n.costUsd; hasCost = true }
    if (n.costEstimated) estimated = true
  }
  const ms = s.startedAt && s.finishedAt ? Math.max(0, Date.parse(s.finishedAt) - Date.parse(s.startedAt)) || 0 : 0
  return {
    agents: agents.length,
    workers: agents.filter(n => !n.native).length,
    natives: agents.filter(n => n.native).length,
    done: agents.filter(n => n.status === 'done').length,
    failed: agents.filter(n => n.status === 'failed').length,
    durationMs: ms,
    costUsd: hasCost ? Math.round(cost * 1e6) / 1e6 : null,
    costEstimated: estimated,
    filesChanged: [...files].sort(),
    tools,
    testsRun,
    testsPassed,
    status: s.turnStatus,
  }
}

// stageReducer folds one journal event into the stage. It is pure: the
// same events in the same order always give the same state. Events it has
// no use for return the state untouched (null stays null until the first
// one that matters); an event at or below the last
// applied seq (a re-delivery) is ignored.
export function stageReducer(state, ev) {
  if (!ev || !RELEVANT.has(ev.type)) return state
  // The lead's own text is the chat's, not the stage's.
  if (ev.type === 'assistant.delta' && !ev.payload?.agentId) return state
  const s = state || initialStage()
  if (typeof ev.seq === 'number' && ev.seq <= s.lastSeq) return s
  const d = draft(s)
  stageApply(d, ev)
  if (typeof ev.seq === 'number') d.lastSeq = ev.seq
  return finishDraft(d)
}

// replayStage builds the stage from a recorded journal: sorted by seq and
// de-duplicated, the way the live stream delivers it.
export function replayStage(events) {
  const sorted = (events || []).filter(Boolean).slice().sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0))
  return sorted.reduce(stageReducer, initialStage())
}

// ── Selectors ────────────────────────────────────────────────────────────

// isIdleVeteran: a worker of an earlier turn the lead hasn't messaged this
// turn; the stage greys it out.
export function isIdleVeteran(n) {
  return !!n?.veteran && n.status === 'idle'
}

// hasTeam reports whether the lead brought anyone in this turn.
export function hasTeam(stage) {
  return !!stage && stage.order.length > 1
}

// leasesOf says who holds the pen (the write lease) and the browser, and
// who waits for which, as the conductor reports them on agent.status
// (leases and a waiting_lease detail): the stage never re-derives them.
export function leasesOf(stage) {
  const out = { pen: null, browser: null, waiting: [] }
  if (!stage) return out
  for (const id of stage.order) {
    const n = stage.nodes[id]
    if (n.status === 'waiting_lease') out.waiting.push({ id, lease: n.statusDetail || 'write' })
    if (n.leases.includes('write') && !out.pen) out.pen = id
    if (n.leases.includes('browser') && !out.browser) out.browser = id
  }
  return out
}

// questProgress is the turn's quest bar: how many briefs are finished.
export function questProgress(stage) {
  const qs = stage?.quests || []
  const total = qs.length
  const done = qs.filter(q => q.status === 'done' || q.status === 'failed' || q.status === 'cancelled').length
  return { done, total, ratio: total ? done / total : 0 }
}

// structureKey changes only when nodes are added or re-parented, so the
// layout is recomputed then and not on every status change.
export function structureKey(stage) {
  if (!stage) return ''
  return stage.order.map(id => `${id}<${stage.nodes[id].parentId || ''}`).join('|')
}
