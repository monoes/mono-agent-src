// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({
  api: { openURL: vi.fn(), resolveChatPath: vi.fn(), revealChatPath: vi.fn(), readChatMarkdown: vi.fn() },
  notify: vi.fn(),
}))
import { api, notify } from '../../services/api.js'
import { ChatMarkdown } from './ChatMarkdown.jsx'
import { clearChatPathCache } from './ChatPathChip.jsx'
import { WorkspaceImageContext } from './ChatAttachments.jsx'

const files = {
  'docs/plan.md': { kind: 'file', previewable: true, path: '/w/docs/plan.md' },
  'src/main.go': { kind: 'file', previewable: false, path: '/w/src/main.go' },
  'src/main.go:42': { kind: 'file', previewable: false, path: '/w/src/main.go', line: 42 },
  '~/projects/x': { kind: 'dir', previewable: false, path: '/home/u/projects/x' },
}

beforeEach(() => {
  clearChatPathCache()
  api.resolveChatPath.mockImplementation(async (_c, token) => files[token] || { kind: 'missing' })
  api.revealChatPath.mockResolvedValue(undefined)
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

const mount = (content) => render(
  <WorkspaceImageContext.Provider value="conv-1"><ChatMarkdown content={content} /></WorkspaceImageContext.Provider>,
)

describe('links in chat messages', () => {
  it('opens an https link in the browser and shows the real target', () => {
    mount('[the report](https://example.com/r?a=1)')
    const a = screen.getByRole('link', { name: 'the report' })
    expect(a).toHaveAttribute('title', 'Open in browser: https://example.com/r?a=1')
    fireEvent.click(a)
    expect(api.openURL).toHaveBeenCalledWith('https://example.com/r?a=1')
  })

  it.each([
    '[x](javascript:alert(1))', '[x](data:text/html;base64,AAAA)', '[x](file:///etc/passwd)',
    '[x](vbscript:1)', '[x](mailto:a@b.c)', '[x](https://bank.com@evil.test/)',
  ])('renders %s inert', (md) => {
    mount(md)
    expect(screen.queryByRole('link')).toBeNull()
    fireEvent.click(screen.getByText('x'))
    expect(api.openURL).not.toHaveBeenCalled()
  })

  it('does not let link text disguise the host', () => {
    mount('[https://bank.com/login](https://evil.test/steal)')
    expect(screen.getByRole('link')).toHaveAttribute('title', expect.stringContaining('https://evil.test/steal'))
    expect(screen.getByTitle(/bank\.com but this link opens evil\.test/)).toHaveTextContent('(evil.test)')
  })

  it('middle-click is routed through the same gate', () => {
    mount('[a](https://example.com/)')
    fireEvent(screen.getByRole('link'), new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1 }))
    expect(api.openURL).toHaveBeenCalledWith('https://example.com/')
  })
})

