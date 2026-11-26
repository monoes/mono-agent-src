// @vitest-environment jsdom
// AskCard (coder-bubble v2): one card for every ask in a coder or org
// bubble. A question, a tool approval, a gate, a workflow's Human-in-Loop
// item and the approval of a send each render, and each hands the person's
// decision to its caller exactly as made.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'
import sendEmail from './__fixtures__/hil-send-email.json'
import sendDm from './__fixtures__/hil-send-dm.json'
import workflow from './__fixtures__/hil-workflow.json'
import { normalizeAsk } from '../../lib/askModel.js'

const { hilApi } = vi.hoisted(() => ({
  hilApi: { attachment: vi.fn(), openPath: vi.fn(), openUrl: vi.fn(), listAsks: vi.fn(), resolve: vi.fn() },
}))
vi.mock('../../services/hilApi.js', () => hilApi)

import { AskCard } from './AskCard.jsx'
import ConfirmHost from '../ConfirmDialog.jsx'

const nameOf = id => ({ dev: 'Developer', qa: 'QA', ceo: 'Chief' }[id] || id)
const send = () => normalizeAsk(sendEmail)

beforeEach(() => {
  vi.clearAllMocks()
  hilApi.attachment.mockResolvedValue({ dataUrl: 'data:image/png;base64,AAAA', mime: 'image/png' })
})
afterEach(cleanup)

describe('AskCard: question, approval and gate', () => {
  it('answers a question with the typed text', () => {
    const onAnswer = vi.fn()
    render(<AskCard ask={{ kind: 'question', ref: 'q1', pending: true, role: 'qa', text: 'Which env?' }} nameOf={nameOf} onAnswer={onAnswer} />)
    expect(screen.getByTestId('org-chat-question')).toHaveTextContent('QA asks')
    const input = screen.getByLabelText('Your answer')
    fireEvent.change(input, { target: { value: 'staging' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onAnswer).toHaveBeenCalledWith(expect.objectContaining({ ref: 'q1' }), 'staging')
  })

  it('approves or denies a tool approval, and rejects a gate with a note', () => {
    const onDecide = vi.fn()
    const { rerender } = render(<AskCard ask={{ kind: 'approval', ref: 'a1', request_id: 'a1', pending: true, role: 'dev', action: 'Bash', text: 'rm build' }} nameOf={nameOf} onDecide={onDecide} />)
    fireEvent.click(screen.getByTestId('org-chat-approve'))
    expect(onDecide).toHaveBeenLastCalledWith(expect.objectContaining({ ref: 'a1' }), true, { note: '' })
    fireEvent.click(screen.getByTestId('org-chat-deny'))
    expect(onDecide).toHaveBeenLastCalledWith(expect.objectContaining({ ref: 'a1' }), false, { note: '' })
    rerender(<AskCard ask={{ kind: 'gate', ref: 'g1', pending: true, role: 'ceo', name: 'ship' }} nameOf={nameOf} onDecide={onDecide} />)
    fireEvent.change(screen.getByLabelText('Note for the org'), { target: { value: 'not yet' } })
    fireEvent.click(screen.getByTestId('org-chat-deny'))
    expect(onDecide).toHaveBeenLastCalledWith(expect.objectContaining({ ref: 'g1' }), false, { note: 'not yet' })
  })

  it('disables the buttons while it cannot act, and shows how a resolved ask ended', () => {
    const { rerender } = render(<AskCard ask={{ kind: 'approval', ref: 'a1', request_id: 'a1', pending: true, role: 'dev', action: 'Bash' }} canAct={false} nameOf={nameOf} />)
    expect(screen.getByTestId('org-chat-approve')).toBeDisabled()
    rerender(<AskCard ask={{ kind: 'approval', ref: 'a1', request_id: 'a1', pending: true, role: 'dev', action: 'Bash' }} outcome={{ state: 'approved' }} nameOf={nameOf} />)
    expect(screen.getByTestId('org-chat-approval')).toHaveAttribute('data-pending', 'false')
    expect(screen.getByRole('status')).toHaveTextContent('approved')
  })
})

describe('AskCard: workflow Human-in-Loop', () => {
  it('shows what the workflow asks, and approves with the edited fields', () => {
    const onDecide = vi.fn()
    render(<AskCard ask={normalizeAsk(workflow)} nameOf={nameOf} onDecide={onDecide} />)
    expect(screen.getByTestId('org-chat-hil')).toHaveTextContent('Weekly outreach')
    expect(screen.getByTestId('ask-hil-readonly')).toHaveTextContent('@someone')
    fireEvent.click(screen.getByTestId('ask-approve'))
    expect(onDecide).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'hil-wf-1' }), true, {})
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Edited caption' } })
    fireEvent.click(screen.getByTestId('ask-approve'))
    expect(onDecide).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'hil-wf-1' }), true, { data: { caption: 'Edited caption' } })
  })

  it('rejects', () => {
    const onDecide = vi.fn()
    render(<AskCard ask={normalizeAsk(workflow)} nameOf={nameOf} onDecide={onDecide} />)
    fireEvent.click(screen.getByTestId('ask-reject'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-wf-1' }), false, {})
  })
})

