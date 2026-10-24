import { useEffect, useState } from 'react'
import { aitaskApi, taskIsDone } from '../SendToCoderApi.js'
import { TaskResultLine } from './TaskResultLine.jsx'
import { aitaskBudgetApi, money } from '../settings/aitaskBudgetApi.js'

const POLL_MS = 4000
const BUDGET_POLL_MS = 15000

// TaskHeaderCard sits at the top of a coder bubble that is an AI task (#957):
// what was asked, its status (kept fresh while it runs), a way back to the tab
// it came from, and Cancel. task = { taskId, goal, status, origin: { page, label } }.
export function TaskHeaderCard({ task, onNavigate }) {
  const [status, setStatus] = useState(task.status || '')
  const [error, setError] = useState('')
  const [result, setResult] = useState(null) // { kind, data } once the task completed
  const [cancelling, setCancelling] = useState(false)
  const [budget, setBudget] = useState(null) // what the task has spent against its cap; absent when the CLI can't say
  // A different task on the same instance starts from nothing: none of the
  // previous task's status, result or error is shown for it.
  const [shownTask, setShownTask] = useState(task.taskId)
  if (shownTask !== task.taskId) {
    setShownTask(task.taskId); setStatus(task.status || ''); setResult(null); setError(''); setCancelling(false); setBudget(null)
  }

  useEffect(() => {
    if (!task.taskId || taskIsDone(status)) return undefined
    let live = true
    const id = setInterval(() => {
      aitaskApi.get(task.taskId).then(t => { if (live && t?.status) setStatus(t.status) }).catch(() => {})
    }, POLL_MS)
    return () => { live = false; clearInterval(id) }
  }, [task.taskId, status])

  // The validated result, read once the task is complete (also for a task
  // reopened after it finished).
  useEffect(() => {
    if (!task.taskId || status !== 'complete') return undefined
    let live = true
    aitaskApi.get(task.taskId).then(t => { if (live && t?.result) setResult({ kind: t.kind || task.kind, data: t.result }) }).catch(() => {})
    return () => { live = false }
  }, [task.taskId, task.kind, status])

  useEffect(() => {
    if (!task.taskId) return undefined
    let live = true
    const load = () => aitaskBudgetApi.forTask(task.taskId).then(b => { if (live && b?.capUsd > 0) setBudget(b) }).catch(() => {})
    load()
    const id = taskIsDone(status) ? null : setInterval(load, BUDGET_POLL_MS)
    return () => { live = false; if (id) clearInterval(id) }
  }, [task.taskId, status])

  const cancel = async () => {
    setCancelling(true)
    setError('')
    try {
      const t = await aitaskApi.cancel(task.taskId)
      setStatus(t?.status || 'cancelled')
    } catch (err) {
      setError(String(err?.message || err))
    } finally {
      setCancelling(false)
    }
  }

  const origin = task.origin
  return (
    <section aria-label="Coder task" data-testid="task-header" style={{
      display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', padding: '6px 12px',
      borderBottom: '1px solid var(--border)', fontFamily: 'var(--font-mono)', fontSize: 11, flexShrink: 0,
    }}>
      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--text)' }} title={task.goal}>{task.goal}</span>
      <span style={{ color: 'var(--cyan)', border: '1px solid var(--border-bright)', borderRadius: 999, padding: '1px 7px' }}>{status === 'complete' && task.doneLabel ? task.doneLabel : (status || 'queued')}</span>
      {budget && (
        <span data-testid="task-budget" title="Spent of this task's spend cap" style={{ color: 'var(--text-muted)' }}>
          {budget.reported ? money(budget.spentUsd) : '–'} / {money(budget.capUsd)}
        </span>
      )}
      {origin?.page && onNavigate && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => onNavigate(origin.page)}>Open in {origin.label || origin.page}</button>
      )}
      {!taskIsDone(status) && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={cancel} disabled={cancelling}>Cancel</button>
      )}
      {result && <TaskResultLine kind={result.kind} taskId={task.taskId} result={result.data} onNavigate={onNavigate} />}
      {error && <span role="alert" style={{ color: 'var(--red)', width: '100%' }}>{error}</span>}
    </section>
  )
}
