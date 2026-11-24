// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k) => k }) }))
const runHelpAction = vi.fn().mockResolvedValue({ ok: true })
vi.mock('./helpActions.js', () => ({ runHelpAction: (...a) => runHelpAction(...a) }))
vi.mock('./helpContent.js', () => ({
  TOPICS: [
    { id: 'start', title: 'Get started', blurb: 'First run', emoji: 'x', quip: 'Buckle up.', steps: [{ text: 'Open templates', action: { kind: 'goto', target: 'noderunner', params: { openTemplates: true } } }, { text: 'Ask', action: { kind: 'coder', prompt: 'p' } }] },
    { id: 'coder', title: 'Coder mode', blurb: 'Full access', emoji: 'y', quip: 'Power.', danger: { level: 4, quip: 'Careful.' }, steps: [{ text: 'Settings', action: { kind: 'goto', target: 'settings' } }] },
  ],
  PIT_STAGES: [
    { level: 1, permission: 'read', label: 'Read', consequence: 'c1', mascotMood: 'calm' },
    { level: 2, permission: 'write', label: 'Write', consequence: 'c2', mascotMood: 'nervous' },
    { level: 3, permission: 'full', label: 'Full', consequence: 'c3', mascotMood: 'panic' },
  ],
}))
import Help from './Help.jsx'
import { searchEgg, createKonami, clickQuip, shouldConfetti } from './helpEasterEggs.js'
import { CoderBubbleContext } from '../../components/bubbles/CoderBubbleContext.jsx'
import en from '../../locales/en.json'
import es from '../../locales/es.json'
import fs from 'node:fs'

afterEach(() => { cleanup(); runHelpAction.mockClear() })

describe('Help page', () => {
  it('expands a topic and runs its step action with navigate and coder deps', async () => {
    const onNavigate = vi.fn()
    const openNewChat = vi.fn()
    render(<CoderBubbleContext.Provider value={{ openNewChat, openDraft: null, navigate: null }}><Help onNavigate={onNavigate} /></CoderBubbleContext.Provider>)
    fireEvent.click(screen.getByText('Get started'))
    fireEvent.click(screen.getAllByRole('button', { name: 'help.topic.takeMe' })[0])
    await waitFor(() => expect(runHelpAction).toHaveBeenCalled())
    const [action, deps] = runHelpAction.mock.calls[0]
    expect(action).toEqual({ kind: 'goto', target: 'noderunner', params: { openTemplates: true } })
    expect(deps.navigate).toBe(onNavigate)
    expect(deps.openNewChat).toBe(openNewChat)
  })

  it('shows a message when an action reports failure', async () => {
    runHelpAction.mockResolvedValueOnce({ ok: false })
    render(<Help onNavigate={vi.fn()} />)
    fireEvent.click(screen.getByText('Get started'))
    fireEvent.click(screen.getAllByRole('button', { name: 'help.topic.askCoder' })[0])
    await waitFor(() => expect(screen.getByTestId('help-note')).toHaveTextContent('help.actionFailed'))
  })

  it('filters topics and offers a way out when nothing matches', () => {
    render(<Help onNavigate={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('help.searchLabel'), { target: { value: 'full access' } })
    expect(screen.queryByTestId('help-topic-start')).not.toBeInTheDocument()
    expect(screen.getByTestId('help-topic-coder')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('help.searchLabel'), { target: { value: 'zzz' } })
    expect(screen.getByTestId('help-empty')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'help.clear' }))
    expect(screen.getByTestId('help-topic-start')).toBeInTheDocument()
  })

  it('escalates the permission pit and revokes everything in one click', () => {
    render(<Help onNavigate={vi.fn()} />)
    const pit = screen.getByTestId('help-pit')
    expect(screen.getByTestId('pit-consequence')).toHaveTextContent('help.pit.calm')
    const grant = screen.getByRole('button', { name: 'help.pit.grant' })
    fireEvent.click(grant); fireEvent.click(grant)
    expect(screen.getByTestId('pit-consequence')).toHaveTextContent('c2')
    expect(pit).toHaveClass('pit-2')
    fireEvent.click(grant)
    expect(grant).toBeDisabled()
    expect(pit).toHaveClass('pit-3')
    fireEvent.click(screen.getByRole('button', { name: /help.pit.revoke/ }))
    expect(pit).toHaveClass('pit-0')
    expect(screen.getByTestId('pit-consequence')).toHaveTextContent('help.pit.calm')
  })

  it('pit ends in a real page and a coder prompt', async () => {
    render(<Help onNavigate={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: /help.pit.review/ }))
    fireEvent.click(screen.getByRole('button', { name: /help.pit.askCoder/ }))
    await waitFor(() => expect(runHelpAction).toHaveBeenCalledTimes(2))
    expect(runHelpAction.mock.calls[0][0]).toEqual({ kind: 'goto', target: 'settings' })
    expect(runHelpAction.mock.calls[1][0]).toEqual({ kind: 'coder', prompt: 'help.pit.coderPrompt' })
  })

  it('pokes the mascot, throws confetti on the seventh poke, and toggles party mode with the konami code', () => {
    render(<Help onNavigate={vi.fn()} />)
    const mascot = screen.getByRole('button', { name: 'help.mascotLabel' })
    fireEvent.click(mascot)
    expect(screen.getByTestId('help-note')).toHaveTextContent('help.egg.quip1')
    for (let i = 0; i < 6; i++) fireEvent.click(mascot)
    expect(screen.getByTestId('help-confetti')).toBeInTheDocument()
    for (const k of ['ArrowUp', 'ArrowUp', 'ArrowDown', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'ArrowLeft', 'ArrowRight', 'b', 'a']) fireEvent.keyDown(window, { key: k })
    expect(screen.getByTestId('help-page')).toHaveClass('help-party')
  })
})

