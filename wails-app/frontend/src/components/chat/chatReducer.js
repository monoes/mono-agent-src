// Pure reducer over one turn's chat:event stream (see
// docs/mastermind/plans/2026-09-11-interactive-agent-chat.md "Event
// contract"). No side effects, no subscriptions — useChatStream.js owns
// hydration/live merge/gap catch-up and dispatches events here in
// ascending-sequence order. Kept pure so it is directly unit-testable
// without mocking Wails.
import { stageReducer } from '../../lib/orgStage.js'

export function initialChatState() {
  return {
    scope: null,      // { conversationId, turnId } this state belongs to, or null
    parts: [],         // ordered [{ kind: 'text', partId, text } | { kind: 'tool', callId }]
    calls: {},          // callId -> { callId, name, arguments, status: 'started'|'completed', ok, result }
    notices: [],         // [{ code, message, severity }], append-only
    usage: null,          // latest { inputTokens, outputTokens, costUsd, source } snapshot, never summed
    session: null,          // { runtime, sessionId } once session.bound fires
    terminal: null,          // { status, reason, code, exitCode, historySaved } once turn.finished fires
    sandbox: null,           // the CLI's monomind.SandboxStatus* verdict ('sandboxed', 'scoped', …); null = none asked for
    stage: null,             // the org stage (#228, lib/orgStage.js); null until an event it uses
    missions: [],            // mission.propose / mission.plan / mission.report events: [{type, payload, at}]
    agents: {},              // dynamic org (#226): agentId -> { role, runtime, model, access, status, brief, report, tools, lastTool, … }
    agentCalls: {},          // their tool calls, callId -> call (the org stage's drawer shows them)
    startedAt: null,          // turn.started's "at", for local elapsed-time display
    lastEventAt: null,          // "at" of the most recently applied event, any type — drives "no new activity for Ns"
    lastSeq: 0,
  }
}

function upsertTextPart(parts, partId, text) {
  const idx = parts.findIndex(p => p.kind === 'text' && p.partId === partId)
  if (idx === -1) return [...parts, { kind: 'text', partId, text }]
  const next = parts.slice()
  next[idx] = { ...next[idx], text: next[idx].text + text }
  return next
}

// The optional tool.completed fields a coder turn adds, kept only when set.
function completionFlags(payload) {
  const flags = {}
  if (payload.truncated) flags.truncated = true
  if (payload.denied) flags.denied = true
  if (payload.cancelled) flags.cancelled = true
  if (typeof payload.durationMs === 'number' && payload.durationMs > 0) flags.durationMs = payload.durationMs
  // A shell call's exit code, when known (exit_code from runners that
  // pass monomind's field name through).
  const exitCode = typeof payload.exitCode === 'number' ? payload.exitCode : payload.exit_code
  if (typeof exitCode === 'number') flags.exitCode = exitCode
  return flags
}

// startedCall is a tool call as tool.started opens it.
function startedCall(payload, at) {
  return {
    callId: payload.callId,
    name: payload.name,
    arguments: payload.arguments ?? null,
    status: 'started',
    ok: null,
    result: null,
    startedAt: at,
    // Coder turns (#202): native marks one of the coding agent's own tools
    // (Bash, Edit, …); parentCallId nests a call made inside a
    // subagent (Task/Agent) call. Only set when present, so other
    // calls keep their exact shape.
    ...(payload.native ? { native: true } : {}),
    ...(payload.parentCallId ? { parentCallId: payload.parentCallId } : {}),
    // The normalized tool kind (shell, edit, patch, mcp, …) every
    // coder runtime reports; NativeToolCard renders by it.
    ...(payload.kind ? { kind: payload.kind } : {}),
    // Write/Edit: whether file_path existed when the call started
    // ("new file" vs "overwrite"); absent when unknown.
    ...(typeof payload.fileExisted === 'boolean' ? { fileExisted: payload.fileExisted } : {}),
  }
}

