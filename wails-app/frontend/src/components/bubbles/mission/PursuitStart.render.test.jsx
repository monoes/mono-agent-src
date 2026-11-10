// @vitest-environment jsdom
// The pursuit start form: what it shows from the CLI's preview, the typed-phrase gate, the limit bounds, that the start is
// not callable before a preview with a token, and every result code shown honestly (never an automatic retry).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../../i18n.js'

const { go } = vi.hoisted(() => ({ go: { PursuitStartPreview: vi.fn(), PursuitStart: vi.fn() } }))
vi.mock('../../../wailsjs/go/main/App', () => go)
import PursuitStartForm from './PursuitStartForm.jsx'
import { expectedPhrase, limitOk } from '../../../lib/pursuitStartApi.js'

const HASH = 'ab12cd34' + 'e'.repeat(56)
const preview = (over = {}) => ({
  schema_version: 1, session_id: 7, chat_id: 'chat-7',
  goal: { state: 'proposed', hash: HASH, criteria: [{ id: 'c1', kind: 'file', spec: 'report.md exists', expected: 'yes' }, { id: 'c2', kind: 'rubric', spec: 'the report is clear', expected: '' }] },
  limits: { parked_max: 3, pct: 20, wakes_max: 6 }, rubric_present: true,
  cost: { rows: [{ key: 'mission_hour', usd: 15, known: true, text: 'One hour on this mission: about $15.' }, { key: 'host_hour', usd: 30, known: true, text: 'One hour on this computer: about $30.' }, { key: 'until_stop', usd: 6.5, known: true, text: 'Until the away limits stop it: about $6.50.' }], text: 'x' },
  checker: { set_up: false }, host: { loop_on: true, judge_pin: 'anthropic/sonnet', wake_cap_usd: 1, judge_call_cap_usd: 0.5 },
  blockers: [], phrase_template: 'I understand: pursue mission <ID>, goal <first 8 characters of the hash>, stop at <percent> percent spent, <parked> questions, <wakes> wake-ups',
  consent_digest: 'd'.repeat(64), start_token: 'tok-1', ...over,
})
const PHRASE = 'I understand: pursue mission 7, goal ab12cd34, stop at 20 percent spent, 3 questions, 6 wake-ups'
const reply = o => Promise.resolve(JSON.stringify(o))
const startDoc = (state, code = '', detail = '') => ({ ok: state === 'started', started: state === 'started', state, output: state === 'started' ? '{"pursuit":{}}' : '', detail, code })

beforeEach(() => { vi.clearAllMocks(); go.PursuitStartPreview.mockImplementation(() => reply(preview())); go.PursuitStart.mockImplementation(() => reply(startDoc('started'))) })
afterEach(cleanup)

const read = async () => { fireEvent.click(screen.getByTestId('pursuit-read')); return screen.findByTestId('pursuit-review') }
const typePhrase = v => fireEvent.change(screen.getByLabelText('Type the phrase'), { target: { value: v } })
const press = () => fireEvent.click(screen.getByTestId('pursuit-start'))
const open = () => render(<PursuitStartForm sessionId={7} />)

describe('the page cannot start before a preview', () => {
  it('has no phrase input and no Start button until the facts are read, and does not call the start', () => {
    open()
    expect(screen.queryByTestId('pursuit-start')).toBeNull()
    expect(screen.queryByLabelText('Type the phrase')).toBeNull()
    expect(go.PursuitStart).not.toHaveBeenCalled()
  })
  it('shows no review after a failed preview (no token), so Start never appears', async () => {
    go.PursuitStartPreview.mockImplementation(() => reply({ error: 'bad', code: 'unreadable' }))
    open(); fireEvent.click(screen.getByTestId('pursuit-read'))
    expect(await screen.findByTestId('pursuit-preview-error')).toHaveTextContent('The CLI')
    expect(screen.queryByTestId('pursuit-start')).toBeNull()
  })
  it('refuses a preview document without a start token', async () => {
    go.PursuitStartPreview.mockImplementation(() => reply(preview({ start_token: undefined })))
    open(); fireEvent.click(screen.getByTestId('pursuit-read'))
    await screen.findByTestId('pursuit-preview-error')
    expect(screen.queryByTestId('pursuit-start')).toBeNull()
  })
  it('a rejected binding call shows the error and no Start', async () => {
    go.PursuitStartPreview.mockImplementation(() => Promise.reject(new Error('no bridge')))
    open(); fireEvent.click(screen.getByTestId('pursuit-read'))
    expect(await screen.findByTestId('pursuit-preview-error')).toHaveTextContent('no bridge')
    expect(screen.queryByTestId('pursuit-start')).toBeNull()
  })
  it('changing a limit drops the old preview', async () => {
    open(); await read()
    fireEvent.change(screen.getByLabelText('Percent of budget'), { target: { value: '30' } })
    expect(screen.queryByTestId('pursuit-review')).toBeNull()
  })
})

