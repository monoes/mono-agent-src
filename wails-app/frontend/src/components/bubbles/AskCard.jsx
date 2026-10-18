import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, X, HelpCircle, ShieldQuestion, Flag, Loader, Send, Pencil, FileText, Link2, ExternalLink, User, Workflow } from 'lucide-react'
import { ChatMarkdown } from '../chat/ChatMarkdown.jsx'
import { formatBytes, safeImageSrc, safeUrl } from '../../lib/askModel.js'
import * as hil from '../../services/hilApi.js'
import { askHilRef } from '../../lib/commsStatus.js'
import './asks.css'
import { quoteClaim } from '../../lib/quoteClaim.js'

const mono = 'var(--font-mono)'

function Muted({ children, style }) {
  return <div style={{ fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', ...style }}>{children}</div>
}

// ResolvedLine says how an ask ended, and who ended it when that isn't the
// person.
function ResolvedLine({ ask, outcome }) {
  const { t } = useTranslation()
  const state = outcome?.state || ask.resolution || (!ask.pending ? ask.state : '')
  if (!state) return null
  const by = ask.resolved_by && ask.resolved_by !== 'human' ? t('orgBubble.resolvedBy', { by: ask.resolved_by }) : ''
  return (
    <Muted style={{ marginTop: 4 }}>
      <span role="status" data-testid="ask-resolved">
        {outcome?.already ? t('orgBubble.already', { state: t(`orgBubble.state.${state}`, { defaultValue: state }) }) : t(`orgBubble.state.${state}`, { defaultValue: state })}
        {by && ` · ${by}`}
      </span>
    </Muted>
  )
}

function ActionError({ outcome }) {
  if (!outcome?.error) return null
  return <div role="alert" style={{ marginTop: 4, fontFamily: mono, fontSize: 9.5, color: 'var(--red, #ef4444)' }}>{outcome.error}</div>
}

function Frame({ ask, pending, icon: Icon, title, label, children }) {
  return (
    <div className="ask-card" role="group" aria-label={label || title} data-testid={`org-chat-${ask.kind}`} data-ask-kind={ask.kind} data-pending={pending ? 'true' : 'false'}>
      <div className="ask-head">
        <Icon size={11} aria-hidden="true" /> <span>{title}</span>
      </div>
      {children}
    </div>
  )
}

function QuestionBody({ ask, nameOf, canAct, busy, outcome, onAnswer, onDismiss }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const pending = ask.pending && !outcome?.state
  const submit = () => { if (text.trim()) onAnswer?.(ask, text.trim()) }
  return (
    <Frame ask={ask} pending={pending} icon={HelpCircle} title={t('orgBubble.asks', { name: nameOf(ask.role) })}>
      <div style={{ marginTop: 4 }}><ChatMarkdown content={ask.text || ''} /></div>
      {ask.answer && <Muted style={{ marginTop: 4 }}>{t('orgBubble.yourAnswer', { answer: ask.answer })}</Muted>}
      {pending ? (
        <div className="ask-actions">
          <input type="text" className="ask-input" value={text} onChange={e => setText(e.target.value)} disabled={!canAct || busy}
            onKeyDown={e => { if (e.key === 'Enter' && !e.nativeEvent?.isComposing) submit() }}
            aria-label={t('orgBubble.answerLabel')} placeholder={t('orgBubble.answerPlaceholder')} />
          <button type="button" className="btn btn-primary btn-sm" data-testid="org-chat-answer" disabled={!canAct || busy || !text.trim()} onClick={submit}>
            {busy ? <Loader size={11} className="spin" /> : t('orgBubble.answer')}
          </button>
          {ask.blocking && (
            <button type="button" className="btn btn-ghost btn-sm" data-testid="org-chat-dismiss" disabled={busy}
              title={t('orgBubble.dismissHint')} onClick={() => onDismiss?.(ask)}>
              {t('orgBubble.dismiss')}
            </button>
          )}
        </div>
      ) : <ResolvedLine ask={ask} outcome={outcome} />}
      <ActionError outcome={outcome} />
    </Frame>
  )
}

function DecisionBody({ ask, nameOf, canAct, busy, outcome, onDecide }) {
  const { t } = useTranslation()
  const [note, setNote] = useState('')
  const pending = ask.pending && !outcome?.state
  const isGate = ask.kind === 'gate'
  const title = isGate
    ? t('orgBubble.gateTitle', { name: nameOf(ask.role), gate: ask.name || ask.ref })
    : t('orgBubble.approvalTitle', { name: nameOf(ask.role), action: ask.action })
  return (
    <Frame ask={ask} pending={pending} icon={isGate ? Flag : ShieldQuestion} title={title}>
      {ask.text && <div style={{ marginTop: 4 }}><ChatMarkdown content={ask.text} /></div>}
      {pending ? (
        <div className="ask-actions">
          {isGate && (
            <input type="text" className="ask-input" value={note} onChange={e => setNote(e.target.value)} disabled={!canAct || busy}
              aria-label={t('orgBubble.noteLabel')} placeholder={t('orgBubble.notePlaceholder')} />
          )}
          <button type="button" className="btn btn-primary btn-sm" data-testid="org-chat-approve" disabled={!canAct || busy}
            onClick={() => onDecide?.(ask, true, { note: note.trim() })} style={{ gap: 4 }}>
            {busy ? <Loader size={11} className="spin" /> : <Check size={11} />} {t('orgBubble.approve')}
          </button>
          <button type="button" className="btn btn-ghost btn-sm" data-testid="org-chat-deny" disabled={!canAct || busy}
            onClick={() => onDecide?.(ask, false, { note: note.trim() })} style={{ gap: 4 }}>
            <X size={11} /> {isGate ? t('orgBubble.reject') : t('orgBubble.deny')}
          </button>
        </div>
      ) : <ResolvedLine ask={ask} outcome={outcome} />}
      <ActionError outcome={outcome} />
    </Frame>
  )
}

// HilBody is a workflow's Human-in-Loop item: what the workflow shows
// read-only, its editable fields, Approve with the edits, or Reject.
function HilBody({ ask, canAct, busy, outcome, onDecide, onNavigate }) {
  const { t } = useTranslation()
  const [values, setValues] = useState(() => ({ ...ask.editable }))
  const pending = ask.pending && !outcome?.state
  const readonly = Object.entries(ask.readonly || {})
  const fields = Object.keys(ask.editable || {})
  const edited = fields.some(k => values[k] !== ask.editable[k])
  return (
    <Frame ask={ask} pending={pending} icon={Workflow} title={t('ask.hilTitle', { workflow: ask.workflowName, node: ask.nodeName })}>
      {onNavigate && (ask.hilRef || ask.personId) && (
        <Muted style={{ marginBottom: 4 }}>
          {ask.personId && <button type="button" className="ask-link" data-testid="ask-recipient-link" onClick={() => onNavigate('people', { person: ask.personId })}>{t('comms.recipient')}</button>}
          {ask.personId && ask.hilRef && ' · '}
          {ask.hilRef && <button type="button" className="ask-link" data-testid="ask-see-comms" onClick={() => onNavigate('communications', { hil_ref: ask.hilRef })}>{t('comms.seeInComms')}</button>}
        </Muted>
      )}
      {readonly.length > 0 && (
        <dl className="ask-facts" data-testid="ask-hil-readonly">
          {readonly.map(([k, v]) => (
            <div key={k}><dt>{k}</dt><dd>{typeof v === 'string' ? v : JSON.stringify(v)}</dd></div>
          ))}
        </dl>
      )}
      {fields.map(k => (
        <label key={k} className="ask-field">
          <span>{k}</span>
          <textarea rows={2} value={typeof values[k] === 'string' ? values[k] : JSON.stringify(values[k])} disabled={!pending || !canAct || busy}
            onChange={e => setValues(v => ({ ...v, [k]: e.target.value }))} />
        </label>
      ))}
      {pending ? (
        <div className="ask-actions">
          <button type="button" className="btn btn-primary btn-sm" data-testid="ask-approve" disabled={!canAct || busy}
            onClick={() => onDecide?.(ask, true, edited ? { data: values } : {})} style={{ gap: 4 }}>
            {busy ? <Loader size={11} className="spin" /> : <Check size={11} />} {t('orgBubble.approve')}
          </button>
          <button type="button" className="btn btn-ghost btn-sm" data-testid="ask-reject" disabled={!canAct || busy}
            onClick={() => onDecide?.(ask, false, {})} style={{ gap: 4 }}>
            <X size={11} /> {t('orgBubble.reject')}
          </button>
        </div>
      ) : <ResolvedLine ask={ask} outcome={outcome} />}
      <ActionError outcome={outcome} />
    </Frame>
  )
}

// Attachment previews. An image shows inline (its url, else the CLI's
// preview of attachment n); a file shows name, size and Open; a link shows
// where it goes and opens in the browser. Nothing but http(s) is linked.
function ImagePreview({ ask, att, index }) {
  const { t } = useTranslation()
  const direct = safeImageSrc(att.url)
  const [src, setSrc] = useState(direct)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (direct || !att.path) return undefined
    let live = true
    hil.attachment(ask.id, index).then(res => {
      if (!live) return
      const s = safeImageSrc(res?.dataUrl)
      if (s) setSrc(s)
      else setFailed(true)
    })
    return () => { live = false }
  }, [ask.id, index, att.path, direct])
  return (
    <figure className="ask-att ask-att-image" data-testid="ask-att-image">
      {src && !failed
        ? <img src={src} alt={att.name || t('ask.imageAlt')} onError={() => setFailed(true)} />
        : <div className="ask-att-missing"><FileText size={14} aria-hidden="true" /> {failed ? t('ask.previewFailed') : t('ask.previewLoading')}</div>}
      <figcaption>
        <span className="ask-att-name">{att.name || t('ask.imageAlt')}</span>
        {att.size > 0 && <span> · {formatBytes(att.size)}</span>}
      </figcaption>
    </figure>
  )
}