// completedCall is a call as tool.completed closes it; existing is its
// started call, if one was seen.
function completedCall(existing, payload, at) {
  const flags = completionFlags(payload)
  // No startedAt/finishedAt on the unmatched-completion fallback: a
  // completion with no observed start has no duration to show, so leaving
  // both undefined (rather than fabricating finishedAt alone) keeps "both
  // timestamps present" the one signal ToolActivityCard needs to decide
  // whether elapsed time can be shown at all.
  return existing
    ? { ...existing, status: 'completed', ok: payload.ok, result: payload.result, finishedAt: at, ...flags }
    : { callId: payload.callId, name: 'unknown', arguments: null, status: 'completed', ok: payload.ok, result: payload.result, ...flags }
}

// agentPatch folds a dynamic-org worker's events (#226) into state.agents.
// A worker's own tool calls carry agentId and stay out of the lead's
// timeline; the worker shows as one row (a part of kind 'agent'). Its calls
// are kept once, in state.agentCalls, for the org stage's drawer (#228).
function agentPatch(state, ev) {
  const p = ev.payload || {}
  const id = p.agentId
  // The lead's lease reports (agent.status for "lead") are the stage's.
  if (id === 'lead') return {}
  const agents = state.agents || {}
  const cur = agents[id] || { agentId: id, tools: 0, status: 'queued' }
  const put = (next) => ({ agents: { ...agents, [id]: { ...cur, ...next } } })
  switch (ev.type) {
    case 'agent.spawned':
      // A native subagent (#230) already shows as its Task call's card: no
      // row of its own in the timeline, only the stage's node.
      if (p.agentType === 'native') return put({ role: p.role, agentType: p.agentType, brief: p.brief, startedAt: ev.at })
      return {
        ...put({ role: p.role, agentType: p.agentType, runtime: p.runtime, model: p.model, effort: p.effort, access: p.access, skills: p.skills || [], brief: p.brief, why: p.why, startedAt: ev.at }),
        parts: state.parts.some(x => x.kind === 'agent' && x.agentId === id) ? state.parts : [...state.parts, { kind: 'agent', agentId: id }],
      }
    case 'agent.status':
      return put({ status: p.to, statusDetail: p.detail || '' })
    case 'agent.reassigned':
      return put({ runtime: p.toRuntime, model: p.toModel, reassigned: `${p.fromRuntime}/${p.fromModel || 'default'}: ${p.reason}` })
    case 'agent.message':
      if (p.direction === 'result') return put({ report: p.text })
      // A worker's question for the user (#256), open until answered.
      if (p.direction === 'question') return put({ question: { id: p.questionId, text: p.text } })
      // Closed by the user's answer, or by the system (timed out, stopped).
      if (p.direction === 'followup' && (p.from === 'user' || p.from === 'system') && cur.question?.id === p.questionId) return put({ question: null })
      return {}
    case 'agent.finished':
      return put({ status: p.outcome, summary: p.summary, costUsd: p.costUsd ?? null, costEstimated: !!p.costEstimated, filesChanged: p.filesChanged || [], durationMs: p.durationMs || 0 })
    case 'tool.started':
      return { ...put({ tools: cur.tools + 1, lastTool: p.name }), agentCalls: { ...state.agentCalls, [p.callId]: { ...startedCall(p, ev.at), agentId: id } } }
    case 'tool.completed':
      return { agentCalls: { ...state.agentCalls, [p.callId]: { ...completedCall(state.agentCalls?.[p.callId], p, ev.at), agentId: id } } }
    case 'usage.updated':
      return p.costUsd != null ? put({ costUsd: p.costUsd, costEstimated: !!p.costEstimated }) : {}
    default:
      return {}
  }
}

// A worker's tool calls, text (#258) and usage (#257) carry its agentId and
// never land in the lead's timeline or usage.
const AGENT_EVENTS = new Set(['tool.started', 'tool.completed', 'assistant.delta', 'usage.updated'])

