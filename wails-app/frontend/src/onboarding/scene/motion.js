// The one place the scene reads the reduced-motion setting, so tests can stub it.
export function prefersReducedMotion() {
  try {
    return typeof window !== 'undefined' && typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return false
  }
}