describe('the facts', () => {
  it('shows the hash, every criterion, the cost rows, the away limits and the judge from the preview', async () => {
    open(); await read()
    expect(screen.getByTestId('pursuit-hash')).toHaveTextContent(HASH)
    expect(screen.getByTestId('pursuit-criteria')).toHaveTextContent('report.md exists')
    expect(screen.getByTestId('pursuit-criteria')).toHaveTextContent('the report is clear')
    expect(screen.getByTestId('pursuit-cost')).toHaveTextContent('about $15.')
    expect(screen.getByTestId('pursuit-cost')).toHaveTextContent('about $6.50.')
    expect(screen.getByTestId('pursuit-away')).toHaveTextContent('20 percent')
    expect(screen.getByTestId('pursuit-judge')).toHaveTextContent('anthropic/sonnet')
    expect(screen.queryByTestId('pursuit-checker')).toBeNull()
  })
  it('shows checker and unconfined rows only when the preview has them', async () => {
    go.PursuitStartPreview.mockImplementation(() => reply(preview({ checker: { set_up: true, command_criteria: 2, unconfined: 'allowed' } })))
    open(); await read()
    expect(screen.getByTestId('pursuit-checker')).toHaveTextContent('2 checks run commands')
    expect(screen.getByTestId('pursuit-unconfined')).toHaveTextContent('allowed')
  })
  it('lists the blockers with their reasons and keeps Start disabled even with the right phrase', async () => {
    go.PursuitStartPreview.mockImplementation(() => reply(preview({ blockers: [{ code: 'judge-pin', reason: 'no judge model is pinned' }] })))
    open(); await read(); typePhrase(PHRASE)
    expect(screen.getByTestId('pursuit-blockers')).toHaveTextContent('no judge model is pinned')
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
  })
  it.each([[{ rubric_present: false }], [{ host: { loop_on: false, judge_pin: '' } }]])('blocks when %j', async (over) => {
    go.PursuitStartPreview.mockImplementation(() => reply(preview(over)))
    open(); await read(); typePhrase(PHRASE)
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
  })
})

describe('the typed phrase', () => {
  it('is filled from the template with values of the preview only, and is not put in the input', async () => {
    open(); await read()
    expect(screen.getByTestId('pursuit-phrase-template')).toHaveTextContent(PHRASE)
    expect(screen.getByLabelText('Type the phrase')).toHaveValue('')
    expect(expectedPhrase(preview({ limits: { parked_max: 9, pct: 55, wakes_max: 4 } }))).toContain('stop at 55 percent spent, 9 questions, 4 wake-ups')
    expect(expectedPhrase({ ...preview(), phrase_template: undefined })).toBe('')
  })
  it('keeps Start disabled until the phrase is exactly right, and never calls the start meanwhile', async () => {
    open(); await read()
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
    typePhrase(PHRASE.replace('20 percent', '21 percent'))
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
    press()
    expect(go.PursuitStart).not.toHaveBeenCalled()
    typePhrase('  ' + PHRASE + ' ')
    expect(screen.getByTestId('pursuit-start')).toBeEnabled()
  })
  it('sends the typed phrase, the three limits as resolved and the token, once', async () => {
    open(); await read(); typePhrase(PHRASE); press()
    expect(await screen.findByTestId('pursuit-started')).toBeInTheDocument()
    expect(go.PursuitStart).toHaveBeenCalledTimes(1)
    expect(go.PursuitStart).toHaveBeenCalledWith(7, { parkedMax: 3, pct: 20, wakesMax: 6 }, PHRASE, 'tok-1')
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
  })
})

describe('limit inputs', () => {
  it.each([['0', false], ['-1', false], ['1.5', false], ['abc', false], ['', true], ['1', true], ['100', true]])('pct %j ok=%s', (v, ok) => expect(limitOk('pct', v)).toBe(ok))
  it('rejects a percent above 100 and counts above the sane bound', () => { expect(limitOk('pct', '101')).toBe(false); expect(limitOk('parkedMax', '1000001')).toBe(false); expect(limitOk('wakesMax', '1000000')).toBe(true) })
  it('shows the bounds message and disables reading for a bad limit', () => {
    open()
    fireEvent.change(screen.getByLabelText('Percent of budget'), { target: { value: '0' } })
    expect(screen.getByTestId('pursuit-limit-error')).toBeInTheDocument()
    expect(screen.getByTestId('pursuit-read')).toBeDisabled()
    expect(screen.getByLabelText('Percent of budget')).toHaveAttribute('min', '1')
  })
  it('sends typed limits to the preview, 0 for those left at the default', async () => {
    open()
    fireEvent.change(screen.getByLabelText('Percent of budget'), { target: { value: '20' } })
    await read()
    expect(go.PursuitStartPreview).toHaveBeenCalledWith(7, { parkedMax: 0, pct: 20, wakesMax: 0 })
  })
})

