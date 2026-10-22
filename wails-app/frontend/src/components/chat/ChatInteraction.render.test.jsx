// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, waitFor, fireEvent, act } from '@testing-library/react'
import { ChatComposer } from './ChatComposer.jsx'
import AIChatPanel from '../AIChatPanel.jsx'

// jsdom implements neither — AIChatPanel's scroll handling touches both.
Element.prototype.scrollIntoView = Element.prototype.scrollIntoView || (() => {})
Element.prototype.scrollTo = Element.prototype.scrollTo || (() => {})

const originalInnerWidth = window.innerWidth

afterEach(() => {
  cleanup()
  // The narrow-viewport Escape test mutates window.innerWidth — restore it
  // so later tests in this file don't mount AIChatPanel already believing
  // the viewport is narrow (no resize handle, no expand button).
  window.innerWidth = originalInnerWidth
})

// ── ChatComposer: IME-safe Enter ─────────────────────────────────────────────

describe('ChatComposer', () => {
  it('sends on a plain Enter', () => {
    const onSend = vi.fn()
    render(<ChatComposer value="hi" onChange={() => {}} onSend={onSend} onStop={() => {}} streaming={false} disabled={false} />)
    fireEvent.keyDown(screen.getByPlaceholderText('Type a message...'), { key: 'Enter' })
    expect(onSend).toHaveBeenCalledTimes(1)
  })

  it('never sends while an IME composition is in progress', () => {
    const onSend = vi.fn()
    render(<ChatComposer value="日本語" onChange={() => {}} onSend={onSend} onStop={() => {}} streaming={false} disabled={false} />)
    const textarea = screen.getByPlaceholderText('Type a message...')
    fireEvent.compositionStart(textarea)
    fireEvent.keyDown(textarea, { key: 'Enter', isComposing: true })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('catches the stray keyCode-229 Enter some browsers fire right as composition ends', () => {
    const onSend = vi.fn()
    render(<ChatComposer value="hi" onChange={() => {}} onSend={onSend} onStop={() => {}} streaming={false} disabled={false} />)
    const textarea = screen.getByPlaceholderText('Type a message...')
    fireEvent.compositionStart(textarea)
    fireEvent.compositionEnd(textarea)
    fireEvent.keyDown(textarea, { key: 'Enter', keyCode: 229 })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('sends normally once composition has actually ended', () => {
    const onSend = vi.fn()
    render(<ChatComposer value="hi" onChange={() => {}} onSend={onSend} onStop={() => {}} streaming={false} disabled={false} />)
    const textarea = screen.getByPlaceholderText('Type a message...')
    fireEvent.compositionStart(textarea)
    fireEvent.compositionEnd(textarea)
    fireEvent.keyDown(textarea, { key: 'Enter' })
    expect(onSend).toHaveBeenCalledTimes(1)
  })

  it('never sends on Shift+Enter — inserts a newline instead', () => {
    const onSend = vi.fn()
    render(<ChatComposer value="hi" onChange={() => {}} onSend={onSend} onStop={() => {}} streaming={false} disabled={false} />)
    fireEvent.keyDown(screen.getByPlaceholderText('Type a message...'), { key: 'Enter', shiftKey: true })
    expect(onSend).not.toHaveBeenCalled()
  })

  it('shows Stop instead of Send while streaming, and it stays clickable', () => {
    const onStop = vi.fn()
    render(<ChatComposer value="" onChange={() => {}} onSend={() => {}} onStop={onStop} streaming={true} disabled={false} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stop generating' }))
    expect(onStop).toHaveBeenCalledTimes(1)
  })

  it('disables Send with an explicit reason when no backend is selected', () => {
    render(<ChatComposer value="hi" onChange={() => {}} onSend={() => {}} onStop={() => {}} streaming={false} disabled={true} disabledReason="Select an agent runtime above to start chatting" />)
    expect(screen.getByText('Select an agent runtime above to start chatting')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
  })
})

// ── AIChatPanel: resize, scroll-follow, close/reopen, Escape/focus ──────────
//
// Only enough mocked surface to mount the panel on one mocked agent runtime
// ("claude") — resize/scroll/focus/Escape are orthogonal to which runtime
// is active.

const createChatConversation2 = vi.fn()
const startChatTurn2 = vi.fn()
// Base default is deliberately {items: []} — the bucket effect fires
// exactly once per workflowID, and each artifact test's own
// .mockResolvedValueOnce covers that one call.
const listChatConversations2 = vi.fn().mockResolvedValue({ items: [] })
const getChatTurns2 = vi.fn().mockResolvedValue({ items: [] })
const getChatEvents2 = vi.fn().mockResolvedValue({ items: [], hasMore: false })
const listOrgDesigns2 = vi.fn().mockResolvedValue(null)
const listProfileDocuments2 = vi.fn().mockResolvedValue([])
const getProfileDocument2 = vi.fn().mockResolvedValue(null)
const getWorkflow2 = vi.fn().mockResolvedValue(null)
const stopChatTurn2 = vi.fn().mockResolvedValue({ ok: true })

vi.mock('../../services/api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    api: {
      ...actual.api,
      scanAgentRuntimes: vi.fn().mockResolvedValue({ agents: [{ id: 'claude', installed: true, binary: '' }] }),
      getAgentRuntimeModels: vi.fn().mockResolvedValue([{ id: 'sonnet' }]),
      listChatConversations: (...args) => listChatConversations2(...args),
      createChatConversation: (...args) => createChatConversation2(...args),
      startChatTurn: (...args) => startChatTurn2(...args),
      getChatTurns: (...args) => getChatTurns2(...args),
      getChatEvents: (...args) => getChatEvents2(...args),
      stopChatTurn: (...args) => stopChatTurn2(...args),
      listOrgDesigns: (...args) => listOrgDesigns2(...args),
      listProfileDocuments: (...args) => listProfileDocuments2(...args),
      getProfileDocument: (...args) => getProfileDocument2(...args),
      getWorkflow: (...args) => getWorkflow2(...args),
    },
  }
})

beforeEach(() => {
  vi.clearAllMocks()
})

describe('AIChatPanel stop() error handling', () => {
  it('recovers from a failed stopChatTurn instead of stranding the UI on "Stopping" forever', async () => {
    createChatConversation2.mockResolvedValue({ id: 'conv-4', backend: 'agent' })
    startChatTurn2.mockResolvedValue({ ok: true, turnId: 'ignored', status: 'active' })
    // stopChatTurn goes through parseStreamResult, which throws on this
    // exact {"error":...} shape (same convention every other chat binding
    // uses for a synchronous failure).
    stopChatTurn2.mockRejectedValueOnce(new Error('chat supervisor not initialized'))

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    const textarea = await screen.findByPlaceholderText('Type a message...')
    fireEvent.change(textarea, { target: { value: 'hello' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Stop generating' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Stop generating' }))
    await waitFor(() => expect(stopChatTurn2).toHaveBeenCalled())

    // Without the fix, stopRequested is stuck true forever (activeTurnId
    // only clears via liveTurn.terminal, which a failed stop call never
    // produces) — the status line is permanently stuck on "Stopping" with
    // no way to retry. With the fix, it resets so the turn is still
    // visibly running and stoppable again.
    await waitFor(() => expect(screen.queryByText('Stopping')).not.toBeInTheDocument())
  })
})

describe('AIChatPanel resize/scroll/focus/Escape', () => {
  it('keeps a persistent turn-announcement live region mounted at all times, not just while a turn is streaming', async () => {
    // Regression: the old per-turn TurnStatus "role=status" region only
    // existed inside {streaming && ...} — unmounted the instant a turn
    // finished, right when its terminal content would need to be
    // announced. A region that must announce turn completion has to
    // already be in the DOM before that moment, not appear at it.
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await screen.findByPlaceholderText('Type a message...')
    expect(screen.getByRole('status', { name: /chat turn announcements/i })).toBeInTheDocument()
  })

  it('dragging the resize handle changes the panel width without touching an in-flight turn', async () => {
    createChatConversation2.mockResolvedValue({ id: 'conv-1', backend: 'agent' })
    startChatTurn2.mockResolvedValue({ ok: true, turnId: 'ignored', status: 'active' })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    const textarea = await screen.findByPlaceholderText('Type a message...')

    // Start a turn so we can prove resizing doesn't disturb it.
    fireEvent.change(textarea, { target: { value: 'hello' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Stop generating' })).toBeInTheDocument())

    const handle = screen.getByTitle('Drag to resize')
    const panel = handle.parentElement
    const initialWidth = panel.style.width
    fireEvent.mouseDown(handle, { clientX: 400 })
    // Body-level userSelect/cursor are suppressed for the drag's duration so
    // dragging over the transcript (or the rest of the page) doesn't fight
    // the resize with a native text selection.
    expect(document.body.style.userSelect).toBe('none')
    fireEvent.mouseMove(window, { clientX: 300 }) // dragged left -> wider
    fireEvent.mouseUp(window)

    expect(document.body.style.userSelect).not.toBe('none')
    expect(panel.style.width).not.toBe(initialWidth)
    // The turn is still running — resizing is a pure layout operation.
    expect(screen.getByRole('button', { name: 'Stop generating' })).toBeInTheDocument()
  })

  it('shows Jump to latest once new content arrives while the reader has scrolled away, and returns to following on click', async () => {
    createChatConversation2.mockResolvedValue({ id: 'conv-1', backend: 'agent' })
    startChatTurn2.mockResolvedValue({ ok: true, turnId: 'ignored', status: 'active' })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    const textarea = await screen.findByPlaceholderText('Type a message...')

    // Scroll away from the bottom FIRST — before any content is added —
    // so the message this test sends next is genuinely "new content that
    // arrives while not following", not content added before the reader
    // ever scrolled.
    const scrollContainer = document.querySelector('[style*="overflow-y: auto"]')
    Object.defineProperty(scrollContainer, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(scrollContainer, 'clientHeight', { value: 500, configurable: true })
    Object.defineProperty(scrollContainer, 'scrollTop', { value: 100, writable: true, configurable: true }) // far from bottom
    fireEvent.scroll(scrollContainer)

    fireEvent.change(textarea, { target: { value: 'hello' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }))
    await waitFor(() => expect(startChatTurn2).toHaveBeenCalled())

    expect(await screen.findByText(/Jump to latest/)).toBeInTheDocument()
    fireEvent.click(screen.getByText(/Jump to latest/))
    await waitFor(() => expect(screen.queryByText(/Jump to latest/)).not.toBeInTheDocument())
  })

  it('preserves the draft and transcript across close/reopen (no remount — same component instance)', async () => {
    const { rerender } = render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    const textarea = await screen.findByPlaceholderText('Type a message...')
    fireEvent.change(textarea, { target: { value: 'unsent draft' } })

    rerender(<AIChatPanel workflowID="general" isOpen={false} onClose={() => {}} />)
    expect(screen.queryByPlaceholderText('Type a message...')).not.toBeInTheDocument()

    rerender(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    expect(screen.getByPlaceholderText('Type a message...')).toHaveValue('unsent draft')
  })

  it('restores focus to whatever opened the panel once it closes', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    expect(document.activeElement).toBe(opener)

    const { rerender } = render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await screen.findByPlaceholderText('Type a message...')

    // Simulate the user actually interacting inside the panel (opening it
    // alone doesn't move focus — this plan only asks for focus *return* on
    // close, not a focus trap on open).
    screen.getByPlaceholderText('Type a message...').focus()

    rerender(<AIChatPanel workflowID="general" isOpen={false} onClose={() => {}} />)
    expect(document.activeElement).toBe(opener)
    opener.remove()
  })

  it('Escape collapses an expanded view first, and only closes when already docked', async () => {
    const onClose = vi.fn()
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={onClose} />)
    await screen.findByPlaceholderText('Type a message...')

    fireEvent.click(screen.getByTitle('Expand'))
    expect(screen.getByTitle('Collapse')).toBeInTheDocument()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByTitle('Expand')).toBeInTheDocument() // back to docked

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('Escape closes immediately when narrow, even if presentation is still "expanded" from a wider viewport', async () => {
    // The expand/collapse toggle is hidden once narrow (no room for it), so
    // a panel left "expanded" before the viewport shrank would otherwise be
    // unreachable by Escape — it would only collapse to a state with no
    // visible control to get out of, trapping the user behind the
    // full-viewport overlay with just the header's X button left.
    const onClose = vi.fn()
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={onClose} />)
    await screen.findByPlaceholderText('Type a message...')

    fireEvent.click(screen.getByTitle('Expand'))
    expect(screen.getByTitle('Collapse')).toBeInTheDocument()

    window.innerWidth = 480
    fireEvent(window, new Event('resize'))
    await waitFor(() => expect(screen.queryByTitle('Collapse')).not.toBeInTheDocument())

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('the past-sessions toggle button reports its open/closed state via aria-expanded', async () => {
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await screen.findByPlaceholderText('Type a message...')

    const toggle = screen.getByTitle('Past sessions')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })

  it('Escape closes only the past-sessions dropdown when it is open, not the whole panel', async () => {
    const onClose = vi.fn()
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={onClose} />)
    await screen.findByPlaceholderText('Type a message...')

    fireEvent.click(screen.getByTitle('Past sessions'))
    expect(screen.getByRole('listbox', { name: /past sessions/i })).toBeInTheDocument()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('past-session rows are keyboard-focusable and activatable with Enter, not just mouse-clickable', async () => {
    const convA = { id: 'conv-a', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'model-a', updatedAt: '2026-09-12T00:00:00.000Z' }
    const convB = { id: 'conv-b', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'model-b', updatedAt: '2026-09-12T00:00:01.000Z' }
    listChatConversations2.mockResolvedValueOnce({ items: [convA, convB] })
    getChatTurns2.mockImplementation((convId) => Promise.resolve({
      items: [{ id: `turn-${convId}`, prompt: convId === 'conv-a' ? 'from-A' : 'from-B', status: 'completed' }],
    }))
    getChatEvents2.mockResolvedValue({ items: [], hasMore: false })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await screen.findByText('from-A') // auto-continued conv-a on mount

    fireEvent.click(await screen.findByTitle('Past sessions'))
    const rowB = await screen.findByRole('option', { name: /model-b/ })
    rowB.focus()
    fireEvent.keyDown(rowB, { key: 'Enter' })

    await screen.findByText('from-B')

    getChatTurns2.mockReset().mockResolvedValue({ items: [] })
    getChatEvents2.mockReset().mockResolvedValue({ items: [], hasMore: false })
  })

  it("marks the current conversation's row as aria-selected, not just a background tint", async () => {
    const convA = { id: 'conv-a', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'model-a', updatedAt: '2026-09-12T00:00:00.000Z' }
    const convB = { id: 'conv-b', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'model-b', updatedAt: '2026-09-12T00:00:01.000Z' }
    listChatConversations2.mockResolvedValueOnce({ items: [convA, convB] })
    getChatTurns2.mockResolvedValue({ items: [] })
    getChatEvents2.mockResolvedValue({ items: [], hasMore: false })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await screen.findByPlaceholderText('Type a message...')

    fireEvent.click(await screen.findByTitle('Past sessions'))
    const rowA = await screen.findByRole('option', { name: /model-a/ })
    const rowB = screen.getByRole('option', { name: /model-b/ })
    expect(rowA).toHaveAttribute('aria-selected', 'true')
    expect(rowB).toHaveAttribute('aria-selected', 'false')

    getChatTurns2.mockReset().mockResolvedValue({ items: [] })
    getChatEvents2.mockReset().mockResolvedValue({ items: [], hasMore: false })
  })
})

// ── Chat result artifacts (Task 6) ──────────────────────────────────────────
//
// chatArtifacts.test.js already covers detectArtifactCandidate/resolveArtifact
// exhaustively at the unit level (forged/malformed/missing/cross-profile
// inputs). What that file CANNOT prove is that AIChatPanel's own wiring
// actually calls them and renders the result — these two tests drive a
// real past turn through loadConversation (mocked getChatTurns/getChatEvents,
// same as production) to exercise that wiring end to end.

function deferred() {
  let resolve
  const promise = new Promise(r => { resolve = r })
  return { promise, resolve }
}

function turnEvents(callName, resultObj) {
  return [
    { seq: 1, at: '2026-09-12T00:00:00Z', type: 'turn.started', payload: {} },
    { seq: 2, at: '2026-09-12T00:00:01Z', type: 'tool.started', payload: { callId: 'call-1', name: callName, arguments: {} } },
    { seq: 3, at: '2026-09-12T00:00:02Z', type: 'tool.completed', payload: { callId: 'call-1', ok: true, result: JSON.stringify(resultObj) } },
    { seq: 4, at: '2026-09-12T00:00:03Z', type: 'turn.finished', payload: { status: 'completed', reason: '', exitCode: 0, historySaved: true } },
  ]
}

describe('AIChatPanel chat result artifacts', () => {
  it('renders an Open action for a create_org result once the name is confirmed against the real org listing, and wires it to onOpenArtifact', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-1', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-1', prompt: 'create an org called Acme', status: 'completed' }] })
    getChatEvents2.mockResolvedValueOnce({ items: turnEvents('create_org', { org_name: 'Acme', created: true }), hasMore: false })
    // Two calls expected: the initial resolve that builds the card, and the
    // click-time re-validation that must also find it still there.
    listOrgDesigns2
      .mockResolvedValueOnce([{ name: 'Acme' }])
      .mockResolvedValueOnce([{ name: 'Acme' }])

    const onOpenArtifact = vi.fn()
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} onOpenArtifact={onOpenArtifact} />)

    const openBtn = await screen.findByTitle('Open organization')
    fireEvent.click(openBtn)
    await waitFor(() => expect(onOpenArtifact).toHaveBeenCalledWith({ type: 'org', name: 'Acme' }))
  })

  it('renders no action for a save_document result whose vault id no longer resolves on either the initial lookup or its automatic retry (deleted/cross-profile), leaving the generic tool card as the only output', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-2', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-2', prompt: 'save a report', status: 'completed' }] })
    getChatEvents2.mockResolvedValueOnce({
      items: turnEvents('save_document', { filename: 'report.md', path: '/x/report.md', size_bytes: 10, vault_document_id: 'doc-999' }),
      hasMore: false,
    })
    // doc-999 no longer resolves (deleted/cross-profile) — mocked null twice
    // so useResolvedArtifacts' own automatic one-time retry (see below) also
    // observes a null instead of falling through to the shared base mock,
    // and so this test waits out the whole retry window itself rather than
    // leaving a pending retry to fire during a later test.
    getProfileDocument2.mockResolvedValueOnce(null).mockResolvedValueOnce(null)

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    // The generic ToolActivityCard renders unconditionally, synchronously —
    // proves the turn actually loaded before asserting on the async part.
    await screen.findByText('save_document')
    await waitFor(() => expect(getProfileDocument2).toHaveBeenCalledTimes(2), { timeout: 2000 })
    expect(screen.queryByTitle('Open document')).not.toBeInTheDocument()
  })

  it('keys the resolved-artifact cache by (turnId, callId), not callId alone — two turns reusing the same callId must not share a cached artifact', async () => {
    // The agent backend's callId is passed straight through from the
    // external monomind protocol's own per-event id with no cross-turn
    // uniqueness guarantee (plan: tool identity is (turnId,callId), never
    // callId alone) — turnEvents() below hardcodes callId 'call-1' for
    // every turn, so both turns here collide on purpose.
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-3', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({
      items: [
        { id: 'turn-a', prompt: 'make a workflow', status: 'completed' },
        { id: 'turn-b', prompt: 'make an org', status: 'completed' },
      ],
    })
    // loadConversation processes turns oldest-first via .slice().reverse(),
    // so turn-b ("make an org") is fetched before turn-a ("make a
    // workflow") — these are queued in THAT order so each turn's events
    // actually match its own prompt.
    getChatEvents2
      .mockResolvedValueOnce({ items: turnEvents('create_org', { org_name: 'SecondTurnOrg', created: true }), hasMore: false })
      .mockResolvedValueOnce({ items: turnEvents('create_workflow', { workflow_id: 'wf-shared-id' }), hasMore: false })
    getWorkflow2.mockResolvedValueOnce({ id: 'wf-shared-id', name: 'First Turn Workflow' })
    listOrgDesigns2.mockResolvedValueOnce([{ name: 'SecondTurnOrg' }])

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    // Both artifact cards must appear, correctly attributed to their own
    // turn — not one card overwriting or hiding the other via a colliding
    // cache key.
    await screen.findByTitle('Copy workflow ID')
    await screen.findByTitle('Open organization')
    expect(screen.getByText('First Turn Workflow')).toBeInTheDocument()
    expect(screen.getByText('SecondTurnOrg')).toBeInTheDocument()
  })

  it('re-validates at click time — a card that resolved successfully but whose org was since deleted refuses to open', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-5', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-5', prompt: 'create an org called Acme', status: 'completed' }] })
    getChatEvents2.mockResolvedValueOnce({ items: turnEvents('create_org', { org_name: 'Acme', created: true }), hasMore: false })
    // First call (initial resolve, while building the card) finds it;
    // second call (re-validation on click, sometime later) no longer does —
    // simulates the org being deleted in between.
    listOrgDesigns2
      .mockResolvedValueOnce([{ name: 'Acme' }])
      .mockResolvedValueOnce([])

    const onOpenArtifact = vi.fn()
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} onOpenArtifact={onOpenArtifact} />)

    const openBtn = await screen.findByTitle('Open organization')
    fireEvent.click(openBtn)

    // The click must trigger a fresh lookup, not just replay the cached
    // resolution from when the card first appeared.
    await waitFor(() => expect(listOrgDesigns2).toHaveBeenCalledTimes(2))
    expect(onOpenArtifact).not.toHaveBeenCalled()
  })
})

