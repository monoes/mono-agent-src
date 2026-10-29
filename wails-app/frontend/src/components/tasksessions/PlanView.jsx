import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { sessionsApi } from '../../services/taskSessions.js'
import { planTree } from '../../lib/taskSessionModel.js'

// PLAN_PAGE is how many rows the plan draws at first, and how many more each "show more" adds.
const PLAN_PAGE = 100

// capTree keeps the first `limit` rows of the tree in reading order (a parent before its children) and says how
// many it left out; a row's children are cut at the same point.
function capTree(nodes, limit) {
  let left = limit
  let total = 0
  const walk = (list) => {
    const out = []
    for (const n of list) {
      total++
      if (left <= 0) { count(n.children); continue }
      left--
      out.push({ task: n.task, children: walk(n.children) })
    }
    return out
  }
  const count = (list) => { for (const n of list) { total++; count(n.children) } }
  const kept = walk(nodes)
  return { kept, hidden: total - (limit - left) }
}

function Row({ node, onOpen, t }) {
  const task = node.task
  return (
    <li>
      <div className="ts-plan-row">
        <button type="button" className="ts-plan-title" onClick={() => onOpen(task.id)}>{task.title}</button>
        <span className="ts-plan-meta">
          <span>#{task.id}</span>
          {task.kind && task.kind !== 'task' && <span className="ts-chip ts-chip--kind">{t(`tasks.session.kind.${task.kind}`, { defaultValue: task.kind })}</span>}
          <span>{t(`tasks.column.${task.status}`, { defaultValue: task.status })}</span>
          {task.blocked && <span className="ts-badge ts-badge--blocked">{t('tasks.session.blocked')}</span>}
          {task.verified && <span className="ts-badge ts-badge--verified">{t('tasks.session.verified')}</span>}
          {task.depends_on?.length > 0 && <span>{t('tasks.session.after', { ids: task.depends_on.map(i => `#${i}`).join(', ') })}</span>}
        </span>
      </div>
      {task.acceptance && <div className="ts-plan-accept"><strong>{t('tasks.session.acceptance')}:</strong> {task.acceptance}</div>}
      {node.children.length > 0 && <ul className="ts-plan-tree">{node.children.map(c => <Row key={c.task.id} node={c} onOpen={onOpen} t={t} />)}</ul>}
    </li>
  )
}

// The plan as a tree: epics, tasks, what each waits for, and how it is judged.
// Read again when the session's revision moves.
export default function PlanView({ sessionId, rev, onOpen }) {
  const { t } = useTranslation()
  const [plan, setPlan] = useState(null)
  const [error, setError] = useState('')
  const [limit, setLimit] = useState(PLAN_PAGE)
  const tree = useMemo(() => planTree(plan), [plan])
  const capped = useMemo(() => capTree(tree, limit), [tree, limit])
  useEffect(() => {
    let live = true
    sessionsApi.plan(sessionId).then(r => {
      if (!live) return
      if (r?.error) setError(r.error)
      else { setError(''); setPlan(r.tasks || []) }
    })
    return () => { live = false }
  }, [sessionId, rev])
  if (error) return <div role="alert">{t('tasks.session.planError')}: {error}</div>
  if (!plan) return <div>{t('tasks.loading')}</div>
  if (!plan.length) return <div className="tb-empty">{t('tasks.session.planEmpty')}</div>
  return (
    <>
      <ul className="ts-plan-tree ts-plan" aria-label={t('tasks.session.view_plan')}>{capped.kept.map(n => <Row key={n.task.id} node={n} onOpen={onOpen} t={t} />)}</ul>
      {capped.hidden > 0 && (
        <button type="button" className="btn btn-ghost btn-sm tb-more" onClick={() => setLimit(l => l + PLAN_PAGE)}>
          {t('tasks.session.planShowMore', { count: Math.min(PLAN_PAGE, capped.hidden), left: capped.hidden })}
        </button>
      )}
    </>
  )
}