function eventPatch(state, ev) {
  const payload = ev.payload || {}
  if (payload.agentId && (ev.type.startsWith('agent.') || AGENT_EVENTS.has(ev.type))) {
    return agentPatch(state, ev)
  }
  switch (ev.type) {
    case 'turn.started':
      // A coder turn's attachments (copies in its folder) are kept for the
      // message's thumbnails.
      return payload.attachments?.length ? { startedAt: ev.at, attachments: payload.attachments } : { startedAt: ev.at }

    case 'session.bound':
      return { session: { runtime: payload.runtime, sessionId: payload.sessionId } }

    case 'assistant.delta':
      return { parts: upsertTextPart(state.parts, payload.partId, payload.text) }

    case 'tool.started': {
      const calls = { ...state.calls, [payload.callId]: startedCall(payload, ev.at) }
      return { calls, parts: [...state.parts, { kind: 'tool', callId: payload.callId }] }
    }

    case 'tool.completed': {
      const calls = { ...state.calls, [payload.callId]: completedCall(state.calls[payload.callId], payload, ev.at) }
      // Retain an unmatched result as its own step (plan: "retain unmatched
      // results") rather than silently dropping a completion whose start
      // was never seen (e.g. hydration started mid-turn).
      const hasStep = state.parts.some(p => p.kind === 'tool' && p.callId === payload.callId)
      const parts = hasStep ? state.parts : [...state.parts, { kind: 'tool', callId: payload.callId }]
      return { calls, parts }
    }

    case 'usage.updated':
      return {
        usage: {
          inputTokens: payload.inputTokens ?? null,
          outputTokens: payload.outputTokens ?? null,
          costUsd: payload.costUsd ?? null,
          source: payload.source,
        },
      }

    case 'notice': {
      // The CLI's verdict on the turn's sandbox: a badge, not a banner.
      if (payload.code === 'agent.sandbox') return { sandbox: payload.message || null }
      // Its words: the mode a restricted turn ran in, or why the fallback setting was ignored.
      if (payload.code === 'agent.sandbox.detail') return { sandboxDetail: payload.message || null }
      const notice = { code: payload.code, message: payload.message, severity: payload.severity }
      // A coder turn's leftover background processes: keep the pids and the
      // turn they belong to, which "Stop all" needs.
      if (payload.code === 'coder.background') {
        Object.assign(notice, {
          pids: Array.isArray(payload.pids) ? payload.pids : [],
          // [{pid, command}]: many are the folder's own setup daemons, not
          // the agent's work, so the banner names each one.
          processes: Array.isArray(payload.processes) ? payload.processes.map(p => ({ pid: p.pid, command: p.command || '' })) : [],
          conversationId: ev.conversationId,
          turnId: ev.turnId,
        })
      }
      return { notices: [...state.notices, notice] }
    }

    case 'mission.propose':
    case 'mission.plan':
    case 'mission.report':
      return { missions: [...(state.missions || []), { type: ev.type, payload, at: ev.at }] }

    case 'turn.finished':
      return {
        terminal: {
          status: payload.status,
          reason: payload.reason,
          exitCode: payload.exitCode ?? null,
          historySaved: !!payload.historySaved,
          // agent_not_setup when the AI agent is not installed or not
          // logged in (the chat panel then links to the AI agents page).
          ...(payload.code ? { code: payload.code } : {}),
        },
        ...(payload.sandbox ? { sandbox: payload.sandbox } : {}),
      }

    default:
      return {}
  }
}

function applyEvent(state, ev) {
  const lastSeq = typeof ev.seq === 'number' ? ev.seq : state.lastSeq
  const lastEventAt = ev.at || state.lastEventAt
  return { ...state, ...eventPatch(state, ev), stage: stageReducer(state.stage, ev), lastSeq, lastEventAt }
}

export function chatReducer(state, action) {
  switch (action.type) {
    case 'scope':
      return { ...initialChatState(), scope: action.scope }

    case 'reset':
      return initialChatState()

    case 'event': {
      const ev = action.event
      if (state.scope && (ev.conversationId !== state.scope.conversationId || ev.turnId !== state.scope.turnId)) {
        return state
      }
      if (typeof ev.seq === 'number' && ev.seq <= state.lastSeq) return state
      return applyEvent(state, ev)
    }

    // Synthesized client-side (useChatStream.js), never a wire event — has
    // no seq of its own, so it goes through a separate action type rather
    // than being squeezed through 'event's seq-ordering guard.
    case 'localNotice':
      return { ...state, notices: [...state.notices, action.notice] }

    default:
      return state
  }
}
