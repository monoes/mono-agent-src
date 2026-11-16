// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({
  api: {
    openURL: vi.fn(), classifyChatPath: vi.fn(async () => 'refused'),
    getWorkflow: vi.fn(), getExecutionDetail: vi.fn(), listOrgDesigns: vi.fn(),
    getPersonDetail: vi.fn(), getProfileDocument: vi.fn(),
  },
  notify: vi.fn(),
}))
vi.mock('../../services/tasks.js', () => ({ tasksApi: { show: vi.fn() } }))
vi.mock('./chatLinks.js', async (orig) => ({ ...(await orig()), isUserGesture: vi.fn(() => true) }))
import { isUserGesture } from './chatLinks.js'
import { api, notify } from '../../services/api.js'
import { tasksApi } from '../../services/tasks.js'
import { ChatMarkdown } from './ChatMarkdown.jsx'
import { ChatBlocksHost, ChatBlocksScope } from './blocks/context.js'
import { FLAG_KEY } from './htmlViewContract.js'

const fence = (info, body) => `\`\`\`${info}\n${typeof body === 'string' ? body : JSON.stringify(body)}\n\`\`\``
const message = [
  'The nightly flow [nightly](monoagent://workflow/w1) failed; see task #7.',
  fence('choices', { question: 'Retry?', options: [{ id: 'y', label: 'yes' }, { id: 'n', label: 'no' }] }),
  fence('chart', { type: 'bar', title: 'Runs', x: ['Mon', 'Tue'], series: [{ name: 'ok', data: [3, 6] }] }),
  fence('card', { kind: 'workflow', title: 'Card flow', link: 'monoagent://workflow/w2' }),
  fence('chart', '{not json'),
].join('\n\n')

const W2 = /Open: monoagent:\/\/workflow\/w2/

function view(onNavigate, onSend) {
  return (
    <ChatBlocksHost.Provider value={{ onSend, conversationId: 'c1' }}>
      <ChatBlocksScope.Provider value={{ scope: 't1:p1', live: false }}>
        <ChatMarkdown content={message} onNavigate={onNavigate} />
      </ChatBlocksScope.Provider>
    </ChatBlocksHost.Provider>
  )
}

beforeEach(() => {
  localStorage.clear()
  api.getWorkflow.mockImplementation(async (id) => ({ id }))
  tasksApi.show.mockResolvedValue({ task: { id: 7 } })
})
afterEach(() => { cleanup(); vi.clearAllMocks(); isUserGesture.mockImplementation(() => true); localStorage.clear() })

describe('one message, all the chat features', () => {
  it('renders prose links, mentions, blocks and the invalid-block fallback', async () => {
    render(view(vi.fn(), vi.fn()))
    expect(await screen.findByRole('button', { name: 'nightly' })).toHaveAttribute('data-chat-app-link', 'workflow')
    expect(await screen.findByRole('button', { name: /task #7/ })).toHaveAttribute('data-chat-app-link', 'task')
    expect(screen.getByRole('button', { name: /yes/ })).toBeInTheDocument()
    expect(screen.getAllByTestId('chat-block-chart')).toHaveLength(1)
    expect(screen.getByText('Card flow')).toBeInTheDocument()
    expect(screen.getByText('{not json')).toBeInTheDocument() // invalid chart stays a code block
  })

  it('a block app link navigates only after the target resolves', async () => {
    let release
    api.getWorkflow.mockImplementation((id) => (id === 'w2' ? new Promise(r => { release = () => r({ id: 'w2' }) }) : Promise.resolve({ id })))
    const nav = vi.fn()
    render(view(nav, vi.fn()))
    fireEvent.click(await screen.findByRole('button', { name: W2 }))
    await waitFor(() => expect(release).toBeTypeOf('function'))
    expect(nav).not.toHaveBeenCalled()
    release()
    await waitFor(() => expect(nav).toHaveBeenCalledWith('noderunner', { workflowId: 'w2' }))
  })

  it('a block app link to something gone never navigates', async () => {
    api.getWorkflow.mockImplementation(async (id) => (id === 'w2' ? null : { id }))
    const nav = vi.fn()
    render(view(nav, vi.fn()))
    fireEvent.click(await screen.findByRole('button', { name: W2 }))
    await waitFor(() => expect(notify).toHaveBeenCalled())
    expect(nav).not.toHaveBeenCalled()
  })

  it('a block app link is refused when the click is not a real gesture', async () => {
    const nav = vi.fn()
    render(view(nav, vi.fn()))
    const btn = await screen.findByRole('button', { name: W2 })
    isUserGesture.mockImplementation(() => false)
    fireEvent.click(btn)
    await new Promise(r => setTimeout(r, 20))
    expect(nav).not.toHaveBeenCalled()
    expect(api.getWorkflow).not.toHaveBeenCalledWith('w2')
  })

  it('the prose chip navigates through the same path', async () => {
    const nav = vi.fn()
    render(view(nav, vi.fn()))
    fireEvent.click(await screen.findByRole('button', { name: 'nightly' }))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('noderunner', { workflowId: 'w1' }))
  })

  it('without onNavigate a block app link stays inert', async () => {
    render(view(undefined, vi.fn()))
    fireEvent.click(await screen.findByRole('button', { name: W2 }))
    await new Promise(r => setTimeout(r, 20))
    expect(api.getWorkflow).not.toHaveBeenCalledWith('w2')
  })
})

describe('html-view onSend asks first', () => {
  const html = fence('html-view', '<button>go</button>')
  function frameView(onSend, onNavigate = vi.fn()) {
    localStorage.setItem(FLAG_KEY, 'true')
    return (
      <ChatBlocksHost.Provider value={{ onSend }}>
        <ChatMarkdown content={html} onNavigate={onNavigate} />
      </ChatBlocksHost.Provider>
    )
  }
  function frameMsg(container, msg) {
    fireEvent.click(screen.getByText('Show interactive view'))
    const iframe = container.querySelector('iframe')
    fireEvent.pointerEnter(iframe.parentElement)
    const post = (data) => window.dispatchEvent(new MessageEvent('message', { data, source: iframe.contentWindow }))
    post({ v: 1, t: 'gesture' })
    post({ v: 1, ...msg })
  }

  it('shows the proposed text and sends only after the user confirms', async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    const { container } = render(frameView(onSend))
    frameMsg(container, { t: 'send', text: 'delete everything' })
    expect(await screen.findByTestId('html-view-confirm')).toHaveTextContent('delete everything')
    expect(onSend).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('delete everything'))
    expect(screen.queryByTestId('html-view-confirm')).toBeNull()
  })

  it('cancel drops it, and a synthetic confirm click is ignored', async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    const { container } = render(frameView(onSend))
    frameMsg(container, { t: 'send', text: 'hi' })
    await screen.findByTestId('html-view-confirm')
    isUserGesture.mockImplementation(() => false)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).not.toHaveBeenCalled()
    isUserGesture.mockImplementation(() => true)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('html-view-confirm')).toBeNull()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('an open request from the frame resolves the link before navigating', async () => {
    const nav = vi.fn()
    const { container } = render(frameView(vi.fn(), nav))
    frameMsg(container, { t: 'open', href: 'monoagent://workflow/w1' })
    await waitFor(() => expect(nav).toHaveBeenCalledWith('noderunner', { workflowId: 'w1' }))
    expect(api.getWorkflow).toHaveBeenCalledWith('w1')
  })
})
