import { useEffect } from 'react'

// Stands in for a step until its task replaces the entry in STEP_COMPONENTS.
// The shell already shows the step's eyebrow and heading; this only lets
// Continue through, so the flow can be walked. It marks nothing.
export default function Placeholder({ setCanContinue }) {
  useEffect(() => { setCanContinue(true) }, [setCanContinue])
  return null
}
