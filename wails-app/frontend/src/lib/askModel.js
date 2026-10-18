// The one model behind AskCard (coder-bubble v2): every ask a person can
// answer in a bubble, whatever it came from. Org chat history gives
// question, approval and gate items; `hil list` gives workflow HIL items
// (kind "hil" here) and the send-approval items of Point 3 (kind "send").
// Pure data in, data out.

// normalizeAsk turns a `hil list` row into an ask, or null for junk.
// Workflow HIL rows have no kind of their own; a send item carries
// kind:"send", a state and who prepared it.
export function normalizeAsk(raw) {
  if (!raw || typeof raw !== 'object' || !raw.id) return null
  // `hil list` nests a send item's payload under .send (internal/hilsend).
  if (raw.kind === 'send' && raw.send && typeof raw.send === 'object') raw = { ...raw.send, ...raw, ...raw.send, id: raw.id }
  if (raw.kind === 'send') {
    const state = raw.state || 'pending'
    return {
      kind: 'send', id: raw.id, ref: raw.id, pending: state === 'pending', state,
      role: raw.preparedBy?.role || '', preparedBy: raw.preparedBy || {},
      action: raw.action || '', channel: raw.channel || '',
      account: raw.account || {}, recipient: raw.recipient || {},
      subject: raw.subject || '', text: String(raw.text ?? ''), textReadOnly: !!raw.textReadOnly,
      attachments: Array.isArray(raw.attachments) ? raw.attachments : [],
      org: raw.org || raw.preparedBy?.org || '', conversationId: raw.conversationId || raw.preparedBy?.conversationId || '',
      hilRef: raw.hil_ref || '', personId: raw.person_id || raw.recipient?.personId || '',
    }
  }
  const state = raw.status || raw.state || 'pending'
  return {
    kind: 'hil', id: raw.id, ref: raw.id, pending: state === 'pending', state,
    workflowName: raw.workflow_name || raw.workflow_id || '', nodeName: raw.node_name || raw.node_id || '',
    readonly: raw.readonly_data || {}, editable: raw.editable_data || {},
    org: raw.node_config?.org || raw.org || '', conversationId: raw.conversationId || '',
    role: '', suggestion: raw.suggestion || raw.node_config?.suggestion || null,
    hilRef: raw.hil_ref || '', personId: raw.person_id || '',
  }
}

export function normalizeAsks(rows) {
  return (Array.isArray(rows) ? rows : []).map(normalizeAsk).filter(Boolean)
}

// asksFor keeps the asks that belong in one bubble. An ask tagged with an
// org or conversation belongs to that one only. An untagged send item
// belongs to the org bubble whose roles include the role that prepared it;
// untagged send items otherwise show in coder bubbles. Untagged workflow
// HIL items belong to no bubble (they stay on the Human in Loop page).
export function asksFor(asks, { org = '', conversationId = '', roleIds = [] } = {}) {
  return (asks || []).filter(a => {
    if (a.org) return a.org === org
    if (a.conversationId) return a.conversationId === conversationId
    if (org) return a.kind === 'send' && !!a.role && roleIds.includes(a.role)
    return !!conversationId && a.kind === 'send'
  })
}

export function pendingCount(asks) {
  return (asks || []).filter(a => a.pending).length
}

// formatBytes: 48213 → "47 KB", 2411724 → "2.3 MB".
export function formatBytes(n) {
  const v = Number(n)
  if (!Number.isFinite(v) || v < 0) return ''
  if (v < 1024) return `${v} B`
  const units = ['KB', 'MB', 'GB']
  let x = v / 1024
  let i = 0
  while (x >= 1024 && i < units.length - 1) { x /= 1024; i += 1 }
  return `${x.toFixed(x >= 10 ? 0 : 1)} ${units[i]}`
}

// safeUrl returns url when it is http(s), else ''. Anything else (file:,
// javascript:) is never linked or opened.
export function safeUrl(url) {
  try {
    const u = new URL(String(url || ''))
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.toString() : ''
  } catch { return '' }
}

// safeImageSrc allows http(s) and data:image/ sources for inline previews.
export function safeImageSrc(src) {
  const s = String(src || '')
  if (/^data:image\/(png|jpe?g|gif|webp|avif|bmp);base64,/i.test(s)) return s
  return safeUrl(s)
}