describe('AskCard: always allow a send', () => {
  // A label recipient: a description that fits any send of its kind, which
  // the CLI refuses to always allow (ErrAlwaysLabel).
  const labelled = () => normalizeAsk({ ...sendEmail, recipient: { name: 'the reply-to address of each message', label: true } })

  it('offers Always allow for an address recipient and asks first, naming the destination', async () => {
    const onDecide = vi.fn()
    render(<><AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} /><ConfirmHost /></>)
    fireEvent.click(screen.getByTestId('ask-always-allow'))
    const dialog = await screen.findByRole('dialog', { name: 'Always allow this destination?' })
    expect(dialog).toHaveTextContent('This sends this email now, and from now on an agent\'s email to https://www.linkedin.com/in/sam-rivera from outlook · me@acme.test goes out without asking you.')
    fireEvent.click(screen.getByRole('button', { name: 'Send and always allow' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), true, { alwaysAllow: true }))
  })

  it('sends nothing when the confirmation is cancelled', async () => {
    const onDecide = vi.fn()
    render(<><AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} /><ConfirmHost /></>)
    fireEvent.click(screen.getByTestId('ask-always-allow'))
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(onDecide).not.toHaveBeenCalled()
  })

  it('keeps an edit: always allow sends the edited text', async () => {
    const onDecide = vi.fn()
    render(<><AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} /><ConfirmHost /></>)
    fireEvent.click(screen.getByTestId('ask-edit'))
    fireEvent.change(screen.getByTestId('ask-text-edit'), { target: { value: 'Hi Sam, short version.' } })
    fireEvent.click(screen.getByTestId('ask-always-allow'))
    fireEvent.click(await screen.findByRole('button', { name: 'Send and always allow' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(expect.anything(), true, { editText: 'Hi Sam, short version.', alwaysAllow: true }))
  })

  it('does not offer it for a label recipient, nor on a resolved card', () => {
    const { rerender } = render(<AskCard ask={labelled()} nameOf={nameOf} onDecide={vi.fn()} />)
    expect(screen.getByTestId('ask-approve')).toBeInTheDocument()
    expect(screen.queryByTestId('ask-always-allow')).toBeNull()
    rerender(<AskCard ask={send()} nameOf={nameOf} outcome={{ state: 'sent' }} onDecide={vi.fn()} />)
    expect(screen.queryByTestId('ask-always-allow')).toBeNull()
  })

  // A card too large to review is refused by the CLI however it is
  // approved (ErrUnreviewable), always-allow included.
  it('is disabled with Approve on a card too large to review, and asks nothing', () => {
    const onDecide = vi.fn()
    const ask = normalizeAsk({ ...sendEmail, textReadOnly: true, unreviewable: 'v holds more than 1000000 keys and elements: too large to review' })
    render(<><AskCard ask={ask} nameOf={nameOf} onDecide={onDecide} /><ConfirmHost /></>)
    expect(screen.getByTestId('ask-approve')).toBeDisabled()
    expect(screen.getByTestId('ask-always-allow')).toBeDisabled()
    fireEvent.click(screen.getByTestId('ask-always-allow'))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(onDecide).not.toHaveBeenCalled()
  })
})

