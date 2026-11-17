// Run-poll helpers for the workflow editor.

// api.getExecutionDetail turns any failure into null (plus a toast), so a
// poll that keeps getting null never learns the run's real state. After this
// many empty polls in a row the editor stops polling and says so.
export const POLL_FAIL_LIMIT = 5

export const nextPollFailures = (count, detail) => (detail ? 0 : count + 1)

// api.cancelWorkflow does not throw: a failure comes back as "error: ...".
export const cancelFailed = (result) => typeof result === 'string' && result.startsWith('error:')