// ── useResolvedArtifacts: one automatic retry on a transient failure ───────
//
// resolveArtifact's own null result is ambiguous — "confirmed gone" and
// "the lookup itself failed" (e.g. a transient hiccup on the same SQLite
// file the chat supervisor is concurrently writing to right as a tool call
// completes) look identical by the time api.js's guard() is done swallowing
// a real error into the same null shape. Unlike the click-time revalidation
// above (which accepts that ambiguity for a re-check), this is the *first*
// resolution — before this fix a transient failure here permanently hid the
// card for the rest of the session. useResolvedArtifacts now gives a null
// exactly one automatic retry before caching it.
describe('AIChatPanel resolved-artifact retry on transient failure', () => {
  it('retries exactly once after a transient failure and shows the card once the retry succeeds', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-retry-1', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-retry-1', prompt: 'save a report', status: 'completed' }] })
    getChatEvents2.mockResolvedValueOnce({
      items: turnEvents('save_document', { filename: 'report.md', path: '/x/report.md', size_bytes: 10, vault_document_id: 'doc-flaky' }),
      hasMore: false,
    })
    // First lookup fails transiently (not a real "not found"); the
    // automatic retry's second attempt succeeds.
    getProfileDocument2
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce({ id: 'doc-flaky', filename: 'report.md', path: '/x/report.md', size_bytes: 10 })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText('save_document') // generic tool card confirms the turn loaded
    await waitFor(() => expect(getProfileDocument2).toHaveBeenCalledTimes(2), { timeout: 2000 })
    await screen.findByTitle('Open document')
    expect(screen.getByText('report.md')).toBeInTheDocument()
  })

  it('gives up after exactly one retry when the second attempt also fails, and never calls the lookup a third time even across later re-renders', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-retry-2', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-retry-2', prompt: 'save a report', status: 'completed' }] })
    getChatEvents2.mockResolvedValueOnce({
      items: turnEvents('save_document', { filename: 'gone.md', path: '/x/gone.md', size_bytes: 5, vault_document_id: 'doc-really-gone' }),
      hasMore: false,
    })
    getProfileDocument2
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(null)

    const { rerender } = render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText('save_document')
    await waitFor(() => expect(getProfileDocument2).toHaveBeenCalledTimes(2), { timeout: 2000 })
    expect(screen.queryByTitle('Open document')).not.toBeInTheDocument()

    // The requirement being tested is permanent caching after the second
    // failure, not merely "no third call within some window" — force two
    // more re-renders (the no-dependency-array effect re-runs on every one)
    // and confirm the exhausted key is never re-attempted. This is what
    // would catch a give-up branch that clears the in-flight guard without
    // ever writing the permanent null into the resolved cache.
    rerender(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    rerender(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    expect(getProfileDocument2).toHaveBeenCalledTimes(2)
  })
})

