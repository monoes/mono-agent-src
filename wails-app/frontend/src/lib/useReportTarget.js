// Where a chat's "Open in Tasks" report link points: the session's task that
// holds the final report. Read once per ask (the plan, then the integration
// check tasks' artifacts); {sessionId, taskId} or null. A failed read, or a
// session with no such task, leaves null and the session just opens.
import { useEffect, useState } from 'react'
import { sessionsApi } from '../services/taskSessions.js'
import { tasksApi } from '../services/tasks.js'
import { integrationTasks, pickReportTask } from './missionModel.js'

export function useReportTarget(sessionId, want) {
  const [target, setTarget] = useState(null)
  useEffect(() => {
    if (!want || !sessionId) { setTarget(null); return undefined }
    let live = true
    ;(async () => {
      const plan = await sessionsApi.plan(sessionId)
      const cands = plan?.error ? [] : integrationTasks(plan?.tasks)
      const by = {}
      await Promise.all(cands.map(async (t) => {
        const r = await tasksApi.show(t.id)
        if (!r?.error) by[t.id] = r?.artifacts || []
      }))
      const pick = pickReportTask(cands, by)
      if (live) setTarget(pick ? { sessionId, taskId: pick.taskId } : null)
    })()
    return () => { live = false }
  }, [sessionId, want])
  return target
}
