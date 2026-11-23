// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => { localStorage.clear(); vi.resetModules() })

const load = async (stored) => {
  vi.resetModules()
  localStorage.clear()
  if (stored) localStorage.setItem('monoagent-lang', stored)
  return (await import('./i18n.js')).default
}

describe('initial language', () => {
  it('uses a stored supported language', async () => {
    expect((await load('es')).language).toBe('es')
  })
  it('ignores a stored language the app has no translation for', async () => {
    expect((await load('fr')).language).toBe('en')
  })
})
