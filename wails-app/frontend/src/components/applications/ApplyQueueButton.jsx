import React, { useState } from 'react'
import { Bot } from 'lucide-react'
import { aitaskApi } from '../SendToCoderApi.js'
import { useSendToCoder } from '../bubbles/CoderBubbleContext.jsx'
import { fetchApplicationAssets } from './applicationAssets.js'
import { parkedApi } from './parkedApi.js'

const noteStyle = { fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }

const WATCH_EVERY_MS = 4000
const WATCH_FOR_MS = 10 * 60 * 1000

// dockQueueChats docks a collapsed coder bubble for each queued application as
// its fill starts: the queue runs in the background and returns no chats, so
// the applications are asked until each one has a task conversation. It
// outlives the button (the list reloads after the start).
function dockQueueChats(ids, openDraft) {
  const waiting = new Set(ids)
  const until = Date.now() + WATCH_FOR_MS
  const tick = async () => {
    for (const id of [...waiting]) {
      const task = (await fetchApplicationAssets(id))?.task
      if (!task?.conversationId) continue
      waiting.delete(id)
      openDraft({
        conversationId: task.conversationId, collapsed: true,
        task: { taskId: task.taskId, kind: 'application-fill', goal: 'Fill in the application form in your browser (you submit it)', status: task.status, origin: { page: 'applications', label: 'Applications' }, doneLabel: 'filled, waiting for you to submit' },
      })
    }
    if (waiting.size > 0 && Date.now() < until) setTimeout(tick, WATCH_EVERY_MS)
  }
  setTimeout(tick, WATCH_EVERY_MS)
}

// ApplyQueueButton hands the selected applications to the coder queue
// (`application fill queue`): it fills them one by one, keeping at most the
// saved "max parked" (default 3) running or waiting for the person's submit.
// The queue runs in the background; each application shows its own state.
// With coder mode off it says why and starts nothing.
export default function ApplyQueueButton({ ids, onStarted, style }) {
  const { openDraft } = useSendToCoder()
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState(null)

  const start = async () => {
    if (busy || ids.length === 0) return
    setBusy(true)
    setNote(null)
    try {
      const state = await aitaskApi.status()
      if (!state?.enabled) {
        setNote({ text: `${state?.reason || 'Coder mode is off.'} Apply works the regular way.` })
        return
      }
      await parkedApi.queue(ids, 0)
      if (openDraft) dockQueueChats(ids, openDraft)
      setNote({ text: `Started: the coder fills ${ids.length === 1 ? 'this application' : `these ${ids.length} applications`} one after another and leaves each form for you to check and submit.` })
      onStarted?.()
    } catch (err) {
      setNote({ text: String(err?.message || err), error: true })
    } finally {
      setBusy(false)
    }
  }

  return (
    <span style={{ display: 'inline-flex', flexDirection: 'column', gap: 4, alignItems: 'flex-end' }}>
      <button type="button" style={style} onClick={start} disabled={busy || ids.length === 0} aria-busy={busy}>
        <Bot size={12} aria-hidden="true" /> {busy ? 'Starting…' : `Apply selected with coder${ids.length ? ` (${ids.length})` : ''}`}
      </button>
      {note && <span role={note.error ? 'alert' : 'status'} style={{ ...noteStyle, color: note.error ? '#f43f5e' : noteStyle.color, maxWidth: 360, textAlign: 'right' }}>{note.text}</span>}
    </span>
  )
}
