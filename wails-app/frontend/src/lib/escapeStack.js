import { useEffect, useRef } from 'react'

// One shared Escape stack for modals that listen on the window. Escape goes to
// the topmost (most recently opened) one only, so a confirm opened over a
// modal closes the confirm and leaves the modal. A modal that wants to ignore
// Escape for a while (busy, a picker open over it) keeps its place on the
// stack and does nothing in its handler: it still shields the ones below.
const stack = []

function onKey(e) {
  if (e.key !== 'Escape') return
  const top = stack[stack.length - 1]
  top?.current?.()
}

// useEscapeClose(handler, active): while active, Escape calls handler if this
// is the topmost entry. Pass active=false for a modal that is mounted but not shown.
export function useEscapeClose(handler, active = true) {
  const ref = useRef(handler)
  ref.current = () => handler?.()
  useEffect(() => {
    if (!active) return undefined
    const entry = ref
    stack.push(entry)
    if (stack.length === 1) window.addEventListener('keydown', onKey)
    return () => {
      const i = stack.lastIndexOf(entry)
      if (i >= 0) stack.splice(i, 1)
      if (!stack.length) window.removeEventListener('keydown', onKey)
    }
  }, [active])
}
