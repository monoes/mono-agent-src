// The notes of a mission task end with one machine line the planner wrote:
// "[mission-meta] {json}" (the same format as internal/dynorg ParseTaskMeta:
// the LAST line, the prefix, then one JSON object). People read the text above
// it; the line itself is never shown raw, never edited, and always written back.

const PREFIX = '[mission-meta] '

// splitNotes: {text, meta, line}. text is what a person reads and edits; meta
// is the parsed object (null when there is none, or an empty one is worth no
// display); line is the original meta line, kept verbatim for joinNotes. A
// last line that does not parse as a JSON object stays in the text.
export function splitNotes(notes) {
  const all = String(notes ?? '')
  const lines = all.replace(/[\n ]+$/, '').split('\n')
  const last = lines[lines.length - 1]
  if (!last.startsWith(PREFIX)) return { text: all, meta: null, line: '' }
  let m
  try { m = JSON.parse(last.slice(PREFIX.length)) } catch { return { text: all, meta: null, line: '' } }
  if (!m || typeof m !== 'object' || Array.isArray(m)) return { text: all, meta: null, line: '' }
  const text = lines.slice(0, -1).join('\n').replace(/\s+$/, '')
  return { text, meta: Object.keys(m).length ? m : null, line: last }
}

// joinNotes puts the kept meta line back under the edited text, in the shape
// the planner writes it (text, a blank line, the line).
export function joinNotes(text, line) {
  if (!line) return text
  const body = String(text ?? '').replace(/\s+$/, '')
  return body ? `${body}\n\n${line}` : line
}

// metaFacts: the files, size and risk of a meta object, as plain strings.
export function metaFacts(meta) {
  if (!meta) return { files: [], size: '', risk: '' }
  const files = Array.isArray(meta.files) ? meta.files.filter(f => typeof f === 'string' && f.trim()) : []
  const one = (v) => (typeof v === 'string' || typeof v === 'number' ? String(v) : '')
  return { files, size: one(meta.size), risk: one(meta.risk) }
}
