// monoagent://<kind>/<id> links in chat messages: a model-written message may
// point at a place in this app. Hostile input: the link is parsed strictly
// against an allowlist of kinds, the target is looked up in the real backend
// (profile-scoped, like chatArtifacts.resolveArtifact) before it is shown as
// clickable, and it is looked up again at click time. chatLinks.classifyUrl is
// untouched: these never reach the browser or the disk, only navigate().
import { defaultUrlTransform } from 'react-markdown'
import { TOOL_LINK_PAGES } from './toolLinks.js'

// The Settings sections navData.section scrolls to (pages/Settings.jsx).
export const SETTINGS_SECTIONS = ['health', 'jev', 'version']
const PAGES = new Set(Object.values(TOOL_LINK_PAGES))
// `run` is a workflow execution, same as `execution`.
const KIND_ALIAS = { workflow: 'workflow', execution: 'execution', run: 'execution', org: 'org', person: 'person', task: 'task', document: 'document', settings: 'settings', page: 'page' }
const ID = /^[A-Za-z0-9_-]{1,64}$/

function decodeOrgName(raw) {
  if (!/^[A-Za-z0-9_.~%-]{1,300}$/.test(raw)) return ''
  let name
  try { name = decodeURIComponent(raw) } catch { return '' }
  if (name.length === 0 || name.length > 128 || /[\u0000-\u001f\u007f/\\]/.test(name) || name !== name.trim()) return ''
  return name
}

// parseAppLink: {kind, id} for a well-formed app link, else null. Nothing is
// trimmed, case-folded or repaired.
export function parseAppLink(href) {
  if (typeof href !== 'string') return null
  const m = /^monoagent:\/\/([a-z]+)\/([^/?#]*)$/.exec(href)
  if (!m || !Object.hasOwn(KIND_ALIAS, m[1])) return null
  const kind = KIND_ALIAS[m[1]]
  const raw = m[2]
  if (kind === 'org') {
    const id = decodeOrgName(raw)
    return id ? { kind, id } : null
  }
  if (kind === 'task') return /^[1-9]\d{0,8}$/.test(raw) ? { kind, id: raw } : null
  if (kind === 'settings') return SETTINGS_SECTIONS.includes(raw) ? { kind, id: raw } : null
  if (kind === 'page') return PAGES.has(raw) ? { kind, id: raw } : null
  return ID.test(raw) ? { kind, id: raw } : null
}

// appLinkTarget: the navigate(page, data) arguments for a parsed link.
export function appLinkTarget({ kind, id }) {
  switch (kind) {
    case 'workflow': return { page: 'noderunner', data: { workflowId: id } }
    case 'execution': return { page: 'noderunner', data: { executionId: id } }
    case 'org': return { page: 'orgs', data: { org: id } }
    case 'person': return { page: 'people', data: { person: id } }
    case 'task': return { page: 'tasks', data: null }
    case 'document': return { page: 'documents', data: null }
    case 'settings': return { page: 'settings', data: { section: id } }
    default: return { page: id, data: null } // 'page': id is already in the allowlist
  }
}

// Whether the target is real is a backend question; a lookup that throws, a
// missing binding, or an answer about some other id all mean "not real".
async function exists({ kind, id }, { api, tasksApi }) {
  if (kind === 'workflow') { const w = await api.getWorkflow(id); return !!w && w.id === id }
  if (kind === 'execution') { const e = await api.getExecutionDetail(id); return !!e && e.id === id }
  if (kind === 'org') {
    const res = await api.listOrgDesigns()
    return (Array.isArray(res) ? res : (res?.items || [])).some(o => o?.name === id)
  }
  if (kind === 'person') { const p = await api.getPersonDetail(id); return !!p && p.id === id }
  if (kind === 'document') { const d = await api.getProfileDocument(id); return !!d && d.id === id }
  if (kind === 'task') { const r = await tasksApi.show(Number(id)); return !!r && !r.error && r.task?.id === Number(id) }
  return false
}

// needsLookup: settings sections and page ids are fixed allowlists.
export function needsLookup(link) { return !!link && link.kind !== 'settings' && link.kind !== 'page' }

// resolveAppLink: {page, data} when the link points at something that exists
// now, else null. `deps` is {api, tasksApi} (passed in, as resolveArtifact does).
export async function resolveAppLink(link, deps) {
  if (!link) return null
  if (needsLookup(link)) {
    let ok = false
    try { ok = await exists(link, deps) } catch { ok = false }
    if (!ok) return null
  }
  return appLinkTarget(link)
}

// react-markdown drops any scheme it does not know; let a well-formed app link
// through and leave every other URL to its default rule.
export function appLinkUrlTransform(url) {
  return parseAppLink(url) ? url : defaultUrlTransform(url)
}

// Bare mentions in prose. Only forms whose id can be validated: `task #N`
// (a board number) and `run|workflow <uuid>` (workflow and execution ids are
// UUIDs). A mention is only ever a candidate; ChatAppLink looks it up and
// leaves it as plain text when nothing real answers.
const UUID_RE = '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
const MENTION_RE = new RegExp(`(?<![\\w#/.-])(?:[Tt]ask #([1-9]\\d{0,8})|(run|workflow) (${UUID_RE}))(?![\\w-])`, 'g')

export function findAppMentions(text) {
  const out = []
  for (const m of text.matchAll(MENTION_RE)) {
    const href = m[1] ? `monoagent://task/${m[1]}` : `monoagent://${m[2] === 'run' ? 'execution' : 'workflow'}/${m[3]}`
    out.push({ start: m.index, end: m.index + m[0].length, href })
  }
  return out
}

// remarkChatAppMentions turns those mentions in plain text into link nodes
// marked data-chat-mention; ChatMarkdown's link renderer draws the ones that
// resolve as app chips and everything else as the text it was.
export function remarkChatAppMentions() {
  const skip = new Set(['code', 'inlineCode', 'link', 'linkReference', 'definition', 'html', 'image'])
  const walk = (node) => {
    if (!node.children) return
    const next = []
    for (const child of node.children) {
      if (skip.has(child.type)) { next.push(child); continue }
      if (child.type !== 'text') { walk(child); next.push(child); continue }
      const hits = findAppMentions(child.value)
      if (hits.length === 0) { next.push(child); continue }
      let at = 0
      for (const h of hits) {
        if (h.start > at) next.push({ type: 'text', value: child.value.slice(at, h.start) })
        next.push({ type: 'link', url: h.href, children: [{ type: 'text', value: child.value.slice(h.start, h.end) }], data: { hProperties: { 'data-chat-mention': 'true' } } })
        at = h.end
      }
      if (at < child.value.length) next.push({ type: 'text', value: child.value.slice(at) })
    }
    node.children = next
  }
  return (tree) => walk(tree)
}