// ── Replayed turns must not misreport an orphaned tool call as live ────────
describe('AIChatPanel replayed tool-call status', () => {
  it('a call still "started" in a reopened (finalized) conversation shows Interrupted, not a live-ticking Running', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-6', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-6', prompt: 'do something slow', status: 'cancelled' }] })
    getChatEvents2.mockResolvedValueOnce({
      items: [
        { seq: 1, at: '2026-09-12T00:00:00Z', type: 'turn.started', payload: {} },
        { seq: 2, at: '2026-09-12T00:00:01Z', type: 'tool.started', payload: { callId: 'call-1', name: 'slow_tool', arguments: {} } },
        // No tool.completed — Stop landed mid-call; the turn still ended.
        { seq: 3, at: '2026-09-12T00:00:02Z', type: 'turn.finished', payload: { status: 'cancelled', reason: 'stopped', exitCode: null, historySaved: true } },
      ],
      hasMore: false,
    })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText(/Interrupted/i)
    expect(screen.queryByText(/^Running$/i)).not.toBeInTheDocument()
  })
})

// ── Cross-instance ownership label ──────────────────────────────────────────
//
// GetChatTurns' ownedByThisInstance field (see
// docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md,
// "OwnerInstanceID is written and read back but never compared to
// anything") is false only for a turn that is genuinely still active AND
// owned by a DIFFERENT live app instance sharing this database. This
// instance has no event stream for that turn and cannot stop it — a
// ticking spinner or a reachable Stop control here would misreport it as
// something this window is actively running.
describe('AIChatPanel cross-instance ownership label', () => {
  // turn.started + an unfinished tool.started, no turn.finished: genuinely
  // still active (no terminal event), matching the shape GetChatTurns would
  // report for a turn actually still running — here or in another window.
  function activeNoTerminalEvents() {
    return [
      { seq: 1, at: '2026-09-12T00:00:00Z', type: 'turn.started', payload: {} },
      { seq: 2, at: '2026-09-12T00:00:01Z', type: 'tool.started', payload: { callId: 'call-1', name: 'slow_tool', arguments: {} } },
    ]
  }

  it('shows a static "Running in another window" label, with no live spinner and no reachable Stop, for an active turn owned by a different instance', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-foreign', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-foreign', prompt: 'do something in the other window', status: 'active', ownedByThisInstance: false }] })
    getChatEvents2.mockResolvedValueOnce({ items: activeNoTerminalEvents(), hasMore: false })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText('Running in another window')
    expect(screen.queryByText(/^Running slow_tool$/)).not.toBeInTheDocument()
    // No live spinner for this turn's own status row (the tool card's own
    // icon is a separate, pre-existing concern — historical turns already
    // render with isLive=false regardless of ownership).
    expect(document.querySelector('.chat-spin')).not.toBeInTheDocument()
    // Documents intent rather than proving it on its own (streaming is
    // false here regardless of ownership, so this alone would pass even
    // unfixed) — the actual guarantee is architectural: the panel's only
    // Stop affordance is the composer's onStop, gated on activeTurnId/
    // streaming, and loadConversation always sets activeTurnId to '' for
    // every reopened past conversation, foreign-owned or not. No code path
    // promotes a historical turn (this one included) into the live/
    // streaming slot Stop is wired to.
    expect(screen.queryByRole('button', { name: 'Stop generating' })).not.toBeInTheDocument()
  })

  it('renders the turn (prompt bubble + label) even with zero observed parts yet, when it is active and foreign — the pre-existing empty-parts skip must not hide it', async () => {
    // Root cause this guards: loadTurnState returns null (and
    // loadConversation then `continue`s, dropping the turn AND its prompt
    // bubble entirely) whenever an active turn's reduced state has zero
    // parts — turn.started alone never adds one. That skip predates this
    // feature and exists for a plausible different case (this instance's
    // own turn, just admitted, with no events yet) but a foreign turn in
    // the exact same shape (just started elsewhere, no tool/text event
    // observed yet — routine while a subprocess launches or a model
    // connects) would otherwise vanish with no explanation right as this
    // window's own next send() in that conversation gets refused
    // (ErrTurnOwnedByOtherInstance) — worse than a stale label, a fully
    // silent one.
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-foreign-empty', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-foreign-empty', prompt: 'just started elsewhere', status: 'active', ownedByThisInstance: false }] })
    getChatEvents2.mockResolvedValueOnce({ items: [{ seq: 1, at: '2026-09-12T00:00:00Z', type: 'turn.started', payload: {} }], hasMore: false })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText('just started elsewhere') // the user-prompt bubble
    await screen.findByText('Running in another window')
  })

  // A turn still running here is re-attached to the live stream (useChatStream),
  // which only applies events carrying this conversation and turn.
  it('regression: an active turn with ownedByThisInstance:true renders its normal live "Running <tool>" treatment, unaffected', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-own-1', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-own-1', prompt: 'do something here', status: 'active', ownedByThisInstance: true }] })
    getChatEvents2.mockResolvedValueOnce({ items: activeNoTerminalEvents().map((e) => ({ ...e, conversationId: 'conv-own-1', turnId: 'turn-own-1' })), hasMore: false })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText('Running slow_tool')
    expect(screen.queryByText('Running in another window')).not.toBeInTheDocument()
  })

  it('regression: an active turn with ownedByThisInstance entirely absent (older data / backend not yet carrying the field) is treated as normal, not foreign', async () => {
    listChatConversations2.mockResolvedValueOnce({ items: [{ id: 'conv-own-2', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: '', updatedAt: '2026-09-12T00:00:00Z' }] })
    getChatTurns2.mockResolvedValueOnce({ items: [{ id: 'turn-own-2', prompt: 'do something here too', status: 'active' }] }) // no ownedByThisInstance field at all
    getChatEvents2.mockResolvedValueOnce({ items: activeNoTerminalEvents().map((e) => ({ ...e, conversationId: 'conv-own-2', turnId: 'turn-own-2' })), hasMore: false })

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await screen.findByText('Running slow_tool')
    expect(screen.queryByText('Running in another window')).not.toBeInTheDocument()
  })
})

