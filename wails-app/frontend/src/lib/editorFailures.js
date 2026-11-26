// Failure handling for the workflow editor's load / activate / open-run paths,
// kept out of NodeRunner.jsx so it can be tested without rendering the canvas.
// Every failure is reported through the injected notify(op, message).

import i18nOr from './i18nOr.js'

const why = (e) => e?.message || String(e)

// Opening a run replaces the canvas only when it holds a different workflow.
export const shouldConfirmRunSwitch = ({ isDirty, currentId, targetId }) =>
  !!isDirty && targetId !== currentId

// Optimistic Active toggle: the new state shows at once and is reverted, with
// the reason, when the backend refuses. An unsaved workflow has nothing to save.
export async function toggleActive({ id, prev, next, setActive, save, notify }) {
  setActive(next)
  if (!id) return
  try { await save(id, next) } catch (e) {
    setActive(prev)
    notify(i18nOr('nodeRunnerMore.op_activateWorkflow'), i18nOr(next ? 'ui6Node.activateFailed' : 'ui6Node.deactivateFailed', { error: why(e) }))
  }
}

// Fetch a workflow for the canvas. The running poll is stopped only once the
// workflow is in hand, so a failed load leaves the current run untouched.
export async function loadWorkflowChecked({ id, getWorkflow, stopPolling, notify }) {
  let wf
  try { wf = await getWorkflow(id) } catch (e) {
    notify(i18nOr('ui6Node.opLoadWorkflow'), i18nOr('ui6Node.loadFailed', { error: why(e) }))
    return null
  }
  if (!wf) { notify(i18nOr('ui6Node.opLoadWorkflow'), i18nOr('ui6Node.workflowGone')); return null }
  stopPolling()
  return wf
}

// Opening a run from the dashboard: confirm before a dirty canvas is replaced,
// and say why when the run's workflow cannot be loaded (the caller then still
// shows the run's own detail). { cancelled: true } leaves everything as is.
export async function openRunWorkflow({ detail, wfId, needLoad, isDirty, getWorkflow, confirmDiscard, notify }) {
  if (!needLoad) return { wf: null }
  if (shouldConfirmRunSwitch({ isDirty, currentId: wfId, targetId: detail.workflow_id }) && !(await confirmDiscard())) {
    return { cancelled: true }
  }
  try {
    const wf = await getWorkflow(detail.workflow_id)
    if (wf) return { wf }
    notify(i18nOr('ui6Node.opOpenRun'), i18nOr('ui6Node.runWorkflowGone', { id: detail.workflow_id }))
  } catch (e) {
    notify(i18nOr('ui6Node.opOpenRun'), i18nOr('ui6Node.runWorkflowUnloadable', { id: detail.workflow_id, error: why(e) }))
  }
  return { wf: null }
}