describe('AskCard: send approval', () => {
  it('shows every copy and the rest of what an email carries, as it will be sent', () => {
    const ask = normalizeAsk({ ...sendEmail, details: [
      { key: 'cc', value: 'eve@example.test' }, { key: 'bcc', value: 'mallory@example.test' },
      { key: 'replyAll', value: 'yes' }, { key: 'connection', value: 'conn-new' }, { key: 'bcc', value: '' },
    ] })
    render(<AskCard ask={ask} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-detail-cc')).toHaveTextContent('eve@example.test')
    expect(screen.getByTestId('ask-detail-bcc')).toHaveTextContent('mallory@example.test')
    expect(screen.getAllByTestId('ask-detail-bcc')).toHaveLength(1)
    expect(screen.getByTestId('org-chat-send')).toHaveTextContent('Bcc')
    expect(screen.getByTestId('org-chat-send')).toHaveTextContent('Reply all')
    expect(screen.getByTestId('ask-detail-connection')).toHaveTextContent('conn-new')
  })

  // An Outlook reply goes to its To as well as to the thread.
  it("shows an Outlook reply's extra To as Also to", () => {
    const ask = normalizeAsk({ ...sendEmail, recipient: { name: "the thread's sender and Also to", label: true },
      details: [{ key: 'alsoTo', value: 'evil@example.test' }] })
    render(<AskCard ask={ask} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-detail-alsoTo')).toHaveTextContent('evil@example.test')
    expect(screen.getByTestId('org-chat-send')).toHaveTextContent('Also to')
  })

  // A draft's card can't show what sending it sends: reject only.
  it("refuses approval of a draft's card, and says why", () => {
    const ask = normalizeAsk({ ...sendEmail, unreviewable: "sending a draft sends the recipients, subject and text the draft holds in the mailbox, which this card can't show" })
    render(<AskCard ask={ask} nameOf={nameOf} onDecide={vi.fn()} />)
    expect(screen.getByTestId('ask-approve')).toBeDisabled()
    expect(screen.getByTestId('ask-unreviewable')).toHaveTextContent("Can't be reviewed here")
    expect(screen.getByTestId('ask-unreviewable')).toHaveTextContent('the draft holds in the mailbox')
  })

  it('shows the recipient with a profile link, the account, the exact text and who prepared it', () => {
    const onNavigate = vi.fn()
    render(<AskCard ask={send()} nameOf={nameOf} onNavigate={onNavigate} />)
    const card = screen.getByTestId('org-chat-send')
    expect(card).toHaveAttribute('aria-label', 'Approve sending email to Sam Rivera')
    expect(screen.getByTestId('ask-recipient')).toHaveTextContent('Sam Rivera')
    expect(screen.getByTestId('ask-account')).toHaveTextContent('outlook · me@acme.test')
    expect(card).toHaveTextContent('Release notes for v1.2')
    expect(screen.getByTestId('ask-text').textContent).toBe(sendEmail.text)
    // An agent names itself: the card shows that as its claim (#833 N4).
    expect(screen.getByTestId('ask-prepared-by')).toHaveTextContent('Prepared by an agent that says it is "Developer". Nothing is sent until you approve.')
    fireEvent.click(within(screen.getByTestId('ask-recipient')).getByText('profile'))
    expect(hilApi.openUrl).toHaveBeenCalledWith('https://www.linkedin.com/in/sam-rivera')
    fireEvent.click(within(screen.getByTestId('ask-recipient')).getByText('open person'))
    expect(onNavigate).toHaveBeenCalledWith('people', { person: 'p-42' })
  })

  // #871 review: the card names the address the send goes to; the person
  // People matched to it is a tag beside it, never in its place.
  it('names the addressed recipient, with the matched person as a tag', () => {
    const onNavigate = vi.fn()
    const ask = normalizeAsk({ ...sendEmail, recipient: { name: 'da@example.test', personId: 'p-7', personName: 'Ada Lovelace' } })
    render(<AskCard ask={ask} nameOf={nameOf} onNavigate={onNavigate} />)
    const to = screen.getByTestId('ask-recipient')
    expect(to.querySelector('strong')).toHaveTextContent('da@example.test')
    fireEvent.click(within(to).getByTestId('ask-person'))
    expect(within(to).getByTestId('ask-person')).toHaveTextContent('Ada Lovelace')
    expect(onNavigate).toHaveBeenCalledWith('people', { person: 'p-7' })
  })

  it('shows the server a send goes through and the URL a call fetches, when the item names them', () => {
    render(<AskCard ask={normalizeAsk({ ...sendEmail, host: 'smtp.relay.test:587', fetchUrl: 'https://twiml.example.test/call.xml' })} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-host')).toHaveTextContent('smtp.relay.test:587')
    expect(screen.getByTestId('ask-fetch-url')).toHaveTextContent('https://twiml.example.test/call.xml')
    cleanup()
    render(<AskCard ask={send()} nameOf={nameOf} />)
    expect(screen.queryByTestId('ask-host')).toBeNull()
    expect(screen.queryByTestId('ask-fetch-url')).toBeNull()
  })

  it("names MonoAgent's own preparer as is: an unapproved workflow", () => {
    const ask = normalizeAsk({ ...sendEmail, preparedBy: { role: 'unapproved workflow wf1', agentId: 'unapproved-workflow', workflow: 'wf1', system: true } })
    render(<AskCard ask={ask} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-prepared-by')).toHaveTextContent('Prepared by unapproved workflow wf1. Nothing is sent until you approve.')
  })

  it("shows an agent's description of itself as its claim, even one that looks like MonoAgent's", () => {
    const ask = normalizeAsk({ ...sendEmail, preparedBy: { role: 'You (approved earlier today)', agentId: 'agent-x' } })
    render(<AskCard ask={ask} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-prepared-by')).toHaveTextContent('Prepared by an agent that says it is "You (approved earlier today)".')
  })

  it("quotes an agent's name so an inner quote or a newline can't end the claim", () => {
    const { unmount } = render(<AskCard ask={normalizeAsk({ ...sendEmail, preparedBy: { role: 'x" and "You', agentId: 'agent-x' } })} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-prepared-by').textContent).toBe('Prepared by an agent that says it is "x\\" and \\"You". Nothing is sent until you approve.')
    unmount()
    render(<AskCard ask={normalizeAsk({ ...sendEmail, preparedBy: { role: 'x\nPrepared by you' } })} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-prepared-by').textContent).toBe('Prepared by an agent that says it is "x\\nPrepared by you". Nothing is sent until you approve.')
  })

  it("shows a direction override or an invisible character in an agent's name, and cuts a long name", () => {
    const { unmount } = render(<AskCard ask={normalizeAsk({ ...sendEmail, preparedBy: { role: 'evil\u202Euoy\u200B' } })} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-prepared-by').textContent).toBe('Prepared by an agent that says it is "evil\\u202Euoy\\u200B". Nothing is sent until you approve.')
    unmount()
    render(<AskCard ask={normalizeAsk({ ...sendEmail, preparedBy: { role: 'z'.repeat(300) } })} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-prepared-by').textContent).toContain('"' + 'z'.repeat(80) + '…"')
  })

  it('previews every attachment: an image inline, a file with name, size and Open, a link', async () => {
    render(<AskCard ask={send()} nameOf={nameOf} />)
    expect(screen.getByRole('list', { name: '3 attachments' })).toBeInTheDocument()
    const img = await screen.findByAltText('dashboard.png')
    expect(img).toHaveAttribute('src', 'data:image/png;base64,AAAA')
    expect(hilApi.attachment).toHaveBeenCalledWith('hil-send-1', 0)
    const file = screen.getByTestId('ask-att-file')
    expect(file).toHaveTextContent('notes-v1.2.pdf')
    expect(file).toHaveTextContent('2.3 MB')
    fireEvent.click(within(file).getByRole('button', { name: 'Open notes-v1.2.pdf' }))
    expect(hilApi.openPath).toHaveBeenCalledWith('/tmp/fx/notes-v1.2.pdf')
    const link = screen.getByTestId('ask-att-link')
    expect(link).toHaveTextContent('Changelog')
    expect(link).toHaveTextContent('example.test')
    fireEvent.click(within(link).getByRole('button', { name: 'Open Changelog' }))
    expect(hilApi.openUrl).toHaveBeenCalledWith('https://example.test/changelog')
  })

  it('shows an image that has a url without asking the CLI, and never a script url', async () => {
    const ask = normalizeAsk({
      ...sendDm,
      attachments: [...sendDm.attachments, { kind: 'link', name: 'bad', url: 'javascript:alert(1)' }, { kind: 'image', name: 'evil', url: 'file:///etc/passwd' }],
    })
    render(<AskCard ask={ask} nameOf={nameOf} />)
    expect(await screen.findByAltText('hello.jpg')).toHaveAttribute('src', 'https://img.example.test/hello.jpg')
    expect(hilApi.attachment).not.toHaveBeenCalled()
    expect(screen.getByTestId('ask-att-link')).not.toHaveTextContent('Open')
    expect(document.querySelector('img[src^="file:"]')).toBeNull()
  })

  it('says so when a preview cannot be loaded', async () => {
    hilApi.attachment.mockResolvedValue({ error: 'gone' })
    render(<AskCard ask={send()} nameOf={nameOf} />)
    expect(await screen.findByText('Preview unavailable')).toBeInTheDocument()
  })

  it('approves the text as prepared, without an edit', () => {
    const onDecide = vi.fn()
    render(<AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} />)
    fireEvent.click(screen.getByTestId('ask-approve'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), true, {})
  })

  it('edits the text first: Approve then sends the edited text', () => {
    const onDecide = vi.fn()
    render(<AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} />)
    expect(screen.queryByTestId('ask-text-edit')).toBeNull()
    fireEvent.click(screen.getByTestId('ask-edit'))
    expect(screen.getByTestId('ask-edit')).toHaveAttribute('aria-pressed', 'true')
    fireEvent.change(screen.getByTestId('ask-text-edit'), { target: { value: 'Hi Sam, short version.' } })
    expect(screen.getByText('Edited: approving sends this text.')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('ask-approve'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), true, { editText: 'Hi Sam, short version.' })
  })

  // A card for fields a step doesn't declare says what the step sends: no
  // Edit, and Approve sends it as shown (the CLI refuses an edit, #848).
  it("has no edit control for a card whose text isn't a message, and approves it as shown", () => {
    const onDecide = vi.fn()
    const text = 'trello card/update with fields it doesn\'t declare:\nupdateFields.zz_extra: "x"'
    render(<AskCard ask={normalizeAsk({ ...sendEmail, textReadOnly: true, text })} nameOf={nameOf} onDecide={onDecide} />)
    expect(screen.getByTestId('ask-text')).toHaveTextContent('updateFields.zz_extra: "x"')
    expect(screen.queryByTestId('ask-edit')).toBeNull()
    expect(screen.queryByTestId('ask-text-edit')).toBeNull()
    fireEvent.click(screen.getByTestId('ask-approve'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), true, {})
    cleanup()
    // One with no text at all is still approvable.
    render(<AskCard ask={normalizeAsk({ ...sendEmail, textReadOnly: true, text: '' })} nameOf={nameOf} />)
    expect(screen.getByTestId('ask-approve')).toBeEnabled()
  })

  // The card never edits a subject (the CLI refuses one where the op has no
  // subject field, #848): it is shown, and Edit opens the text only.
  it('shows the subject without an edit control, and approves an edit with the text only', () => {
    const onDecide = vi.fn()
    render(<AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} />)
    expect(screen.getByText('Release notes for v1.2').tagName).toBe('DD')
    fireEvent.click(screen.getByTestId('ask-edit'))
    expect(screen.getAllByRole('textbox')).toEqual([screen.getByTestId('ask-text-edit')])
    fireEvent.change(screen.getByTestId('ask-text-edit'), { target: { value: 'Short.' } })
    fireEvent.click(screen.getByTestId('ask-approve'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), true, { editText: 'Short.' })
  })

  // A card too large to review (the CLI refuses its approve, #887) can be
  // rejected, not approved, and says why.
  it('refuses approval of a card too large to review, and says why', () => {
    const onDecide = vi.fn()
    const ask = normalizeAsk({ ...sendEmail, textReadOnly: true, unreviewable: 'v holds more than 1000000 keys and elements: too large to review' })
    render(<AskCard ask={ask} nameOf={nameOf} onDecide={onDecide} />)
    expect(screen.getByTestId('ask-approve')).toBeDisabled()
    expect(screen.getByTestId('ask-unreviewable')).toHaveTextContent('more than 1000000 keys')
    fireEvent.click(screen.getByTestId('ask-reject'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), false, {})
  })

  it('does not approve an empty text', () => {
    render(<AskCard ask={send()} nameOf={nameOf} />)
    fireEvent.click(screen.getByTestId('ask-edit'))
    fireEvent.change(screen.getByTestId('ask-text-edit'), { target: { value: '   ' } })
    expect(screen.getByTestId('ask-approve')).toBeDisabled()
  })

  it('rejects', () => {
    const onDecide = vi.fn()
    render(<AskCard ask={send()} nameOf={nameOf} onDecide={onDecide} />)
    fireEvent.click(screen.getByTestId('ask-reject'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ id: 'hil-send-1' }), false, {})
  })

  it('locks while a decision is in flight and shows the refusal', () => {
    const { rerender } = render(<AskCard ask={send()} nameOf={nameOf} busy />)
    expect(screen.getByTestId('ask-approve')).toBeDisabled()
    expect(screen.getByTestId('ask-reject')).toBeDisabled()
    rerender(<AskCard ask={send()} nameOf={nameOf} outcome={{ error: 'account is signed out' }} />)
    expect(screen.getByRole('alert')).toHaveTextContent('account is signed out')
    expect(screen.getByTestId('ask-approve')).not.toBeDisabled()
  })

  it('shows a sent item read-only, without buttons', async () => {
    render(<AskCard ask={normalizeAsk({ ...sendEmail, state: 'approved' })} nameOf={nameOf} />)
    expect(screen.queryByTestId('ask-approve')).toBeNull()
    expect(screen.getByTestId('org-chat-send')).toHaveAttribute('data-pending', 'false')
    expect(screen.getByTestId('ask-resolved')).toHaveTextContent('approved')
    await waitFor(() => expect(screen.getByAltText('dashboard.png')).toBeInTheDocument())
  })
})