function FileAttachment({ att }) {
  const { t } = useTranslation()
  return (
    <div className="ask-att ask-att-file" data-testid="ask-att-file">
      <FileText size={14} aria-hidden="true" />
      <span className="ask-att-name">{att.name}</span>
      {att.size > 0 && <span className="ask-att-meta">{formatBytes(att.size)}</span>}
      {att.path && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => hil.openPath(att.path)}
          aria-label={t('ask.openNamed', { name: att.name })} style={{ gap: 4, fontSize: 10 }}>
          <ExternalLink size={10} /> {t('ask.open')}
        </button>
      )}
    </div>
  )
}

function LinkAttachment({ att }) {
  const { t } = useTranslation()
  const url = safeUrl(att.url)
  let host = ''
  try { host = new URL(url).host } catch { /* shown without a host */ }
  return (
    <div className="ask-att ask-att-link" data-testid="ask-att-link">
      <Link2 size={14} aria-hidden="true" />
      <span className="ask-att-name">{att.name || host || att.url}</span>
      {host && <span className="ask-att-meta">{host}</span>}
      {url && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => hil.openUrl(url)}
          aria-label={t('ask.openNamed', { name: att.name || host })} style={{ gap: 4, fontSize: 10 }}>
          <ExternalLink size={10} /> {t('ask.open')}
        </button>
      )}
    </div>
  )
}

