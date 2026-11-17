// Failure handling for the workflow editor's load / activate / open-run paths,
// kept out of NodeRunner.jsx so it can be tested without rendering the canvas.
// Every failure is reported through the injected notify(op, message).

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
    notify('activate workflow', `Could not ${next ? 'activate' : 'deactivate'} the workflow: ${why(e)}`)
  }
}

// Fetch a workflow for the canvas. The running poll is stopped only once the
// workflow is in hand, so a failed load leaves the current run untouched.
export async function loadWorkflowChecked({ id, getWorkflow, stopPolling, notify }) {
  let wf
  try { wf = await getWorkflow(id) } catch (e) {
    notify('load workflow', `Could not load the workflow: ${why(e)}`)
    return null
  }
  if (!wf) { notify('load workflow', 'That workflow no longer exists.'); return null }
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
    notify('open run', `Workflow ${detail.workflow_id} no longer exists, so only the run's own detail is shown.`)
  } catch (e) {
    notify('open run', `Could not load workflow ${detail.workflow_id} (${why(e)}), so only the run's own detail is shown.`)
  }
  return { wf: null }
}