// ── loadConversation staleness guard ────────────────────────────────────────
//
// A slow, superseded load must never clobber a newer one's transcript (plan:
// "Guard asynchronous loads with a conversation generation token"). This is
// a genuinely reachable UI race, not a synthetic one: the panel's own
// bucket-auto-continue effect calls loadConversation(items[0]) on mount, and
// a user opening the sessions dropdown and picking a DIFFERENT conversation
// before that first fetch finishes is a completely ordinary interaction.
describe('AIChatPanel loadConversation race', () => {
  it('a later click wins even if its fetch resolves before an earlier, still-in-flight one', async () => {
    const convA = { id: 'conv-a', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'model-a', updatedAt: '2026-09-12T00:00:00.000Z' }
    const convB = { id: 'conv-b', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'model-b', updatedAt: '2026-09-12T00:00:01.000Z' }
    // items[0] (convA) auto-continues on mount via the bucket effect.
    listChatConversations2.mockResolvedValueOnce({ items: [convA, convB] })

    const turnsA = deferred()
    const turnsB = deferred()
    getChatTurns2.mockImplementation((convId) => (convId === 'conv-a' ? turnsA.promise : turnsB.promise))
    getChatEvents2.mockImplementation((_convId, turnId) => Promise.resolve({
      items: turnEvents('create_workflow', { workflow_id: `wf-${turnId}` }),
      hasMore: false,
    }))

    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    // Auto-continue has already called loadConversation(convA) — its
    // getChatTurns is pending on turnsA. Switch to convB (a later click)
    // before A resolves.
    fireEvent.click(await screen.findByTitle('Past sessions'))
    fireEvent.click(await screen.findByText('model-b'))

    // Resolve out of click order on purpose — B (the later click) finishes
    // first, A (the earlier, now-superseded click) finishes last. Without
    // the generation guard, A's late arrival overwrites B's transcript.
    turnsB.resolve({ items: [{ id: 'turn-b', prompt: 'from B', status: 'completed' }] })
    await screen.findByText('from B')
    turnsA.resolve({ items: [{ id: 'turn-a', prompt: 'from A', status: 'completed' }] })
    // Give A's now-superseded chain a chance to (wrongly, if unguarded) land.
    await new Promise(r => setTimeout(r, 0))

    expect(screen.getByText('from B')).toBeInTheDocument()
    expect(screen.queryByText('from A')).not.toBeInTheDocument()

    // Restore the shared mocks' plain default behavior for hygiene — this
    // test is the only one in the file using .mockImplementation on these.
    getChatTurns2.mockReset().mockResolvedValue({ items: [] })
    getChatEvents2.mockReset().mockResolvedValue({ items: [], hasMore: false })
  })
})

