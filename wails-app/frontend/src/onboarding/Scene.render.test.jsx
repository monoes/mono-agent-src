// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, act, cleanup } from '@testing-library/react'
import Scene from './Scene.jsx'

const { lang } = vi.hoisted(() => ({ lang: { value: 'en' } }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key, opts) => (opts ? `${key} ${JSON.stringify(opts)}` : key), i18n: { language: lang.value } }),
}))

const { created } = vi.hoisted(() => ({ created: [] }))
vi.mock('./scene/index.js', () => ({
  STEP_IDS: ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish'],
  createScene: opts => {
    const s = {
      opts,
      mount: vi.fn(el => { const r = document.createElement('div'); r.className = 'onb-scene'; r.setAttribute('aria-hidden', 'true'); el.appendChild(r) }),
      setState: vi.fn(),
      playBuild: vi.fn(() => Promise.resolve()),
      rebuild: vi.fn(),
      destroy: vi.fn(),
    }
    created.push(s)
    return s
  },
}))

const last = () => created[created.length - 1]
beforeEach(() => { created.length = 0; lang.value = 'en' })
afterEach(cleanup)

describe('Scene', () => {
  it('mounts one scene and destroys it on unmount', () => {
    const { unmount } = render(<Scene statuses={{}} currentStep="welcome" />)
    expect(created).toHaveLength(1)
    expect(last().mount).toHaveBeenCalledTimes(1)
    expect(last().setState).toHaveBeenLastCalledWith({}, 'welcome')
    unmount()
    expect(last().destroy).toHaveBeenCalledTimes(1)
  })

  it('plays the build when a step becomes done, not when it is merely current', () => {
    const { rerender } = render(<Scene statuses={{ welcome: 'done' }} currentStep="system" />)
    const s = last()
    rerender(<Scene statuses={{ welcome: 'done', system: 'skipped' }} currentStep="profile" />)
    expect(s.playBuild).not.toHaveBeenCalled()
    rerender(<Scene statuses={{ welcome: 'done', system: 'skipped', profile: 'done' }} currentStep="about" />)
    expect(s.playBuild).toHaveBeenCalledTimes(1)
    expect(s.playBuild).toHaveBeenCalledWith('profile')
    expect(s.setState).toHaveBeenLastCalledWith({ welcome: 'done', system: 'skipped', profile: 'done' }, 'about')
    // a late success on an earlier step builds then, even though the user moved on
    rerender(<Scene statuses={{ welcome: 'done', system: 'done', profile: 'done' }} currentStep="about" />)
    expect(s.playBuild).toHaveBeenCalledTimes(2)
    expect(s.playBuild).toHaveBeenLastCalledWith('system')
    // the same statuses again replay nothing
    rerender(<Scene statuses={{ welcome: 'done', system: 'done', profile: 'done' }} currentStep="monomind" />)
    expect(s.playBuild).toHaveBeenCalledTimes(2)
  })

  it('finished: tells the scene, and plays no build when a step turns done', () => {
    const { rerender } = render(<Scene statuses={{ about: 'todo' }} currentStep="about" finished />)
    const s = last()
    expect(s.setState).toHaveBeenLastCalledWith({ about: 'todo' }, 'about', { finished: true })
    rerender(<Scene statuses={{ about: 'done' }} currentStep="monomind" finished />)
    expect(s.playBuild).not.toHaveBeenCalled()
    expect(s.setState).toHaveBeenLastCalledWith({ about: 'done' }, 'monomind', { finished: true })
  })

  it('does not replay builds for steps that were already done on mount', () => {
    render(<Scene statuses={{ welcome: 'done', system: 'done', profile: 'done', about: 'skipped' }} currentStep="monomind" />)
    expect(last().playBuild).not.toHaveBeenCalled()
    expect(last().setState).toHaveBeenCalledWith({ welcome: 'done', system: 'done', profile: 'done', about: 'skipped' }, 'monomind')
  })

  it('does not touch the scene when a re-render brings equal statuses and the same step', () => {
    const { rerender } = render(<Scene statuses={{ welcome: 'done' }} currentStep="profile" />)
    const s = last()
    const calls = s.setState.mock.calls.length
    rerender(<Scene statuses={{ welcome: 'done' }} currentStep="profile" />)
    rerender(<Scene statuses={{ welcome: 'done' }} currentStep="profile" />)
    expect(s.setState.mock.calls.length).toBe(calls)
    rerender(<Scene statuses={{ welcome: 'done' }} currentStep="about" />)
    expect(s.setState.mock.calls.length).toBe(calls + 1)
  })

  it('refreshes the scene words when the language changes', () => {
    const { rerender } = render(<Scene statuses={{ welcome: 'done' }} currentStep="profile" />)
    const s = last()
    const calls = s.setState.mock.calls.length
    lang.value = 'es'
    rerender(<Scene statuses={{ welcome: 'done' }} currentStep="profile" />)
    expect(s.setState.mock.calls.length).toBe(calls + 1)
    expect(s.playBuild).not.toHaveBeenCalled()
  })

  it('passes translated counter text to the scene', () => {
    render(<Scene statuses={{}} currentStep="welcome" />)
    expect(last().opts.labels.found(3, 12)).toBe('onboarding.scene.found {"n":3,"total":12}')
    expect(last().opts.labels.plan('profile')).toBe('onboarding.scene.plan.profile')
    expect(last().opts.labels.plan('finish')).toBe('onboarding.scene.plan.finish')
  })

  it('Rebuild restores the village and removes the button', () => {
    render(<Scene statuses={{}} currentStep="finish" />)
    const s = last()
    expect(screen.queryByRole('button')).toBeNull()
    act(() => s.opts.onRampageEnd({ left: 120, top: 80 }))
    const btn = screen.getByRole('button', { name: 'onboarding.scene.rebuild' })
    expect(btn.closest('[aria-hidden="true"]')).toBeNull()
    expect(btn.style.left).toBe('120px')
    fireEvent.click(btn)
    expect(s.rebuild).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('removes the Rebuild button when the last step is left', () => {
    const { rerender } = render(<Scene statuses={{}} currentStep="finish" />)
    act(() => last().opts.onRampageEnd({ left: 0, top: 0 }))
    expect(screen.getByRole('button')).toBeInTheDocument()
    rerender(<Scene statuses={{}} currentStep="firstRun" />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