describe('the result of Start', () => {
  const go1 = async () => { open(); await read(); typePhrase(PHRASE); press() }
  it('started: says it is final and offers no second press', async () => {
    await go1()
    expect(await screen.findByTestId('pursuit-started')).toHaveTextContent('The goal is now final')
    expect(screen.queryByRole('alert')).toBeNull()
  })
  it('declined: says nothing started, keeps the preview, lets the person press again by hand', async () => {
    go.PursuitStart.mockImplementation(() => reply(startDoc('not_started', 'declined', 'the confirmation was declined; nothing started')))
    await go1()
    expect(await screen.findByTestId('pursuit-note-declined')).toHaveTextContent('Nothing started')
    expect(screen.getByTestId('pursuit-start')).toBeEnabled()
    await new Promise(r => setTimeout(r, 20))
    expect(go.PursuitStart).toHaveBeenCalledTimes(1) // never an automatic retry
  })
  it.each(['unknown'])('%s: says to check status, disables Start for good, and does not retry', async (code) => {
    go.PursuitStart.mockImplementation(() => reply(startDoc('unknown', code, 'may have started')))
    await go1()
    expect(await screen.findByTestId('pursuit-note-unknown')).toHaveTextContent('do not press Start again')
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
    await new Promise(r => setTimeout(r, 20))
    expect(go.PursuitStart).toHaveBeenCalledTimes(1)
  })
  it('a start call that rejects is unknown, not an error and not a retry', async () => {
    go.PursuitStart.mockImplementation(() => Promise.reject(new Error('bridge gone')))
    await go1()
    expect(await screen.findByTestId('pursuit-note-unknown')).toHaveTextContent('do not press Start again')
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
    expect(go.PursuitStart).toHaveBeenCalledTimes(1)
  })
  it.each([
    ['busy', 'Another confirmation is open'], ['phrase', 'The phrase does not match'], ['blocked', 'Something blocks'], ['no_native_dialog', 'no window'],
    ['operator_only', 'Only you can start'], ['chat_mismatch', 'another chat'], ['hidden_characters', 'cannot be shown safely'], ['criterion_too_long', 'too long'], ['invalid_input', 'malformed'],
  ])('%s: shows its words and the CLI detail, and nothing started', async (code, words) => {
    go.PursuitStart.mockImplementation(() => reply(startDoc('not_started', code, 'cli says so')))
    await go1()
    const note = await screen.findByTestId(`pursuit-note-${code}`)
    expect(note).toHaveTextContent(words)
    expect(note).toHaveTextContent('cli says so')
    expect(screen.queryByTestId('pursuit-started')).toBeNull()
  })
  it.each([['token', 'expired'], ['stale', 'goal changed'], ['consent_stale', 'has changed']])('%s: asks to read again and the read-again button reads a fresh preview', async (code, words) => {
    go.PursuitStart.mockImplementation(() => reply(startDoc('not_started', code, '')))
    await go1()
    const note = await screen.findByTestId(`pursuit-note-${code}`)
    expect(note).toHaveTextContent(words)
    go.PursuitStartPreview.mockImplementation(() => reply(preview({ start_token: 'tok-2' })))
    fireEvent.click(note.querySelector('button'))
    await waitFor(() => expect(go.PursuitStartPreview).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByTestId(`pursuit-note-${code}`)).toBeNull())
    expect(screen.getByLabelText('Type the phrase')).toHaveValue('') // a new preview, a new phrase
    expect(go.PursuitStart).toHaveBeenCalledTimes(1)
  })
  it('already_started: says it is running and disables Start', async () => {
    go.PursuitStart.mockImplementation(() => reply(startDoc('not_started', 'already_started', 'exists')))
    await go1()
    expect(await screen.findByTestId('pursuit-note-already_started')).toHaveTextContent('already running')
    expect(screen.getByTestId('pursuit-start')).toBeDisabled()
  })
  it('an unlisted code gets the generic words and the CLI detail', async () => {
    go.PursuitStart.mockImplementation(() => reply(startDoc('not_started', 'zzz', 'their words')))
    await go1()
    expect(await screen.findByTestId('pursuit-note-zzz')).toHaveTextContent('It did not start. their words')
  })
})
