import { flushSync } from 'react-dom'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Send, Square, Paperclip } from 'lucide-react'
import { AttachmentChips } from './ChatAttachments.jsx'

// ChatComposer is the draft textarea + send/stop control, extracted from
// the old assistant panel so its own interaction rules (IME composition, auto-resize)
// are independently testable. Fully controlled — the draft itself lives in
// the parent's `value` state, so nothing here is lost across a presentation
// change (docked/expanded/narrow) as long as the parent doesn't unmount.
//
// With onAttach (coder chats), files can be pasted, dropped or picked:
// onAttach(files) gets them, attachments are shown as chips above the
// draft, and onRemoveAttachment(id) drops one.
// The text being typed lives in this component's own state, so a keystroke
// re-renders only the composer. The parent's `value` (which sits above the whole
// transcript) is told about it at most every EMIT_MS, and at once on send, blur,
// composition end and unmount; onSend(text) hands over the exact text so the
// parent never reads a stale draft. An outside change to `value` (clearing after
// a send, restoring after a failed one) still replaces the local text.
const EMIT_MS = 250

export function ChatComposer({ value, onChange, onSend, onStop, streaming, disabled, disabledReason, attachments = [], onAttach, onRemoveAttachment }) {
  const textareaRef = useRef(null)
  const [text, setText] = useState(value)
  const textRef = useRef(value)
  const emitted = useRef([])      // values sent up that the parent has not echoed back yet
  const timer = useRef(null)
  const sizeFrame = useRef(0)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const fileRef = useRef(null)
  const [dragging, setDragging] = useState(false)
  const canAttach = !!onAttach && !disabled
  // Enter must never submit while an IME composition (CJK/etc candidate
  // selection) is in progress — isComposing is the standard signal, but a
  // few browsers still fire one stray keydown with keyCode 229 right as
  // composition ends, before isComposing has flipped back to false, so
  // both are checked.
  const composingRef = useRef(false)

  const flush = useCallback(() => {
    if (timer.current === null) return
    clearTimeout(timer.current)
    timer.current = null
    emitted.current.push(textRef.current)
    onChangeRef.current(textRef.current)
  }, [])
  useEffect(() => () => flush(), [flush])
  // The overlay decides what Escape does from the parent's draft and listens on
  // window in the capture phase, so the pending text is pushed up (and rendered)
  // before that listener runs: ours is registered first, as a child's effect.
  useEffect(() => {
    const onEsc = (e) => { if (e.key === 'Escape' && timer.current !== null) flushSync(flush) }
    window.addEventListener('keydown', onEsc, true)
    return () => window.removeEventListener('keydown', onEsc, true)
  }, [flush])

  // Parent value changed: an echo of what we sent is ignored; anything else is
  // an outside edit and wins (never while an IME composition is open).
  useEffect(() => {
    const i = emitted.current.indexOf(value)
    if (i >= 0) { emitted.current.splice(0, i + 1); return }
    if (value === textRef.current || composingRef.current) return
    if (timer.current !== null) { clearTimeout(timer.current); timer.current = null }
    emitted.current = []
    textRef.current = value
    setText(value)
  }, [value])

  const resize = useCallback(() => {
    if (sizeFrame.current) return
    sizeFrame.current = requestAnimationFrame(() => {
      sizeFrame.current = 0
      const el = textareaRef.current
      if (!el) return
      el.style.height = 'auto'
      el.style.height = Math.min(el.scrollHeight, 120) + 'px'
    })
  }, [])
  useEffect(() => () => { if (sizeFrame.current) cancelAnimationFrame(sizeFrame.current) }, [])
  useLayoutEffect(resize, [text, resize])

  const handleChange = (e) => {
    const next = e.target.value
    textRef.current = next
    setText(next)
    if (timer.current !== null) clearTimeout(timer.current)
    timer.current = setTimeout(flush, EMIT_MS)
  }

  const send = () => {
    flush()
    emitted.current = []   // whatever the parent does next (clear, restore) is an outside edit
    onSend(textRef.current)
  }

  const handleKeyDown = (e) => {
    if (e.key !== 'Enter' || e.shiftKey) return
    if (composingRef.current || e.nativeEvent?.isComposing || e.keyCode === 229) return
    e.preventDefault()
    send()
  }

  // A paste with files and no text attaches them; text (even with a
  // picture of it, as spreadsheets copy) pastes as text.
  const handlePaste = (e) => {
    const files = Array.from(e.clipboardData?.files || [])
    if (!canAttach || files.length === 0 || e.clipboardData.getData('text/plain')) return
    e.preventDefault()
    onAttach(files)
  }
  const hasFiles = e => Array.from(e.dataTransfer?.types || []).includes('Files')
  const dropProps = canAttach ? {
    onDragOver: e => { if (hasFiles(e)) { e.preventDefault(); setDragging(true) } },
    onDragLeave: () => setDragging(false),
    onDrop: e => {
      setDragging(false)
      const files = Array.from(e.dataTransfer?.files || [])
      if (files.length === 0) return
      e.preventDefault()
      onAttach(files)
    },
  } : {}

  return (
    <div data-testid="chat-composer" {...dropProps} style={{
      padding: '8px 12px 10px',
      borderTop: '1px solid rgba(0,180,216,0.1)',
      display: 'flex', flexDirection: 'column', gap: 6,
      flexShrink: 0,
      outline: dragging ? '1px dashed rgba(0,180,216,0.6)' : 'none', outlineOffset: -3,
    }}>
      {disabled && disabledReason && (
        <div style={{ padding: '8px 12px', background: 'rgba(251,191,36,.08)', border: '1px solid rgba(251,191,36,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 10, color: '#fbbf24' }}>
          {disabledReason}
        </div>
      )}
      {onAttach && <AttachmentChips attachments={attachments} onRemove={onRemoveAttachment} />}
      <div style={{ display: 'flex', gap: 6 }}>
        {onAttach && (
          <>
            <input ref={fileRef} type="file" multiple hidden data-testid="attach-input"
              onChange={e => { const files = Array.from(e.target.files || []); e.target.value = ''; if (files.length) onAttach(files) }} />
            <button
              type="button"
              onClick={() => fileRef.current?.click()}
              disabled={!canAttach}
              title="Attach images or files (or paste or drop them here)"
              aria-label="Attach files"
              style={{
                background: 'transparent', border: '1px solid rgba(0,180,216,0.15)', borderRadius: 8,
                padding: '0 9px', cursor: canAttach ? 'pointer' : 'default',
                color: canAttach ? '#00b4d8' : 'var(--text-muted)', display: 'flex', alignItems: 'center', flexShrink: 0,
              }}
            >
              <Paperclip size={13} />
            </button>
          </>
        )}
        <textarea
          ref={textareaRef}
          value={text}
          onChange={handleChange}
          onBlur={flush}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          onCompositionStart={() => { composingRef.current = true }}
          onCompositionEnd={() => { composingRef.current = false; flush() }}
          // Typing is blocked too, not just Send: with no usable backend
          // there is nothing a draft can be sent to, and a composer that
          // accepts text while refusing to send it reads as a broken app
          // rather than an unavailable one.
          disabled={disabled}
          placeholder="Type a message..."
          rows={1}
          style={{
            flex: 1,
            background: '#020509',
            border: '1px solid rgba(0,180,216,0.15)',
            borderRadius: 8,
            padding: '8px 10px',
            color: '#e2e8f0',
            fontFamily: 'var(--font-mono)', fontSize: 11,
            outline: 'none',
            resize: 'none',
            minHeight: 36,
            maxHeight: 120,
            lineHeight: 1.4,
          }}
        />
        {streaming ? (
          <button
            onClick={onStop}
            title="Stop generating"
            aria-label="Stop generating"
            style={{
              background: 'rgba(239,68,68,0.15)',
              border: '1px solid rgba(239,68,68,0.35)',
              borderRadius: 8,
              padding: '0 12px',
              cursor: 'pointer',
              color: '#ef4444',
              display: 'flex', alignItems: 'center',
              transition: 'all 100ms',
              flexShrink: 0,
            }}
            onMouseEnter={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.25)' }}
            onMouseLeave={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.15)' }}
          >
            <Square size={12} fill="#ef4444" />
          </button>
        ) : (
          <button
            onClick={send}
            disabled={!text.trim() || disabled}
            title={disabled ? 'No backend selected — see the notice above' : 'Send message'}
            aria-label="Send message"
            style={{
              background: !text.trim() || disabled ? 'rgba(0,180,216,0.05)' : 'rgba(0,180,216,0.15)',
              border: `1px solid ${!text.trim() || disabled ? 'rgba(0,180,216,0.08)' : 'rgba(0,180,216,0.3)'}`,
              borderRadius: 8,
              padding: '0 12px',
              cursor: !text.trim() || disabled ? 'default' : 'pointer',
              color: !text.trim() || disabled ? 'var(--text-muted)' : '#00b4d8',
              display: 'flex', alignItems: 'center',
              transition: 'all 100ms',
              flexShrink: 0,
            }}
            onMouseEnter={e => { if (text.trim() && !disabled) e.currentTarget.style.background = 'rgba(0,180,216,0.25)' }}
            onMouseLeave={e => { if (text.trim() && !disabled) e.currentTarget.style.background = 'rgba(0,180,216,0.15)' }}
          >
            <Send size={13} />
          </button>
        )}
      </div>
    </div>
  )
}
