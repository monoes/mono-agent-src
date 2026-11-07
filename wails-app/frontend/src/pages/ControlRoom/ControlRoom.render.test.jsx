// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within, act } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import '../../i18n.js'
const { sessionsApi } = vi.hoisted(() => ({ sessionsApi: { list: vi.fn() } }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi }))
import ControlRoom from './ControlRoom.jsx'
import StopPanel from './StopPanel.jsx'
import SpendMeter from './SpendMeter.jsx'
import TreeClusters from './TreeClusters.jsx'

// The documents are the CLI's golden files: the page is tested against the contract, not a copy of it.
const here = dirname(fileURLToPath(import.meta.url))
const golden = (n) => JSON.parse(readFileSync(join(here, '../../../../../internal/cli/doomsday/testdata', `controlroom_${n}.golden.json`), 'utf8'))
const running = golden('overview_running')
const unverified = golden('overview_unverified')
const page = golden('feed_page')

let api
beforeEach(() => {
  sessionsApi.list.mockReset(); sessionsApi.list.mockResolvedValue({ sessions: [] })
  api = {
    overview: vi.fn().mockResolvedValue(running),
    feed: vi.fn().mockResolvedValue(page),
    stop: vi.fn().mockResolvedValue({ ok: true, stopped: true, output: 'stopped', detail: '', code: '' }),
  }
})
afterEach(cleanup)

const show = async (doc, find) => {
  api.overview.mockResolvedValue(doc)
  render(<ControlRoom api={api} />)
  return find ? screen.findByTestId(find) : screen.findByTestId('cr-page')
}

