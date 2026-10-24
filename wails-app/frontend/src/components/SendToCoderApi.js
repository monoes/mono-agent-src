import * as GoApp from '../wailsjs/go/main/App'

// The AI-task bindings (app_aitask.go), kept apart from services/api.js so the
// Send-to-coder pieces stand alone. Each call starts inside a promise, so a
// synchronous throw (no Wails bridge) lands in the caller's catch.
export const aitaskApi = {
  status: () => Promise.resolve().then(() => GoApp.AITaskStatus()),
  submit: (kind, goal, context = [], applicationId = '') =>
    Promise.resolve().then(() => GoApp.SubmitAITask(kind, goal, context, applicationId)),
  startFill: (applicationId) => Promise.resolve().then(() => GoApp.StartApplicationFill(applicationId)),
  get: (id) => Promise.resolve().then(() => GoApp.GetAITask(id)),
  cancel: (id) => Promise.resolve().then(() => GoApp.CancelAITask(id)),
}

// The propose-then-apply calls (app_aitask_apply.go): the CLI's JSON, parsed.
const parsed = (p) => p.then(s => (typeof s === 'string' ? JSON.parse(s) : s))
export const aitaskApplyApi = {
  preview: (id) => parsed(Promise.resolve().then(() => GoApp.PreviewFailedRunFix(id))),
  apply: (id, allowActive, expectHash) => parsed(Promise.resolve().then(() => GoApp.ApplyFailedRunFix(id, !!allowActive, expectHash))),
  saveDraft: (id) => parsed(Promise.resolve().then(() => GoApp.SaveGeneratedWorkflow(id))),
}

// selectionText is the context entry for text a button hands over (the Go side
// writes it to a file and passes it as selection=<path>).
export const selectionText = (text) => `selection-text=${text}`

// Terminal task states: nothing to poll or cancel after these.
export const taskIsDone = (status) => ['complete', 'failed', 'cancelled'].includes(status)
