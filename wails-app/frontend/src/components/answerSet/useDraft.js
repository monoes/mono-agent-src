import { useCallback, useState } from 'react'
import { dropDraft, getDraft, setDraft } from '../../lib/answerDrafts.js'

// useState whose value is also kept as a draft under key ('' keeps it local).
export function useDraft(key, initial) {
  const [held, setHeld] = useState(() => ({ key, value: getDraft(key) ?? initial }))
  // Another question under the same component: its own draft.
  const value = held.key === key ? held.value : (getDraft(key) ?? initial)
  const set = useCallback(next => {
    setHeld(prev => {
      const was = prev.key === key ? prev.value : (getDraft(key) ?? initial)
      const v = typeof next === 'function' ? next(was) : next
      setDraft(key, v)
      return { key, value: v }
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- initial is only the starting value
  }, [key])
  const drop = useCallback(() => dropDraft(key), [key])
  return [value, set, drop]
}