describe('states of the page', () => {
  it('off: says so, no tree, spend, feed or Stop', async () => {
    await show(golden('overview_off'), 'cr-off')
    for (const id of ['cr-tree', 'cr-spend', 'cr-feed', 'cr-stop']) expect(screen.queryByTestId(id)).toBeNull()
    expect(api.feed).not.toHaveBeenCalled()
  })
  it('no run: says so, nothing else', async () => {
    await show(golden('overview_no_run'), 'cr-no-run')
    const hint = screen.getByTestId('cr-no-run-hint').textContent
    for (const verb of ['doomsday render --charter', 'doomsday start --charter', '--boss-session', 'doomsday status']) expect(hint).toContain(verb)
    expect(screen.queryByRole('button', { name: /start/i })).toBeNull()
    for (const id of ['cr-tree', 'cr-spend', 'cr-feed', 'cr-stop']) expect(screen.queryByTestId(id)).toBeNull()
  })
  it('healthy: header, clusters, spend, feed and Stop', async () => {
    await show(running, 'cr-tree')
    expect(screen.getByTestId('cr-run-state')).toHaveTextContent('Run active')
    expect(screen.getByTestId('cr-integrity')).toHaveAttribute('data-chain', 'yes')
    expect(screen.getAllByTestId('cr-cluster')).toHaveLength(2)
    expect(screen.getByTestId('cr-spend')).toHaveAttribute('data-spend', 'ok')
    expect(screen.getByTestId('cr-spend')).toHaveTextContent('$0.0028')
    expect(await screen.findAllByTestId('cr-feed-row')).toHaveLength(3)
    expect(screen.getByTestId('cr-stop')).toBeInTheDocument()
    expect(screen.queryByTestId('cr-bad-chain')).toBeNull()
  })
  it('bad chain: a banner, never a healthy state, no tree, spend or rows; Stop stays', async () => {
    await show(unverified, 'cr-bad-chain')
    expect(screen.getByRole('alert')).toHaveTextContent('does not verify')
    expect(screen.getByTestId('cr-integrity')).toHaveAttribute('data-chain', 'no')
    expect(screen.getByTestId('cr-integrity')).not.toHaveTextContent('verified')
    for (const id of ['cr-tree', 'cr-spend', 'cr-feed']) expect(screen.queryByTestId(id)).toBeNull()
    expect(api.feed).not.toHaveBeenCalled()
    expect(screen.getByTestId('cr-run-state')).toHaveTextContent('Run stopping')
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it('unknown chain: a banner that says it could not be checked', async () => {
    await show({ ...unverified, integrity: { chain_verified: 'unknown', head_seq: 0 } }, 'cr-bad-chain')
    expect(screen.getByTestId('cr-bad-chain')).toHaveAttribute('data-chain', 'unknown')
    expect(screen.getByTestId('cr-bad-chain')).toHaveTextContent('could not be checked')
    expect(screen.getByTestId('cr-integrity')).toHaveTextContent('not checked')
    expect(screen.queryByTestId('cr-spend')).toBeNull()
  })
  it('a missing integrity block is never healthy', async () => {
    await show({ ...running, integrity: undefined }, 'cr-bad-chain')
    expect(screen.queryByTestId('cr-tree')).toBeNull()
  })
  it('error from the CLI: shown, no data, polling goes on', async () => {
    await show({ error: 'operator only', code: 'operator_only' }, 'cr-error')
    expect(screen.getByTestId('cr-error')).toHaveTextContent('operator only')
    // The first poll failed, so a run may be live: Stop is offered, labelled as "run state unknown".
    expect(screen.getByTestId('cr-stop')).toBeInTheDocument()
    expect(screen.getByTestId('cr-stop-unknown')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it.each([
    ['an {error} document', { error: 'database is locked' }],
    ['a read-error document', golden('overview_error')],
    ['an unknown schema', { schema_version: 99, available: true }],
  ])('first poll fails with %s: Stop is reachable and the banner may say so', async (_n, doc) => {
    await show(doc, 'cr-error')
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    expect(screen.getByTestId('cr-stop-confirm')).toBeInTheDocument()
  })
  it.each([
    ['unanchored', golden('overview_unanchored')],
    ['no', unverified],
    ['unknown', { ...running, integrity: { chain_verified: 'unknown' } }],
  ])('chain %s: Stop is reachable', async (_n, doc) => {
    await show(doc, 'cr-stop')
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it.each([
    ['no', { ...unverified, integrity: { chain_verified: 'no', head_seq: 0 } }],
    ['unknown', { ...running, integrity: { chain_verified: 'unknown' } }],
    ['unanchored', { ...golden('overview_unanchored') }],
    ['a missing integrity block', { ...running, integrity: undefined }],
  ])('chain %s with a run row saying ended: Stop stays enabled', async (_n, doc) => {
    await show({ ...doc, run: { ...doc.run, state: 'quiescent', active: false } }, 'cr-stop')
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it('the first poll still pending: Stop is offered as "run state unknown"', async () => {
    api.overview.mockReturnValue(new Promise(() => {}))
    render(<ControlRoom api={api} />)
    expect(await screen.findByTestId('cr-loading')).toBeInTheDocument()
    expect(screen.getByTestId('cr-stop-unknown')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it('a quiescent run: ended, Stop disabled', async () => {
    await show({ ...running, run: { ...running.run, state: 'quiescent', active: false, stopping: true } }, 'cr-stop')
    expect(screen.getByTestId('cr-run-state')).toHaveTextContent('Run ended')
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeDisabled()
  })
})

describe('integrity states from the contract', () => {
  const unanchored = golden('overview_unanchored')
  const behind = golden('overview_behind_witness')
  it.each([
    ['no_witness', 'no stop witness'],
    ['no_witness_record', 'no record'],
    ['witness_unreadable', 'could not be read'],
  ])('unanchored (%s): data shown under an amber banner, never the healthy look', async (reason, words) => {
    await show({ ...unanchored, integrity: { ...unanchored.integrity, reason } }, 'cr-unanchored')
    expect(screen.getByTestId('cr-unanchored')).toHaveTextContent('not anchored')
    expect(screen.getByTestId('cr-reason')).toHaveTextContent(words)
    expect(screen.getByTestId('cr-integrity')).toHaveAttribute('data-chain', 'unanchored')
    expect(screen.getByTestId('cr-integrity')).toHaveTextContent('NOT anchored')
    expect(screen.getByTestId('cr-integrity')).not.toHaveTextContent('verified')
    expect(screen.queryByTestId('cr-bad-chain')).toBeNull()
    expect(screen.getByTestId('cr-tree')).toBeInTheDocument()
    expect(screen.getByTestId('cr-spend')).toBeInTheDocument()
    expect(await screen.findAllByTestId('cr-feed-row')).toHaveLength(3)
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it('unanchored is not the healthy look: the verified badge and head never show', async () => {
    await show(unanchored, 'cr-unanchored')
    expect(screen.getByTestId('cr-integrity')).not.toHaveTextContent('#42')
    expect(screen.getByTestId('cr-integrity')).not.toHaveAttribute('data-chain', 'yes')
  })
  it.each([
    ['behind_witness', 'cut off'],
    ['witness_mismatch', 'disagree'],
  ])('no (%s): a red banner with the reason, no tree, spend or feed', async (reason, words) => {
    await show({ ...behind, integrity: { chain_verified: 'no', reason, head_seq: 0 } }, 'cr-bad-chain')
    expect(screen.getByTestId('cr-bad-chain')).toHaveAttribute('data-chain', 'no')
    expect(screen.getByTestId('cr-reason')).toHaveTextContent(words)
    expect(screen.queryByTestId('cr-unanchored')).toBeNull()
    for (const id of ['cr-tree', 'cr-spend', 'cr-feed']) expect(screen.queryByTestId(id)).toBeNull()
    expect(api.feed).not.toHaveBeenCalled()
  })
  it('no with no run row (forged or deleted): banner, header without a run, Stop stays', async () => {
    await show(golden('overview_run_row_forged'), 'cr-bad-chain')
    expect(screen.getByTestId('cr-integrity')).toHaveAttribute('data-chain', 'no')
    expect(screen.queryByTestId('cr-run-state')).toBeNull()
    expect(screen.getByTestId('cr-stop')).toBeInTheDocument()
  })
  it('an unknown reason still gets a plain sentence', async () => {
    await show({ ...behind, integrity: { chain_verified: 'no', reason: 'something_new', head_seq: 0 } }, 'cr-bad-chain')
    expect(screen.getByTestId('cr-reason')).toHaveTextContent('could not be confirmed')
  })
})

describe('a failed read is an error, not off', () => {
  it.each([
    ['db_unavailable', 'database could not be opened'],
    ['read_timeout', 'took too long'],
    ['read_failed', 'Reading the run failed.'],
    ['brand_new_code', 'reason this version does not know'],
  ])('error_code %s is said in plain words', async (code, words) => {
    await show({ ...golden('overview_error'), error_code: code }, 'cr-error')
    expect(screen.getByTestId('cr-error')).toHaveAttribute('data-error-code', code === 'brand_new_code' ? 'unknown' : code)
    expect(screen.getByTestId('cr-error')).toHaveTextContent(words)
    expect(screen.queryByTestId('cr-off')).toBeNull()
    expect(screen.queryByTestId('cr-no-run')).toBeNull()
    for (const id of ['cr-tree', 'cr-spend', 'cr-feed']) expect(screen.queryByTestId(id)).toBeNull()
  })
  it('after a good poll, an error document keeps Stop, and the run stays the last known one', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      api.overview.mockResolvedValue(running)
      render(<ControlRoom api={api} />)
      await screen.findByTestId('cr-stop')
      api.overview.mockResolvedValue(golden('overview_error'))
      await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
      expect(screen.getByTestId('cr-error')).toHaveAttribute('data-error-code', 'db_unavailable')
      expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
    } finally { vi.useRealTimers() }
  })
})

describe('Stop survives a failed poll', () => {
  afterEach(() => vi.useRealTimers())
  const laterPoll = async (doc) => {
    api.overview.mockResolvedValue(doc)
    await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
  }
  const firstOk = async (doc = running) => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    api.overview.mockResolvedValue(doc)
    render(<ControlRoom api={api} />)
    await screen.findByTestId('cr-stop')
  }
  it('a good poll then an error: the error banner shows and Stop is still there', async () => {
    await firstOk()
    await laterPoll({ error: 'database is locked' })
    expect(screen.getByTestId('cr-error')).toHaveTextContent('database is locked')
    expect(screen.getByTestId('cr-stop')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it('an unreadable document (unknown schema) after a good poll keeps Stop', async () => {
    await firstOk()
    await laterPoll({ schema_version: 99, available: true })
    expect(screen.getByTestId('cr-error')).toBeInTheDocument()
    expect(screen.getByTestId('cr-stop')).toBeInTheDocument()
  })
  it('the last known ended run keeps Stop enabled under an error (a stale row must not lock the operator out)', async () => {
    await firstOk({ ...running, run: { ...running.run, state: 'quiescent', active: false } })
    // findByTestId can resolve on the first render (the pre-poll error view, where Stop is rightly enabled); wait for
    // the first good poll to apply. A disabled Stop needs a poll with a verified chain that says the run is not active.
    await waitFor(() => expect(screen.getByRole('button', { name: /Stop run/ })).toBeDisabled())
    await laterPoll({ error: 'x' })
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeEnabled()
  })
  it('a bad chain keeps Stop, and an error after it too', async () => {
    await firstOk(unverified)
    expect(screen.getByTestId('cr-bad-chain')).toBeInTheDocument()
    await laterPoll({ error: 'x' })
    expect(screen.getByTestId('cr-stop')).toBeInTheDocument()
  })
  it('after a good poll that says no run, Stop is gone; an error after that offers it again as "state unknown"', async () => {
    await firstOk()
    await laterPoll(golden('overview_no_run'))
    expect(screen.queryByTestId('cr-stop')).toBeNull()
    api.overview.mockResolvedValue({ error: 'x' })
    await act(async () => { await vi.advanceTimersByTimeAsync(16000) }) // an idle page polls every 15 s
    expect(screen.getByTestId('cr-error')).toBeInTheDocument()
    expect(screen.getByTestId('cr-stop-unknown')).toBeInTheDocument()
  })
})

describe('the feed', () => {
  it('shows class, amounts, refs and what was exceeded, and no text of an answer', async () => {
    await show(running, 'cr-tree')
    const rows = await screen.findAllByTestId('cr-feed-row')
    expect(rows[0]).toHaveAttribute('data-class', 'ask_answer')
    expect(rows[0]).toHaveTextContent('ask answered')
    expect(rows[0]).toHaveTextContent('$0.0003')
    expect(rows[0]).toHaveTextContent('ap-9f')
    expect(rows[1]).toHaveTextContent('parked')
    expect(rows[1]).toHaveTextContent('grant_max_per_grant_micro')
    expect(rows[2]).toHaveAttribute('data-class', 'other')
  })
  it('escapes and bounds what the CLI sends', async () => {
    api.feed.mockResolvedValue({ ...page, next_before: 0, items: [{ seq: 5, kind: 'x', class: '<img src=x onerror=alert(1)>', op: '<b>' + 'a'.repeat(500), ref: '<script>' }] })
    await show(running, 'cr-tree')
    const row = (await screen.findAllByTestId('cr-feed-row'))[0]
    expect(row).toHaveAttribute('data-class', 'other')
    expect(row.querySelector('img,b,script')).toBeNull()
    expect(row.textContent.length).toBeLessThan(300)
  })
  it('loads older rows from next_before', async () => {
    await show(running, 'cr-tree')
    await screen.findAllByTestId('cr-feed-row')
    api.feed.mockResolvedValueOnce({ ...page, items: [{ seq: 39, kind: 'k', class: 'run' }], next_before: 0 })
    fireEvent.click(screen.getByRole('button', { name: 'Older decisions' }))
    await waitFor(() => expect(screen.getAllByTestId('cr-feed-row')).toHaveLength(4))
    expect(api.feed).toHaveBeenLastCalledWith(40, 50)
  })
})

describe('tree and spend components', () => {
  it('clusters, not members: 5000 members are a bounded DOM', () => {
    const clusters = Array.from({ length: 200 }, (_, i) => ({ kind: 'k' + i, total: 25, live: 5 }))
    render(<TreeClusters tree={{ total: 5000, live: 1000, clusters, truncated: true }} />)
    expect(screen.getAllByTestId('cr-cluster').length).toBeLessThanOrEqual(20)
    expect(screen.getByTestId('cr-tree')).toHaveTextContent('1000 live of 5000')
    expect(screen.getByTestId('cr-tree')).toHaveTextContent('Some kinds are not shown')
  })
  it('the counters are labelled as not authenticated, whatever the badge says', () => {
    const { unmount } = render(<SpendMeter spend={{ ...running.spend, counters_authenticated: false }} rate={null} />)
    expect(screen.getByTestId('cr-counters-note')).toHaveTextContent('not authenticated')
    unmount()
    render(<SpendMeter spend={{ ...running.spend }} rate={null} />)
    expect(screen.getByTestId('cr-counters-note')).toBeInTheDocument()
  })
  it('spend: ok, near, over, no ceiling, no budget', () => {
    const cases = [
      [{ cap_micro: 100, used_micro: 10 }, 'ok'],
      [{ cap_micro: 100, used_micro: 85 }, 'near'],
      [{ cap_micro: 100, used_micro: 120 }, 'over'],
      [{ cap_micro: null, no_ceiling: true, used_micro: 5 }, 'no_ceiling'],
      [{ cap_micro: null, no_ceiling: false, used_micro: 5 }, 'no_budget'],
    ]
    for (const [spend, kind] of cases) {
      const { unmount } = render(<SpendMeter spend={spend} rate={null} />)
      expect(screen.getByTestId('cr-spend')).toHaveAttribute('data-spend', kind)
      if (kind === 'no_ceiling') {
        expect(screen.getByTestId('cr-spend')).toHaveTextContent('no spend ceiling')
        expect(screen.queryByRole('meter')).toBeNull()
      }
      unmount()
    }
  })
  it('the rate is labelled approx, or absent', () => {
    const { rerender } = render(<SpendMeter spend={{ cap_micro: 100, used_micro: 1 }} rate={2_000_000} />)
    expect(screen.getByTestId('cr-rate')).toHaveTextContent('About $2.00 per hour (approx')
    rerender(<SpendMeter spend={{ cap_micro: 100, used_micro: 1 }} rate={null} />)
    expect(screen.getByTestId('cr-rate')).toHaveTextContent('not enough readings')
    expect(screen.getByTestId('cr-rate')).toHaveTextContent('approx')
  })
  it('the meter is accessible', () => {
    render(<SpendMeter spend={{ cap_micro: 200, used_micro: 50 }} rate={null} />)
    expect(screen.getByRole('meter')).toHaveAttribute('aria-valuenow', '25')
  })
})

describe('the Stop panel', () => {
  it('needs click then confirm, and calls stop once, with no argument', async () => {
    render(<StopPanel api={api} />)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    expect(api.stop).not.toHaveBeenCalled()
    const dlg = screen.getByRole('alertdialog')
    fireEvent.click(within(dlg).getByRole('button', { name: /Yes, stop/ }))
    await screen.findByTestId('cr-stop-result')
    expect(api.stop).toHaveBeenCalledTimes(1)
    expect(api.stop).toHaveBeenCalledWith()
    expect(screen.getByTestId('cr-stop-result')).toHaveAttribute('data-ok', 'yes')
    expect(screen.getByTestId('cr-stop-result')).toHaveTextContent('stopped')
  })
  it('cancel calls nothing', () => {
    render(<StopPanel api={api} />)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(api.stop).not.toHaveBeenCalled()
  })
  it('stopping: disabled while the call is in flight, so a double click cannot stop twice', async () => {
    let finish
    api.stop.mockReturnValue(new Promise(r => { finish = r }))
    render(<StopPanel api={api} />)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: /Yes, stop/ }))
    const btn = await screen.findByRole('button', { name: /Stopping/ })
    expect(btn).toBeDisabled()
    fireEvent.click(btn)
    finish({ ok: true, stopped: true, output: 'stopped' })
    await screen.findByTestId('cr-stop-result')
    expect(api.stop).toHaveBeenCalledTimes(1)
  })
  it('shows NOT stopped and the survivors verbatim', async () => {
    api.stop.mockResolvedValue({ ok: false, stopped: false, output: 'NOT stopped: 2 process(es) survive: 11, 12', detail: 'exit status 1', code: '' })
    render(<StopPanel api={api} />)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: /Yes, stop/ }))
    const res = await screen.findByTestId('cr-stop-result')
    expect(res).toHaveAttribute('data-ok', 'no')
    expect(res).toHaveAttribute('role', 'alert')
    expect(res).toHaveTextContent('NOT confirmed stopped')
    expect(res).toHaveTextContent('NOT stopped: 2 process(es) survive: 11, 12')
  })
  it('shows the CLI refusal (operator_only) and an error from the binding', async () => {
    api.stop.mockResolvedValue({ ok: false, stopped: false, output: '', detail: 'operator only', code: 'operator_only' })
    render(<StopPanel api={api} />)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: /Yes, stop/ }))
    expect(await screen.findByTestId('cr-stop-result')).toHaveTextContent('operator only')
    cleanup()
    api.stop.mockResolvedValue({ error: 'boom' })
    render(<StopPanel api={api} />)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: /Yes, stop/ }))
    const res = await screen.findByTestId('cr-stop-result')
    expect(res).toHaveAttribute('data-ok', 'no')
    expect(res).toHaveTextContent('boom')
  })
  it('a page Stop refreshes the overview once', async () => {
    await show(running, 'cr-stop')
    await screen.findAllByTestId('cr-feed-row')
    const before = api.overview.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: /Yes, stop/ }))
    await screen.findByTestId('cr-stop-result')
    await waitFor(() => expect(api.overview.mock.calls.length).toBeGreaterThan(before))
  })
})

describe('what the page cannot do', () => {
  it('has no start, approve, pause or resume control', async () => {
    await show(running, 'cr-tree')
    const names = screen.getAllByRole('button').map(b => b.textContent.toLowerCase())
    for (const bad of ['start', 'approve', 'pause', 'resume', 'grant', 'lift']) expect(names.filter(n => n.includes(bad))).toEqual([])
  })
})