describe('helpEasterEggs', () => {
  it('detects search eggs, cycles quips, and the konami code', () => {
    expect(searchEgg('sudo make me a sandwich')).toBe('help.egg.sudo')
    expect(searchEgg('vault')).toBeNull()
    expect(clickQuip(6)).toBe('help.egg.quip1')
    expect(shouldConfetti(7)).toBe(true)
    expect(shouldConfetti(6)).toBe(false)
    const feed = createKonami()
    expect(feed('ArrowUp')).toBe(false)
    expect(feed('x')).toBe(false)
  })
})

describe('help locale keys', () => {
  const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
  it('en and es define the same help keys, and every key used in source exists', () => {
    const e = flat(en.help, 'help.'), s = flat(es.help, 'help.')
    expect(s.sort()).toEqual(e.slice().sort())
    expect(en.sidebar.nav.help).toBeTruthy()
    expect(es.sidebar.nav.help).toBeTruthy()
    const dir = process.cwd() + '/src/pages/Help/'
    const used = new Set()
    for (const f of ['Help.jsx', 'HelpTopicCard.jsx', 'PermissionPit.jsx', 'helpEasterEggs.js']) {
      for (const m of fs.readFileSync(dir + f, 'utf8').matchAll(/['"`](help\.[A-Za-z0-9_.]+)['"`]/g)) used.add(m[1])
    }
    for (const k of used) expect(e, k).toContain(k)
  })
})

// Found in a real browser: an opened topic stayed in its one 320px grid cell and its steps were squeezed into a
// sliver beside the step number and button. The open topic takes the whole row and a step wraps its button.
describe('Help layout', () => {
  const css = fs.readFileSync(process.cwd() + '/src/pages/Help/Help.css', 'utf8')
  it('an opened topic spans the whole grid row', () => {
    expect(css).toMatch(/\.help-topic\.open\s*\{[^}]*grid-column:\s*1\s*\/\s*-1/)
  })
  it('a step wraps its action instead of squeezing its text', () => {
    expect(css).toMatch(/\.help-step\s*\{[^}]*flex-wrap:\s*wrap/)
    expect(css).toMatch(/\.help-step-text\s*\{[^}]*flex:\s*1\s+1\s+\d+px/)
  })
})
