// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, act } from '@testing-library/react'

vi.mock('../../services/api.js', () => ({ api: { openURL: vi.fn() } }))
import { api } from '../../services/api.js'

import { ChatTimeline } from './ChatTimeline.jsx'
import { ChatMarkdown, isAllowedURL } from './ChatMarkdown.jsx'
import { ToolActivityCard } from './ToolActivityCard.jsx'
import { computeStatusLabel, TurnStatus } from './TurnStatus.jsx'
import { chatReducer, initialChatState } from './chatReducer.js'

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function ev(type, payload, seq, at) {
  return { version: 1, profileId: 'default', conversationId: 'c1', turnId: 't1', seq, at: at || `2026-09-12T00:00:0${seq}.000Z`, type, payload }
}

function reduce(events) {
  return events.reduce((s, e) => chatReducer(s, { type: 'event', event: e }), initialChatState())
}

// ── ChatMarkdown ─────────────────────────────────────────────────────────────

describe('ChatMarkdown', () => {
  it('renders formatted markdown with a real link', () => {
    render(<ChatMarkdown content={'# Summary\n\nSee [the report](https://example.com/report) for **details**.'} />)
    expect(screen.getByRole('heading', { name: 'Summary' })).toBeInTheDocument()
    expect(screen.getByText('details').tagName).toBe('STRONG')
  })

  it('keeps raw embedded HTML as literal text, never injected as DOM', () => {
    render(<ChatMarkdown content={'before <img src=x onerror="window.__pwned=true"> after'} />)
    expect(window.__pwned).toBeUndefined()
    expect(screen.getByText(/before/)).toBeInTheDocument()
  })

  it('allows http/https links through the scheme-gated openURL path on click', () => {
    render(<ChatMarkdown content={'[go](https://example.com/x)'} />)
    fireEvent.click(screen.getByRole('link', { name: 'go' }))
    expect(api.openURL).toHaveBeenCalledWith('https://example.com/x')
  })

  it('blocks a javascript: link instead of navigating or calling openURL', () => {
    render(<ChatMarkdown content={'[go](javascript:alert(1))'} />)
    expect(screen.queryByRole('link', { name: 'go' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('go'))
    expect(api.openURL).not.toHaveBeenCalled()
  })

  it('blocks a schemeless/relative link — it must not resolve to an allowed scheme it never had', () => {
    // Regression: isAllowedURL used to resolve href against a placeholder
    // base (`new URL(href, 'https://chat.invalid/')`), so a schemeless
    // string like this one resolved to "https:" and passed the check, while
    // the ORIGINAL unresolved string (not the resolved one) was still what
    // got passed to openURL/rendered as the real href — validating one
    // value and using a different one.
    render(<ChatMarkdown content={'[go](../../secret.txt)'} />)
    expect(screen.queryByRole('link', { name: 'go' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('go'))
    expect(api.openURL).not.toHaveBeenCalled()
  })

  it('blocks a protocol-relative link the same way', () => {
    render(<ChatMarkdown content={'[go](//evil.example/payload)'} />)
    expect(screen.queryByRole('link', { name: 'go' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('go'))
    expect(api.openURL).not.toHaveBeenCalled()
  })

  it('isAllowedURL accepts only http/https and rejects everything else', () => {
    expect(isAllowedURL('https://example.com')).toBe(true)
    expect(isAllowedURL('http://example.com')).toBe(true)
    expect(isAllowedURL('mailto:a@b.com')).toBe(false)
    expect(isAllowedURL('javascript:alert(1)')).toBe(false)
    expect(isAllowedURL('data:text/html,<script>1</script>')).toBe(false)
    expect(isAllowedURL('file:///etc/passwd')).toBe(false)
    expect(isAllowedURL('')).toBe(false)
    expect(isAllowedURL(undefined)).toBe(false)
  })

  it('isAllowedURL rejects schemeless, relative, and protocol-relative strings instead of inheriting a placeholder scheme', () => {
    expect(isAllowedURL('../../secret.txt')).toBe(false)
    expect(isAllowedURL('foo/bar')).toBe(false)
    expect(isAllowedURL('Applications/Terminal.app')).toBe(false)
    expect(isAllowedURL('~/.ssh/id_rsa')).toBe(false)
    expect(isAllowedURL('//evil.example/payload')).toBe(false)
  })

  it('does not auto-load a remote image — renders a blocked placeholder, never an <img> tag', () => {
    render(<ChatMarkdown content={'![diagram](https://evil.example.com/tracker.png)'} />)
    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByText('diagram')).toBeInTheDocument()
  })

  it('renders an oversized single code line without crashing or truncating content', () => {
    const huge = 'x'.repeat(20000)
    render(<ChatMarkdown content={'```\n' + huge + '\n```'} />)
    expect(screen.getByText(huge)).toBeInTheDocument()
  })

  it('renders malformed/unterminated markdown without throwing', () => {
    expect(() => render(<ChatMarkdown content={'**unterminated bold [link(broken `code'} />)).not.toThrow()
  })

  it('renders empty content as nothing, not a crash', () => {
    expect(() => render(<ChatMarkdown content="" />)).not.toThrow()
  })
})

// ── ToolActivityCard ─────────────────────────────────────────────────────────

describe('ToolActivityCard', () => {
  it('shows a running status while the call has not completed', () => {
    render(<ToolActivityCard call={{ callId: 'c1', name: 'search_docs', arguments: { q: 'x' }, status: 'started', ok: null, result: null }} />)
    expect(screen.getByText(/search_docs/)).toBeInTheDocument()
    expect(screen.getByText(/Running/i)).toBeInTheDocument()
  })

  it('exposes a real, natively keyboard-operable button (native Enter/Space activate it — no custom key handling needed) that toggles details on click', () => {
    render(<ToolActivityCard call={{ callId: 'c1', name: 'search_docs', arguments: { q: 'x' }, status: 'completed', ok: true, result: 'found 3' }} />)
    const header = screen.getByRole('button', { name: /search_docs/ })
    expect(header.tagName).toBe('BUTTON') // real <button>: Enter/Space activation is native, not reimplemented
    expect(header).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(header)
    expect(header).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('found 3')).toBeInTheDocument()
  })

  it('distinguishes an explicit empty-string result from no result yet', () => {
    render(<ToolActivityCard call={{ callId: 'c1', name: 'noop', arguments: null, status: 'completed', ok: true, result: '' }} />)
    fireEvent.click(screen.getByRole('button', { name: /noop/ }))
    expect(screen.getByText(/empty/i)).toBeInTheDocument()
  })

  it('shows a failed status distinctly when ok is explicitly false', () => {
    render(<ToolActivityCard call={{ callId: 'c1', name: 'run_action', arguments: {}, status: 'completed', ok: false, result: 'boom' }} />)
    expect(screen.getByText(/Failed/i)).toBeInTheDocument()
  })

  it('never collapses/hides an errored call — details default open on failure', () => {
    render(<ToolActivityCard call={{ callId: 'c1', name: 'run_action', arguments: {}, status: 'completed', ok: false, result: 'boom' }} />)
    expect(screen.getByText('boom')).toBeInTheDocument()
  })

  it('shows a static duration once completed, computed from startedAt/finishedAt', () => {
    render(<ToolActivityCard call={{ callId: 'c1', name: 'search_docs', arguments: {}, status: 'completed', ok: true, result: 'ok', startedAt: '2026-09-12T00:00:00.000Z', finishedAt: '2026-09-12T00:00:01.500Z' }} />)
    expect(screen.getByText(/1\.5s/)).toBeInTheDocument()
  })

  it('shows no duration when timestamps are missing — e.g. an unmatched completion with no observed start', () => {
    render(<ToolActivityCard call={{ callId: 'orphan', name: 'unknown', arguments: null, status: 'completed', ok: false, result: '' }} />)
    expect(screen.queryByText(/\ds\b/)).not.toBeInTheDocument()
  })

  it('shows a live-ticking elapsed time for a still-running call, advancing once a second', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-09-12T00:00:02.000Z'))
      render(<ToolActivityCard call={{ callId: 'c1', name: 'slow_tool', arguments: {}, status: 'started', ok: null, result: null, startedAt: '2026-09-12T00:00:00.000Z' }} />)
      expect(screen.getByText(/2\.0s/)).toBeInTheDocument()
      act(() => { vi.advanceTimersByTime(1000) })
      expect(screen.getByText(/3\.0s/)).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('a call still "started" when replayed from a finalized turn (isLive=false) shows Interrupted, not a perpetually-ticking Running clock', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-09-12T00:00:05.000Z'))
      render(<ToolActivityCard isLive={false} call={{ callId: 'c1', name: 'slow_tool', arguments: {}, status: 'started', ok: null, result: null, startedAt: '2026-09-12T00:00:00.000Z' }} />)
      expect(screen.getByText(/Interrupted/i)).toBeInTheDocument()
      expect(screen.queryByText(/Running/i)).not.toBeInTheDocument()
      expect(screen.queryByText(/\ds\b/)).not.toBeInTheDocument() // no bogus/frozen duration either
      act(() => { vi.advanceTimersByTime(5000) })
      expect(screen.queryByText(/\ds\b/)).not.toBeInTheDocument() // still nothing ticking after time passes
    } finally {
      vi.useRealTimers()
    }
  })

  it('the exact same started call still ticks normally when isLive=true (the still-streaming case is unaffected)', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-09-12T00:00:02.000Z'))
      render(<ToolActivityCard isLive={true} call={{ callId: 'c1', name: 'slow_tool', arguments: {}, status: 'started', ok: null, result: null, startedAt: '2026-09-12T00:00:00.000Z' }} />)
      expect(screen.getByText(/Running/i)).toBeInTheDocument()
      expect(screen.getByText(/2\.0s/)).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps the aria-controls target present in the DOM even while collapsed', () => {
    render(<ToolActivityCard turnId="t1" call={{ callId: 'c1', name: 'search_docs', arguments: {}, status: 'completed', ok: true, result: 'found 3' }} />)
    const header = screen.getByRole('button', { name: /search_docs/ })
    expect(header).toHaveAttribute('aria-expanded', 'false')
    const controlsId = header.getAttribute('aria-controls')
    expect(controlsId).toBeTruthy()
    expect(document.getElementById(controlsId)).not.toBeNull()
  })

  it('actually visually hides the controlled panel while collapsed — the always-mounted div must not carry an inline style that overrides `hidden`', () => {
    // Regression: the panel div was given both `hidden={!open}` AND an
    // inline `style={{ display: 'flex', ... }}` unconditionally. An inline
    // style always wins over the UA stylesheet's `[hidden]{display:none}`
    // rule, so the body rendered permanently visible regardless of the
    // toggle — collapsing/expanding the card looked like it did nothing.
    render(<ToolActivityCard turnId="t1" call={{ callId: 'c1', name: 'search_docs', arguments: {}, status: 'completed', ok: true, result: 'found 3' }} />)
    const header = screen.getByRole('button', { name: /search_docs/ })
    const controlsId = header.getAttribute('aria-controls')
    const panel = document.getElementById(controlsId)
    expect(panel).not.toBeVisible()
    expect(panel.style.display).not.toBe('flex')

    fireEvent.click(header)
    expect(panel).toBeVisible()
    expect(panel.style.display).toBe('flex')

    fireEvent.click(header)
    expect(panel).not.toBeVisible()
  })

  it('scopes the controlled panel id by turnId, so two turns reusing the same callId never collide', () => {
    render(<>
      <ToolActivityCard turnId="turn-1" call={{ callId: 'c1', name: 'a', arguments: {}, status: 'completed', ok: true, result: 'x' }} />
      <ToolActivityCard turnId="turn-2" call={{ callId: 'c1', name: 'b', arguments: {}, status: 'completed', ok: true, result: 'y' }} />
    </>)
    const [headerA, headerB] = screen.getAllByRole('button')
    expect(headerA.getAttribute('aria-controls')).not.toBe(headerB.getAttribute('aria-controls'))
  })
})

// ── TurnStatus ───────────────────────────────────────────────────────────────

describe('computeStatusLabel', () => {
  const base = { terminal: null, stopRequested: false, startedAt: null, lastEventAt: null, parts: [], calls: {} }

  it('reports "Starting agent" before any event has landed', () => {
    expect(computeStatusLabel(base, 0).label).toBe('Starting agent')
  })

  it('reports "Waiting for response" once started but before any text/tool', () => {
    expect(computeStatusLabel({ ...base, startedAt: '2026-09-12T00:00:00.000Z' }, 0).label).toBe('Waiting for response')
  })

  it('reports "Running <tool>" while a tool call is in flight', () => {
    const state = { ...base, startedAt: 't', calls: { c1: { callId: 'c1', name: 'get_workflow', status: 'started' } } }
    expect(computeStatusLabel(state, 0).label).toBe('Running get_workflow')
  })

  it('reports "Responding" once assistant text has started arriving', () => {
    const state = { ...base, startedAt: 't', parts: [{ kind: 'text', partId: 'p1', text: 'hi' }] }
    expect(computeStatusLabel(state, 0).label).toBe('Responding')
  })

  it('reports "Stopping" once a stop was requested but not yet acknowledged', () => {
    expect(computeStatusLabel({ ...base, stopRequested: true }, 0).label).toBe('Stopping')
  })

  it('maps each terminal status to its honest label, never a bare success claim for a limit', () => {
    expect(computeStatusLabel({ ...base, terminal: { status: 'completed', reason: 'end_turn' } }, 0).label).toBe('Completed')
    expect(computeStatusLabel({ ...base, terminal: { status: 'completed', reason: 'limit reached (max_turns)' } }, 0).label).toBe('Limit reached')
    expect(computeStatusLabel({ ...base, terminal: { status: 'failed', reason: 'boom' } }, 0).label).toBe('Failed')
    expect(computeStatusLabel({ ...base, terminal: { status: 'cancelled', reason: 'stopped' } }, 0).label).toBe('Stopped')
    expect(computeStatusLabel({ ...base, terminal: { status: 'interrupted', reason: '' } }, 0).label).toBe('Interrupted')
  })

  it('flags idle warning only after 15s with no new event, and never once terminal', () => {
    const startedAt = '2026-09-12T00:00:00.000Z'
    const state = { ...base, startedAt, lastEventAt: startedAt }
    const t0 = new Date(startedAt).getTime()
    expect(computeStatusLabel(state, t0 + 5000).isIdleWarning).toBe(false)
    expect(computeStatusLabel(state, t0 + 15001).isIdleWarning).toBe(true)
    expect(computeStatusLabel({ ...state, terminal: { status: 'completed' } }, t0 + 60000).isIdleWarning).toBe(false)
  })

  // Cross-instance ownership (GetChatTurns' ownedByThisInstance — see
  // docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md,
  // "OwnerInstanceID is written and read back but never compared to
  // anything"). ownedByThisInstance is merged into the state object the
  // same way TurnStatus's own stopRequested prop is (see its call site) —
  // it is not part of chatReducer's own event-replay state.
  it('reports "Running in another window" for an active turn explicitly owned by a different instance, overriding any in-progress signal', () => {
    const state = { ...base, startedAt: 't', ownedByThisInstance: false, calls: { c1: { callId: 'c1', name: 'get_workflow', status: 'started' } } }
    expect(computeStatusLabel(state, 0)).toEqual({ label: 'Running in another window', isIdleWarning: false })
  })

  // streamsIncrementally (agent-exec.ts protocol rev 5, runner-registry.ts's
  // RunnerSpec.streamsIncrementally): most runtimes only ever yield a
  // complete message at a step/turn boundary, never partial text as it
  // generates. Waiting silently through that with the same "Waiting for
  // response" + "No new activity for 15s" wording used for a streaming
  // runtime implies something might be stuck when nothing is wrong — the
  // turn was never going to show partial output. Merged into state the
  // same way ownedByThisInstance/stopRequested are (see TurnStatus's call
  // site), not part of chatReducer's own replay state.
  it('reports a distinct waiting label for a non-streaming runtime, instead of the generic "Waiting for response"', () => {
    const state = { ...base, startedAt: 't', streamsIncrementally: false }
    expect(computeStatusLabel(state, 0).label).not.toBe('Waiting for response')
    expect(computeStatusLabel(state, 0).label).toMatch(/full reply/i)
  })

  it('suppresses the idle-activity warning for a non-streaming runtime while waiting — silence there is normal, not evidence of a hang', () => {
    const startedAt = '2026-09-12T00:00:00.000Z'
    const state = { ...base, startedAt, lastEventAt: startedAt, streamsIncrementally: false }
    const t0 = new Date(startedAt).getTime()
    expect(computeStatusLabel(state, t0 + 15001).isIdleWarning).toBe(false)
    expect(computeStatusLabel(state, t0 + 120000).isIdleWarning).toBe(false)
  })

  it('defaults to today\'s streaming-runtime behavior when streamsIncrementally is omitted (older data, existing callers, existing tests)', () => {
    const state = { ...base, startedAt: 't' }
    expect(computeStatusLabel(state, 0).label).toBe('Waiting for response')
  })

  it('once real activity arrives, a non-streaming runtime uses the same "Running <tool>"/"Responding" labels as any other turn', () => {
    const runningState = { ...base, startedAt: 't', streamsIncrementally: false, calls: { c1: { callId: 'c1', name: 'get_workflow', status: 'started' } } }
    expect(computeStatusLabel(runningState, 0).label).toBe('Running get_workflow')
    const respondingState = { ...base, startedAt: 't', streamsIncrementally: false, parts: [{ kind: 'text', partId: 'p1', text: 'hi' }] }
    expect(computeStatusLabel(respondingState, 0).label).toBe('Responding')
  })

  it('a terminal event always wins over ownedByThisInstance:false — a foreign turn actually observed to have finished shows its real terminal label', () => {
    const state = { ...base, ownedByThisInstance: false, terminal: { status: 'completed', reason: 'end_turn' } }
    expect(computeStatusLabel(state, 0).label).toBe('Completed')
  })

  it('treats ownedByThisInstance:true, or the field entirely absent, as normal — never shows the foreign-instance label', () => {
    expect(computeStatusLabel({ ...base, startedAt: 't', ownedByThisInstance: true }, 0).label).toBe('Waiting for response')
    expect(computeStatusLabel({ ...base, startedAt: 't' }, 0).label).toBe('Waiting for response') // field entirely absent
  })
})

describe('TurnStatus component', () => {
  it('renders the current label from reducer state', () => {
    const state = reduce([ev('turn.started', { backend: 'agent', text: 'hi' }, 1)])
    render(<TurnStatus state={state} stopRequested={false} />)
    expect(screen.getByText('Waiting for response')).toBeInTheDocument()
  })

  it('announces state in a polite live region for screen readers', () => {
    const state = reduce([ev('turn.started', { backend: 'agent', text: 'hi' }, 1)])
    render(<TurnStatus state={state} stopRequested={false} />)
    const region = screen.getByText('Waiting for response').closest('[aria-live]')
    expect(region).toHaveAttribute('aria-live', 'polite')
  })

  it('shows the "Stopping" label when the stopRequested PROP is true, even though reducer state never carries that field', () => {
    // Regression: computeStatusLabel reads stopRequested off the object
    // it's passed — the component used to call it with bare `state` alone,
    // so this prop (tracked separately by the chat view, never part of
    // chatReducer's own state shape) never reached it. Only the icon
    // swapped; the text label was stuck on whatever it would otherwise be.
    const state = reduce([ev('turn.started', { backend: 'agent', text: 'hi' }, 1)])
    render(<TurnStatus state={state} stopRequested={true} />)
    expect(screen.getByText('Stopping')).toBeInTheDocument()
    expect(screen.queryByText('Waiting for response')).not.toBeInTheDocument()
  })

  // Cross-instance ownership label (the chat view's transcript loader
  // threads GetChatTurns' ownedByThisInstance through as its own prop, the
  // same way it already does for stopRequested — see the regression test
  // above for why that field can't live inside chatReducer's own state).
  it('renders the static foreign-instance label with no live spinner when ownedByThisInstance is false on an active (non-terminal) turn', () => {
    const state = reduce([
      ev('turn.started', { backend: 'agent', text: 'hi' }, 1),
      ev('tool.started', { callId: 'c1', name: 'slow_tool', arguments: {} }, 2),
    ])
    const { container } = render(<TurnStatus state={state} stopRequested={false} ownedByThisInstance={false} />)
    expect(screen.getByText('Running in another window')).toBeInTheDocument()
    expect(screen.queryByText(/Running slow_tool/)).not.toBeInTheDocument()
    // No live spinner (the Loader icon TurnStatus otherwise shows for any
    // non-terminal turn) — a foreign-owned turn gets no liveness indicator
    // this instance cannot actually back up.
    expect(container.querySelector('.chat-spin')).not.toBeInTheDocument()
  })

  it('does not tick — label and idle-warning stay identical over time for a foreign-owned active turn', () => {
    vi.useFakeTimers()
    try {
      const state = reduce([ev('turn.started', { backend: 'agent', text: 'hi' }, 1)])
      render(<TurnStatus state={state} stopRequested={false} ownedByThisInstance={false} />)
      expect(screen.getByText('Running in another window')).toBeInTheDocument()
      act(() => { vi.advanceTimersByTime(20000) }) // past the normal 15s idle-warning threshold
      expect(screen.getByText('Running in another window')).toBeInTheDocument()
      expect(screen.queryByText(/No new activity/)).not.toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('regression: defaults ownedByThisInstance to true when the prop is omitted entirely, so every existing caller is unaffected', () => {
    const state = reduce([ev('turn.started', { backend: 'agent', text: 'hi' }, 1)])
    const { container } = render(<TurnStatus state={state} stopRequested={false} />)
    expect(screen.getByText('Waiting for response')).toBeInTheDocument()
    expect(container.querySelector('.chat-spin')).toBeInTheDocument() // normal live spinner still shows
  })
})

// ── ChatTimeline (fixture: user request → final result, Task 4 gate) ────────

describe('ChatTimeline', () => {
  it('follows a full turn from request to result without losing ordering, errors or partial work', () => {
    const state = reduce([
      ev('turn.started', { backend: 'agent', text: 'add validation' }, 1),
      ev('assistant.delta', { partId: 'p1', text: "I'll check the workflow." }, 2),
      ev('tool.started', { callId: 'c1', name: 'get_workflow', arguments: { id: 'wf1' } }, 3),
      ev('tool.completed', { callId: 'c1', ok: true, result: '{"fields":3}' }, 4),
      ev('notice', { code: 'stale-stop', message: 'a stray stop arrived late', severity: 'warning' }, 5),
      ev('assistant.delta', { partId: 'p2', text: 'Added validation for required fields.' }, 6),
      ev('turn.finished', { status: 'completed', reason: 'end_turn', exitCode: 0, historySaved: true }, 7),
    ])
    render(<ChatTimeline state={state} />)

    const container = screen.getByTestId('chat-timeline')
    const text = container.textContent
    // Ordering: first text block precedes the tool card, which precedes the
    // second text block — never collapsed into one bubble, order preserved.
    expect(text.indexOf("I'll check the workflow")).toBeLessThan(text.indexOf('get_workflow'))
    expect(text.indexOf('get_workflow')).toBeLessThan(text.indexOf('Added validation'))
    // Nonfatal notice is visible but did not erase surrounding work.
    expect(screen.getByText(/stray stop arrived late/)).toBeInTheDocument()
    expect(screen.getByText("I'll check the workflow.")).toBeInTheDocument()
    expect(screen.getByText('Added validation for required fields.')).toBeInTheDocument()
  })

  it('preserves partial/interrupted work instead of erasing it', () => {
    const state = reduce([
      ev('turn.started', { backend: 'agent', text: 'go' }, 1),
      ev('assistant.delta', { partId: 'p1', text: 'partial answer' }, 2),
      ev('tool.started', { callId: 'c1', name: 'slow_tool', arguments: {} }, 3),
      ev('turn.finished', { status: 'cancelled', reason: 'stopped', exitCode: null, historySaved: true }, 4),
    ])
    render(<ChatTimeline state={state} />)
    expect(screen.getByText('partial answer')).toBeInTheDocument()
    expect(screen.getByText(/slow_tool/)).toBeInTheDocument()
  })

  it('renders a tool-only response (no assistant text) as real timeline content', () => {
    const state = reduce([
      ev('turn.started', { backend: 'agent', text: 'go' }, 1),
      ev('tool.started', { callId: 'c1', name: 'create_workflow', arguments: {} }, 2),
      ev('tool.completed', { callId: 'c1', ok: true, result: '{"workflow_id":"wf1"}' }, 3),
      ev('turn.finished', { status: 'completed', reason: 'end_turn', exitCode: 0, historySaved: true }, 4),
    ])
    render(<ChatTimeline state={state} />)
    expect(screen.getByText(/create_workflow/)).toBeInTheDocument()
  })

  it('threads turnId and isLive down to its tool cards — a replayed turn with an orphaned call shows Interrupted', () => {
    const state = reduce([
      ev('turn.started', { backend: 'agent', text: 'go' }, 1),
      ev('tool.started', { callId: 'c1', name: 'slow_tool', arguments: {} }, 2),
      // No tool.completed — this call is orphaned (e.g. Stop mid-call).
    ])
    render(<ChatTimeline state={state} turnId="turn-42" isLive={false} />)
    expect(screen.getByText(/Interrupted/i)).toBeInTheDocument()
    const header = screen.getByRole('button', { name: /slow_tool/ })
    expect(header.getAttribute('aria-controls')).toContain('turn-42')
  })

  it('defaults to isLive=true when not passed, so an actively-streaming turn (the common call site) still shows Running', () => {
    const state = reduce([
      ev('turn.started', { backend: 'agent', text: 'go' }, 1),
      ev('tool.started', { callId: 'c1', name: 'slow_tool', arguments: {} }, 2),
    ])
    render(<ChatTimeline state={state} />)
    expect(screen.getByText(/Running/i)).toBeInTheDocument()
  })
})

// ── Provider rate limits (agent-exec rev 20) ─────────────────────────────────

describe('ChatTimeline rate-limit notices', () => {
  it('keeps each retry and the final "Rate limited by …" message after tools ran and the turn failed', () => {
    const final = 'Rate limited by openrouter/x:free (429) after 3 attempts. Free models are rate-limited; try again later or pick another model.'
    const state = reduce([
      ev('turn.started', { runtime: 'pi' }, 1),
      ev('assistant.delta', { partId: 'part-1', text: 'Working on it' }, 2),
      ev('notice', { code: 'agent.rate_limit_retry', message: 'Rate limited (429) by openrouter/x:free; retrying in 2s (attempt 2/3)', severity: 'warning' }, 3),
      ev('notice', { code: 'agent.rate_limit_retry', message: 'Rate limited (429) by openrouter/x:free; retrying in 5s (attempt 3/3)', severity: 'warning' }, 4),
      ev('notice', { code: 'agent.rate_limited', message: final, severity: 'error' }, 5),
      ev('turn.finished', { status: 'failed', reason: `rate-limited: ${final}`, exitCode: 1, historySaved: true }, 6),
    ])
    render(<ChatTimeline state={state} turnId="t1" isLive={false} />)
    expect(screen.getByText(/retrying in 2s \(attempt 2\/3\)/)).toBeInTheDocument()
    expect(screen.getByText(/retrying in 5s \(attempt 3\/3\)/)).toBeInTheDocument()
    expect(screen.getByText(final)).toBeInTheDocument()
    render(<TurnStatus state={state} stopRequested={false} />)
    expect(screen.getByRole('status')).toHaveTextContent('Failed')
  })
})