// ── Bucket-switch history fetch: retry after failure ────────────────────────
//
// conversationsFetchedRef marks a bucket "fetched" synchronously, before the
// request it guards even resolves — so a failure must not leave that mark in
// place, or the bucket becomes permanently stuck: same workflowID,
// same bucket string, so a plain close/reopen of the panel (isOpen only)
// never re-triggers the effect's fetch again once the ref already matches.
describe('AIChatPanel bucket-switch history fetch', () => {
  it('retries on close/reopen after a failed fetch, instead of leaving the bucket stuck', async () => {
    listChatConversations2.mockRejectedValueOnce(new Error('backend unavailable'))
    listChatConversations2.mockResolvedValueOnce({
      items: [{ id: 'conv-retry', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'retried-model', updatedAt: '2026-09-12T00:00:00Z' }],
    })
    getChatTurns2.mockResolvedValue({ items: [] })

    const { rerender } = render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await waitFor(() => expect(listChatConversations2).toHaveBeenCalledTimes(1))

    // Close and reopen the SAME bucket (workflowID unchanged) —
    // the only user action a stuck ref would make unrecoverable, since
    // isOpen toggling is the one thing that still re-runs this effect.
    rerender(<AIChatPanel workflowID="general" isOpen={false} onClose={() => {}} />)
    rerender(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)

    await waitFor(() => expect(listChatConversations2).toHaveBeenCalledTimes(2))
    // Proves the retry's result was actually used (loadConversation ran),
    // not just that a second HTTP-ish call happened.
    await waitFor(() => expect(getChatTurns2).toHaveBeenCalledWith('conv-retry', '', 50))
  })

  // The .catch() above has always had this staleness check; the .then()
  // success path did not — a late-resolving fetch for a bucket the UI no
  // longer shows could call loadConversation (or clear state) and stomp
  // whatever the newer, already-current bucket had just loaded.
  it('a stale bucket fetch resolving after a newer bucket already loaded does not overwrite it', async () => {
    const bucketA = deferred()
    listChatConversations2.mockImplementationOnce(() => bucketA.promise) // wf-a's fetch — stays pending
    listChatConversations2.mockResolvedValueOnce({
      items: [{ id: 'conv-b', backend: 'agent', workflowContext: 'wf-b', runtimeId: 'claude', model: 'model-b', updatedAt: '2026-09-12T00:00:01Z' }],
    })
    getChatTurns2.mockImplementation((convId) => Promise.resolve({
      items: [{ id: `turn-${convId}`, prompt: convId === 'conv-a' ? 'from-A' : 'from-B', status: 'completed' }],
    }))
    getChatEvents2.mockResolvedValue({ items: [], hasMore: false })

    const { rerender } = render(<AIChatPanel workflowID="wf-a" isOpen={true} onClose={() => {}} />)
    await waitFor(() => expect(listChatConversations2).toHaveBeenCalledTimes(1))

    // Switch buckets (a new workflowID) before wf-a's fetch resolves.
    rerender(<AIChatPanel workflowID="wf-b" isOpen={true} onClose={() => {}} />)
    await waitFor(() => expect(listChatConversations2).toHaveBeenCalledTimes(2))
    await screen.findByText('from-B')

    // The stale wf-a fetch finally resolves, with a conversation for the
    // bucket the UI no longer shows.
    await act(async () => {
      bucketA.resolve({ items: [{ id: 'conv-a', backend: 'agent', workflowContext: 'wf-a', runtimeId: 'claude', model: 'model-a', updatedAt: '2026-09-12T00:00:00Z' }] })
      await new Promise(r => setTimeout(r, 0))
    })

    expect(screen.getByText('from-B')).toBeInTheDocument()
    expect(screen.queryByText('from-A')).not.toBeInTheDocument()

    getChatTurns2.mockReset().mockResolvedValue({ items: [] })
    getChatEvents2.mockReset().mockResolvedValue({ items: [], hasMore: false })
  })
})
