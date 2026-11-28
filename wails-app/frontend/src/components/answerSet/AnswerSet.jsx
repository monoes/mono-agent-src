// Several questions waiting in one reply are answered together (#1803). Each
// pending question (an A2UI card, a chat block, an org role's question) says so
// with useAnswerItem. From two of one group on, none of them sends on its own:
// they only hold what the person picked, and one AnswerSetBar sends the whole
// set once every question is answered (or, where allowed, skipped). Nothing is
// ever sent without that press.
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import './answerSet.css'

const Api = createContext(null)
const Items = createContext(null)

export function AnswerSetProvider({ onSubmit, children }) {
  const collectors = useRef(new Map())
  const [items, setItems] = useState(() => new Map())
  const [sending, setSending] = useState(() => new Set())
  const submitRef = useRef(onSubmit)
  submitRef.current = onSubmit
  const itemsRef = useRef(items)
  itemsRef.current = items
  const inFlight = useRef(new Set())

  const api = useMemo(() => ({
    collectors: collectors.current,
    put(key, meta) {
      setItems(prev => {
        const had = prev.get(key)
        if (had && had.group === meta.group && had.ready === meta.ready && had.skipped === meta.skipped) return prev
        return new Map(prev).set(key, meta)
      })
    },
    remove(key) {
      setItems(prev => {
        if (!prev.has(key)) return prev
        const next = new Map(prev)
        next.delete(key)
        return next
      })
    },
  }), [])

  // The set as it is on screen at the press: every item of the group, ready.
  const submit = useCallback(async group => {
    if (inFlight.current.has(group) || !submitRef.current) return
    const mine = [...itemsRef.current].filter(([, m]) => m.group === group)
    if (mine.length < 2 || mine.some(([, m]) => !m.ready) || mine.every(([, m]) => m.skipped)) return
    const parts = mine.map(([key]) => api.collectors.get(key)?.current()).filter(Boolean)
    if (parts.length !== mine.length) return
    inFlight.current.add(group)
    setSending(new Set(inFlight.current))
    try {
      await submitRef.current(parts)
    } finally {
      inFlight.current.delete(group)
      setSending(new Set(inFlight.current))
    }
  }, [api])

  const state = useMemo(() => ({ items, sending, submit }), [items, sending, submit])
  return <Api.Provider value={api}><Items.Provider value={state}>{children}</Items.Provider></Api.Provider>
}

const countOf = (items, group) => [...items.values()].filter(m => m.group === group).length

// useAnswerItem registers a pending question of a group ('' = not pending).
// ready says it is answered or skipped; collect() returns what to send for it.
// grouped: the group holds other questions, so this one must not send alone.
export function useAnswerItem(key, { group, ready = false, skipped = false, collect }) {
  const api = useContext(Api)
  const state = useContext(Items)
  const collectRef = useRef(collect)
  collectRef.current = collect
  const on = !!api && !!group
  useLayoutEffect(() => {
    if (!on) { api?.remove(key); return }
    api.collectors.set(key, collectRef)
    api.put(key, { group, ready, skipped })
  }, [api, on, key, group, ready, skipped])
  useEffect(() => () => { api?.collectors.delete(key); api?.remove(key) }, [api, key])
  const grouped = on && countOf(state.items, group) > 1
  return { grouped, sending: on && state.sending.has(group) }
}

// SkipToggle leaves one question out of the set, by the person's own choice.
export function SkipToggle({ skipped, onChange, disabled }) {
  const { t } = useTranslation()
  return (
    <button type="button" className="answer-skip" aria-pressed={skipped} disabled={disabled} onClick={() => onChange(!skipped)}>
      {t(skipped ? 'answerSet.skipped' : 'answerSet.skip')}
    </button>
  )
}

export function AnswerSetBar({ group }) {
  const { t } = useTranslation()
  const state = useContext(Items)
  const mine = state && group ? [...state.items.values()].filter(m => m.group === group) : []
  if (mine.length < 2) return null
  const ready = mine.filter(m => m.ready).length
  const nothing = mine.every(m => m.skipped)
  const sending = state.sending.has(group)
  return (
    <div className="answer-set-bar" data-testid="answer-set-bar" role="group" aria-label={t('answerSet.label')}>
      <p>
        <strong>{t('answerSet.waiting', { count: mine.length })}</strong>{' '}
        {t('answerSet.how')}
      </p>
      <div className="answer-set-actions">
        <span role="status">{nothing ? t('answerSet.allSkipped') : t('answerSet.progress', { ready, total: mine.length })}</span>
        <button type="button" className="btn btn-primary btn-sm" disabled={ready < mine.length || nothing || sending} onClick={() => state.submit(group)}>
          {t(sending ? 'answerSet.sending' : 'answerSet.sendAll')}
        </button>
      </div>
    </div>
  )
}