function Attachments({ ask }) {
  const { t } = useTranslation()
  if (!ask.attachments?.length) return null
  return (
    <div className="ask-atts" role="list" aria-label={t('ask.attachments', { count: ask.attachments.length })}>
      {ask.attachments.map((att, i) => (
        <div role="listitem" key={`${att.name || att.url || att.path}-${i}`}>
          {att.kind === 'image' ? <ImagePreview ask={ask} att={att} index={i} />
            : att.kind === 'link' ? <LinkAttachment att={att} />
              : <FileAttachment att={att} />}
        </div>
      ))}
    </div>
  )
}

// SendBody is the approval of an outbound message (an email, a DM, a
// comment, a post...) an agent prepared but may not send: who it goes to,
// from which account, the exact text, every attachment, and who prepared it.
// Edit makes the text editable; Approve then sends the text as shown. A
// card whose text says what the step sends, not a message (textReadOnly),
// has no Edit: there is nothing to edit, and approving sends it as shown.
function SendBody({ ask, nameOf, canAct, busy, outcome, onDecide, onNavigate }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(ask.text)
  // A re-previewed text (the backend changed it) replaces what was typed.
  useEffect(() => { setText(ask.text) }, [ask.text])
  const pending = ask.pending && !outcome?.state
  const edited = text !== ask.text
  const rcpt = ask.recipient || {}
  const profile = safeUrl(rcpt.profileUrl)
  const acct = [ask.account?.platform, ask.account?.handle].filter(Boolean).join(' · ')
  const preparer = ask.preparedBy?.role ? nameOf(ask.preparedBy.role) : ''
  const channel = t(`ask.channel.${ask.channel}`, { defaultValue: ask.channel || ask.action })
  return (
    <Frame ask={ask} pending={pending} icon={Send}
      title={t('ask.sendTitle', { channel })}
      label={t('ask.sendLabel', { channel, name: rcpt.name || '' })}>
      <dl className="ask-facts">
        <div>
          <dt>{t('ask.to')}</dt>
          <dd data-testid="ask-recipient">
            <User size={10} aria-hidden="true" /> <strong>{rcpt.name || t('ask.unknownRecipient')}</strong>
            {profile && (
              <> · <button type="button" className="ask-link" onClick={() => hil.openUrl(profile)}>{t('ask.profile')}</button></>
            )}
            {rcpt.personId && onNavigate && (
              <> · <button type="button" className="ask-link" onClick={() => onNavigate('people', { person: rcpt.personId })}>{t('ask.person')}</button></>
            )}
            {onNavigate && askHilRef(ask) && (
              <> · <button type="button" className="ask-link" data-testid="ask-see-comms" onClick={() => onNavigate('communications', { hil_ref: askHilRef(ask) })}>{t('comms.seeInComms')}</button></>
            )}
          </dd>
        </div>
        {acct && <div><dt>{t('ask.from')}</dt><dd data-testid="ask-account">{acct}</dd></div>}
        {ask.subject && <div><dt>{t('ask.subject')}</dt><dd>{ask.subject}</dd></div>}
      </dl>
      {editing && pending && !ask.textReadOnly ? (
        <textarea className="ask-text-edit" data-testid="ask-text-edit" rows={Math.min(12, Math.max(4, text.split('\n').length + 1))}
          value={text} onChange={e => setText(e.target.value)} disabled={busy}
          aria-label={t('ask.textLabel')} />
      ) : (
        <pre className="ask-text" data-testid="ask-text" aria-label={t('ask.textLabel')} tabIndex={0}>{text}</pre>
      )}
      {edited && pending && <Muted style={{ marginTop: 2 }}>{t('ask.edited')}</Muted>}
      <Attachments ask={ask} />
      {/* An agent names itself, so its name is its claim, JSON-quoted as the
          browser cards quote one (requesterParts): an inner quote or a newline
          can't end it. Only MonoAgent's own label (preparedBy.system: an
          unapproved workflow) is shown as is. */}
      {preparer && (
        <Muted style={{ marginTop: 4 }}>
          <span data-testid="ask-prepared-by">{ask.preparedBy?.system
            ? t('ask.preparedBy', { name: preparer })
            : t('ask.preparedByClaim', { name: quoteClaim(preparer) })}</span>
        </Muted>
      )}
      {pending ? (
        <div className="ask-actions">
          <button type="button" className="btn btn-primary btn-sm" data-testid="ask-approve" disabled={!canAct || busy || (!ask.textReadOnly && !text.trim())}
            onClick={() => onDecide?.(ask, true, edited ? { editText: text } : {})} style={{ gap: 4 }}>
            {busy ? <Loader size={11} className="spin" /> : <Check size={11} />} {t('orgBubble.approve')}
          </button>
          {!ask.textReadOnly && (
            <button type="button" className="btn btn-ghost btn-sm" data-testid="ask-edit" aria-pressed={editing} disabled={busy}
              onClick={() => setEditing(e => !e)} style={{ gap: 4 }}>
              <Pencil size={11} /> {editing ? t('ask.doneEditing') : t('ask.edit')}
            </button>
          )}
          <button type="button" className="btn btn-ghost btn-sm" data-testid="ask-reject" disabled={!canAct || busy}
            onClick={() => onDecide?.(ask, false, {})} style={{ gap: 4 }}>
            <X size={11} /> {t('orgBubble.reject')}
          </button>
        </div>
      ) : <ResolvedLine ask={ask} outcome={outcome} />}
      <ActionError outcome={outcome} />
    </Frame>
  )
}

// AskCard is the one card for everything a person is asked in a bubble,
// coder or org: a question, a tool approval, a gate, a workflow's
// Human-in-Loop item and the approval of a send. It only renders: the
// caller decides how an answer reaches the CLI.
//   ask      an item (lib/askModel.js, or an `org chat history` item)
//   onAnswer(ask, text)  onDismiss(ask)  onDecide(ask, approve, {note, editText, data})
export function AskCard({ ask, nameOf = id => id, canAct = true, busy = false, outcome, onAnswer, onDismiss, onDecide, onNavigate }) {
  const shared = { ask, nameOf, canAct, busy, outcome }
  switch (ask.kind) {
    case 'question': return <QuestionBody {...shared} onAnswer={onAnswer} onDismiss={onDismiss} />
    case 'approval':
    case 'gate': return <DecisionBody {...shared} onDecide={onDecide} />
    case 'hil': return <HilBody {...shared} onDecide={onDecide} onNavigate={onNavigate} />
    case 'send': return <SendBody {...shared} onDecide={onDecide} onNavigate={onNavigate} />
    default: return null
  }
}