describe('file paths in chat messages', () => {
  it('styles an inline-code path as a button only once it exists', async () => {
    mount('Edit `docs/plan.md` and `docs/ghost.md`.')
    const b = await screen.findByRole('button', { name: 'docs/plan.md' })
    expect(b).toHaveAttribute('title', expect.stringContaining('Preview'))
    expect(screen.getByText('docs/ghost.md').closest('button')).toBeNull()
    expect(api.resolveChatPath).toHaveBeenCalledWith('conv-1', 'docs/plan.md')
  })

  it('finds a path in plain text, and leaves a missing one as text', async () => {
    mount('See src/main.go:42 and src/other.go for it.')
    const b = await screen.findByRole('button', { name: 'src/main.go:42' })
    expect(b).toHaveAttribute('title', expect.stringContaining('line 42'))
    expect(screen.queryByRole('button', { name: 'src/other.go' })).toBeNull()
    expect(screen.getByText(/src\/other\.go/)).toBeInTheDocument()
  })

  it('a click on a non-markdown file reveals it', async () => {
    mount('`src/main.go`')
    fireEvent.click(await screen.findByRole('button', { name: 'src/main.go' }))
    expect(api.revealChatPath).toHaveBeenCalledWith('conv-1', 'src/main.go')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('a folder opens in the file manager', async () => {
    mount('`~/projects/x`')
    fireEvent.click(await screen.findByRole('button', { name: '~/projects/x' }))
    expect(api.revealChatPath).toHaveBeenCalledWith('conv-1', '~/projects/x')
  })

  it('a click on a markdown file previews it; Ctrl/Cmd-click and the folder button reveal', async () => {
    api.readChatMarkdown.mockResolvedValue('# Plan\n\nstep **one**')
    mount('`docs/plan.md`')
    const b = await screen.findByRole('button', { name: 'docs/plan.md' })
    fireEvent.click(b, { ctrlKey: true })
    expect(api.revealChatPath).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /Show plan\.md in the file manager/ }))
    expect(api.revealChatPath).toHaveBeenCalledTimes(2)

    fireEvent.click(b)
    const dlg = await screen.findByRole('dialog')
    expect(dlg).toHaveAttribute('aria-modal', 'true')
    expect(await screen.findByRole('heading', { name: 'Plan' })).toBeInTheDocument()
    expect(api.readChatMarkdown).toHaveBeenCalledWith('conv-1', 'docs/plan.md')
    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('previews through the sanitising renderer: no raw HTML, inert links, no fetched images', async () => {
    api.readChatMarkdown.mockResolvedValue([
      '<script>window.__pwned=1</script>',
      '<img src=x onerror="window.__pwned=2">',
      '[bad](javascript:window.__pwned=3)',
      '![pic](./local.png) ![remote](https://example.com/p.png)',
      '`docs/plan.md`',
      '[good](https://example.com/ok)',
    ].join('\n\n'))
    mount('`docs/plan.md`')
    fireEvent.click(await screen.findByRole('button', { name: 'docs/plan.md' }))
    const dlg = await screen.findByRole('dialog')
    await screen.findByText('good')
    expect(window.__pwned).toBeUndefined()
    expect(dlg.querySelector('script')).toBeNull()
    expect(dlg.querySelector('img')).toBeNull()
    expect(dlg).toHaveTextContent('<script>window.__pwned=1</script>')
    expect(screen.queryByRole('link', { name: 'bad' })).toBeNull()
    fireEvent.click(screen.getByRole('link', { name: 'good' }))
    expect(api.openURL).toHaveBeenCalledWith('https://example.com/ok')
    // paths inside a file are relative to the file, not looked up against the chat
    expect(api.resolveChatPath).toHaveBeenCalledTimes(1)
  })

  it('shows why a preview failed', async () => {
    api.readChatMarkdown.mockRejectedValue(new Error('file is too large to preview'))
    mount('`docs/plan.md`')
    fireEvent.click(await screen.findByRole('button', { name: 'docs/plan.md' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('too large')
  })

  it('does not look up commands or options', async () => {
    mount('Run `ls -la /` or `--force`.')
    await Promise.resolve()
    expect(api.resolveChatPath).not.toHaveBeenCalled()
  })

  it('reports a reveal failure', async () => {
    api.revealChatPath.mockRejectedValue(new Error('boom'))
    mount('`src/main.go`')
    fireEvent.click(await screen.findByRole('button', { name: 'src/main.go' }))
    await waitFor(() => expect(notify).toHaveBeenCalled())
  })
})

describe('code blocks', () => {
  it('copy the block with a button, and never treat block text as a path', async () => {
    const writeText = vi.fn()
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    mount('```sh\ncat docs/plan.md\n```')
    fireEvent.click(screen.getByRole('button', { name: 'Copy code' }))
    expect(writeText).toHaveBeenCalledWith('cat docs/plan.md\n')
    await Promise.resolve()
    expect(api.resolveChatPath).not.toHaveBeenCalled()
  })
})

describe('pathological markdown', () => {
  it('renders deeply nested quotes as plain text, fast', () => {
    const evil = '> '.repeat(50000)
    const t0 = Date.now()
    mount(evil)
    expect(Date.now() - t0).toBeLessThan(1000)
    expect(screen.getByTestId('chat-markdown-plain').textContent).toBe(evil)
  })

  it('leaves normal markdown rendered', () => {
    mount('# Title\n\n> quote\n\n- a\n  - b\n')
    expect(screen.queryByTestId('chat-markdown-plain')).toBeNull()
    expect(screen.getByRole('heading', { name: 'Title' })).toBeInTheDocument()
  })

  it('the preview applies the same guard', async () => {
    api.resolveChatPath.mockResolvedValue({ kind: 'file', previewable: true, path: '/w/n.md' })
    api.readChatMarkdown.mockResolvedValue('- '.repeat(5000))
    mount('`n.md`')
    fireEvent.click(await screen.findByRole('button', { name: 'n.md' }))
    expect(await screen.findByTestId('chat-markdown-plain')).toBeInTheDocument()
  })

  it('a render fault falls back to the text instead of blanking the app', async () => {
    const { default: ErrorBoundary } = await import('../ErrorBoundary.jsx')
    const Boom = () => { throw new RangeError('Maximum call stack size exceeded') }
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<ErrorBoundary fallback={() => <pre>shown as text</pre>}><Boom /></ErrorBoundary>)
    spy.mockRestore()
    expect(screen.getByText('shown as text')).toBeInTheDocument()
  })
})

describe('disguised links', () => {
  it.each([
    ['[**bank.com**](https://evil.test/x)', /bank\.com but this link opens evil\.test/],
    ['[bаnk.com](https://evil.test/x)', /xn--.* but this link opens evil\.test/],
    ['[bank.com login page](https://evil.test/x)', /bank\.com but this link opens evil\.test/],
  ])('%s warns', (md, re) => {
    mount(md)
    expect(screen.getByTitle(re)).toHaveTextContent('(evil.test)')
  })

  it('shows the destination beside any text that is not the URL, and uses the canonical href', () => {
    mount('[the report](HTTPS://Example.COM/a%20b)\n\n[https://example.com/ok](https://example.com/ok)')
    const links = screen.getAllByRole('link')
    expect(links[1]).toHaveAttribute('href', 'https://example.com/ok')
    expect(screen.getAllByText(/\(example\.com\)/)).toHaveLength(1)
  })
})
