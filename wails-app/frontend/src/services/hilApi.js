// The ONE place the bubbles talk to the human-in-loop queue (`monoagentcli
// hil ...`). AskCard, useHilAsks and the dock only call these functions. Every function
// resolves to data or {error}; none throws.
import { api } from './api.js'
import { normalizeAsks, safeUrl } from '../lib/askModel.js'
import { emitCommsChanged } from '../lib/commsStatus.js'
import i18nOr from '../lib/i18nOr.js'

// listAsks returns the profile's pending asks (workflow HIL and send items),
// normalized (lib/askModel.js). A failed read is an empty list, not an
// error: the next poll tries again.
export async function listAsks() {
  try {
    return normalizeAsks(await api.hilList())
  } catch {
    return []
  }
}

// resolve approves or rejects ask `id`. approve: true with editText sends
// the edited text instead of the prepared one (send items), and with
// alwaysAllow also sends to that destination without asking from now on;
// data holds the edited fields of a workflow HIL item. Resolves
// {ok:true,state} or {error}.
export async function resolve(id, { approve, editText, data, kind, alwaysAllow } = {}) {
  if (!id) return { error: i18nOr('ui6PagesMisc.no_id') }
  try {
    // A send item is resolved by the operator through `hil resolve`, which
    // performs exactly the previewed payload (with the edit applied) once.
    const res = kind === 'send'
      ? await api.hilSendResolve(id, !!approve, { editText: editText || '', ...(approve && alwaysAllow ? { alwaysAllow: true } : {}) })
      : await api.hilResolve(id, { approve: !!approve, editText, data })
    if (res?.error) return { error: res.error }
    emitCommsChanged() // the Communications row for this item changed state
    return { ok: true, state: res?.state || (approve ? 'approved' : 'rejected') }
  } catch (err) {
    return { error: String(err?.message || err) }
  }
}

// attachment returns attachment n of send item id for an inline preview:
// {dataUrl, mime, name, size} or {error}.
export async function attachment(id, n) {
  try {
    const dataUrl = await api.hilSendAttachmentData(id, n)
    if (typeof dataUrl !== 'string' || !dataUrl.startsWith('data:')) return { error: i18nOr('ui6PagesMisc.unavailable') }
    return { dataUrl, mime: dataUrl.slice(5, dataUrl.indexOf(';')) }
  } catch (err) {
    return { error: String(err?.message || err) }
  }
}

// openPath opens a local file in the OS viewer; openUrl a web link in the
// browser (http/https only).
export async function openPath(path) {
  if (!path) return false
  try { await api.openPathWithOS(path); return true } catch { return false }
}

export async function openUrl(url) {
  const safe = safeUrl(url)
  if (!safe) return false
  try { await api.openURL(safe); return true } catch { return false }
}
