// Runs one starter for real, the way the workflow editor does: the template
// becomes a workflow (CreateWorkflowFromTemplate), the person's click switches
// it on (SetWorkflowActive, the editor's "Activate & run"), RunWorkflow starts
// the CLI, workflow:exec-started names the execution, GetExecutionDetail is
// polled for each node, and workflow:complete ends it. Nothing here guesses
// progress: every state comes from a binding answer or an event.
import { useCallback, useEffect, useRef, useState } from 'react'
import * as GoApp from '../../wailsjs/go/main/App'
import { subscribeEvent } from '../../services/api.js'
import { POLL_FAIL_LIMIT, nextPollFailures } from '../../lib/runPoll.js'
import { callStep } from '../callStep.js'
import { orderNodes, nodeStates, resultFields, runError, isSuccess, isPartial, isLive, needsSiteApproval, agentNotSetup } from '../starters.js'

export const POLL_MS = 1500
export const START_TIMEOUT_MS = 60000

// templateId -> workflow id, for the app session: "Try again", and coming back
// to the step, run the same copy instead of making another.
const workflowIds = new Map()
export function forgetStarterWorkflows() { workflowIds.clear() } // tests

// run: null, or { templateId, phase: 'preparing'|'starting'|'running'|'done'|'failed',
//   nodes: [{ id, name, state }], result: [{ key, value }], error, reason: ''|'approval'|'agent'|'notStarted'|'lost'|'noText'|'partial', loginRequired }
export function useStarterRun({ onSuccess } = {}) {
  const [run, setRun] = useState(null)
  const token = useRef(0)
  const live = useRef(null) // { offs, timer, interval }
  const successRef = useRef(onSuccess)
  successRef.current = onSuccess

  const stop = useCallback(() => {
    const l = live.current
    live.current = null
    if (!l) return
    l.offs.forEach(off => { try { off() } catch { /* already gone */ } })
    clearTimeout(l.timer)
    clearInterval(l.interval)
  }, [])

  useEffect(() => () => { token.current++; stop() }, [stop])

  const start = useCallback(async (starter) => {
    stop()
    const my = ++token.current
    const alive = () => token.current === my
    const templateId = starter.templateId
    let nodes = []
    const update = (patch) => { if (alive()) setRun(r => ({ ...r, ...patch })) }
    const fail = (patch) => {
      if (!alive()) return
      stop()
      update({ phase: 'failed', ...patch })
    }
    const refused = (r) => fail({ error: r.message, loginRequired: !!r.loginRequired, reason: '' })

    setRun({ templateId, phase: 'preparing', nodes: [], result: [], error: '', reason: '', loginRequired: false })

    const create = async () => {
      const made = await callStep(() => GoApp.CreateWorkflowFromTemplate(templateId))
      if (!alive()) return null
      if (!made.ok) { refused(made); return null }
      if (!made.value?.id) { fail({ error: '', reason: 'noText' }); return null }
      workflowIds.set(templateId, made.value.id)
      return made.value.id
    }
    let id = workflowIds.get(templateId)
    const reused = !!id
    if (!id && !(id = await create())) return
    let wf = await callStep(() => GoApp.GetWorkflow(id))
    if (!alive()) return
    if (!wf.ok && reused && !wf.loginRequired) {
      // the copy made earlier is gone (deleted meanwhile): make a new one
      workflowIds.delete(templateId)
      if (!(id = await create())) return
      wf = await callStep(() => GoApp.GetWorkflow(id))
      if (!alive()) return
    }
    if (!wf.ok) return refused(wf)
    nodes = orderNodes(wf.value?.nodes)
    update({ nodes: nodeStates(nodes, null) })

    const on = await callStep(() => GoApp.SetWorkflowActive(id, true))
    if (!alive()) return
    if (!on.ok) return refused(on)

    // Listen before starting, so no event is missed.
    let execId = null
    let completion = null
    let failures = 0
    let chain = Promise.resolve()
    let queued = 0
    const l = { offs: [], timer: null, interval: null }
    live.current = l
    // A poll queued before the run ended (stop) does nothing: no second
    // success, no late "lost" over a finished run.
    const current = () => alive() && live.current === l

    const pollOnce = async (final) => {
      if (!current() || !execId) return
      // Wrapped: a failed execution's detail carries its own "error" field,
      // which callStep would read as the call failing.
      const got = await callStep(async () => ({ detail: await GoApp.GetExecutionDetail(execId) }))
      if (!current()) return
      const detail = got.ok ? got.value.detail : null
      failures = nextPollFailures(failures, detail)
      if (!detail) {
        if (failures >= POLL_FAIL_LIMIT || final) fail({ error: got.ok ? '' : got.message, reason: 'lost' })
        return
      }
      const states = nodeStates(nodes, detail)
      if (isLive(detail) && !final) { update({ phase: 'running', nodes: states }); return }
      stop()
      if (isSuccess(detail)) {
        update({ phase: 'done', nodes: states, result: resultFields(starter, nodes, detail) })
        successRef.current?.()
        return
      }
      const error = runError(detail, completion)
      const waiting = states.some(n => n.state === 'waiting')
      let reason = ''
      if (needsSiteApproval(error) || waiting) reason = 'approval'
      else if (agentNotSetup(completion)) reason = 'agent'
      else if (isPartial(detail)) reason = 'partial'
      else if (!error) reason = 'noText'
      update({ phase: 'failed', nodes: states, error, reason })
    }
    const poll = (final) => {
      queued++
      chain = chain.then(() => pollOnce(final)).finally(() => { queued-- })
      return chain
    }

    l.offs.push(subscribeEvent('workflow:exec-started', (data) => {
      if (!alive() || data?.workflow_id !== id || execId || !data?.execution_id) return
      execId = data.execution_id
      clearTimeout(l.timer)
      update({ phase: 'running' })
      poll(false)
      l.interval = setInterval(() => { if (queued === 0) poll(false) }, POLL_MS) // a tick never piles up behind a slow poll
    }))
    l.offs.push(subscribeEvent('workflow:complete', (data) => {
      if (!alive() || data?.workflow_id !== id) return
      completion = data
      if (!execId) {
        fail({ error: String(data?.error || ''), reason: agentNotSetup(data) ? 'agent' : 'notStarted' })
        return
      }
      poll(true)
    }))
    l.timer = setTimeout(() => { if (!execId) fail({ error: '', reason: 'notStarted' }) }, START_TIMEOUT_MS)

    update({ phase: 'starting' })
    const started = await callStep(() => GoApp.RunWorkflow(id))
    if (!alive()) return
    if (!started.ok) refused(started)
  }, [stop])

  const busy = !!run && (run.phase === 'preparing' || run.phase === 'starting' || run.phase === 'running')
  return { run, start, busy }
}
