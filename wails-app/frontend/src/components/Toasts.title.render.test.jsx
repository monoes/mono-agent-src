// @vitest-environment jsdom
// A toast with a title of its own shows it in place of "Failed: <name>".
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, act, cleanup } from '@testing-library/react'
import i18n from '../i18n.js'
import Toasts from './Toasts.jsx'
import { notify } from '../services/api.js'
import en from '../locales/en.json'
import es from '../locales/es.json'

afterEach(cleanup)

describe('toast titles', () => {
  it('shows the title it was given and keeps the reason', () => {
    render(<Toasts />)
    act(() => notify('a2ui', 'question q1 is not open any more', '', i18n.t('a2ui.pressFailed')))
    expect(screen.getByText('Card action not completed')).toBeTruthy()
    expect(screen.getByText('question q1 is not open any more')).toBeTruthy()
    expect(screen.queryByText(/a2ui/)).toBeNull()
    act(() => notify('chat', 'boom'))
    expect(screen.getByText('Failed: chat')).toBeTruthy()
  })
  it('has both card titles in English and Spanish', () => {
    for (const key of ['pressFailed', 'queuedTitle']) {
      expect(en.a2ui[key]).toBeTruthy()
      expect(es.a2ui[key]).toBeTruthy()
      expect(es.a2ui[key]).not.toBe(en.a2ui[key])
    }
  })
})
