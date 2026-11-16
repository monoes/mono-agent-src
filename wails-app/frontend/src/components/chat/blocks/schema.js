// Strict validation primitives for chat blocks (spec:
// docs/mastermind/specs/2026-11-16-chat-blocks.md). The JSON in a block is
// hostile: every validator throws Bad on anything off-spec, and validate()
// turns that into null so the block falls back to the ordinary code block.

export const LIM = { body: 16384, depth: 6, str: 200 }

export class Bad extends Error {}
export const bad = (m) => { throw new Bad(m) }

// Control characters, DEL and bidi overrides never reach the screen.
// eslint-disable-next-line no-control-regex
const NOISE = /[\u0000-\u001f\u007f‪-‮⁦-⁩]/g
const has = (o, k) => Object.prototype.hasOwnProperty.call(o, k)

export function obj(v) {
  if (v === null || typeof v !== 'object' || Array.isArray(v)) bad('object expected')
  return v
}

// opt: the key's value when present and not null/undefined, else undefined.
export function opt(o, k) {
  return has(o, k) && o[k] !== null && o[k] !== undefined ? o[k] : undefined
}

export function str(v, { max = LIM.str, re } = {}) {
  if (typeof v !== 'string') bad('string expected')
  if (v.length > max) bad('string too long')
  const s = v.replace(NOISE, ' ').trim()
  if (!s) bad('empty string')
  if (re && !re.test(s)) bad('string format')
  return s
}

export const optStr = (o, k, spec) => { const v = opt(o, k); return v === undefined ? undefined : str(v, spec) }

export function num(v) {
  if (typeof v !== 'number' || !Number.isFinite(v)) bad('finite number expected')
  return v
}

export function bool(v) {
  if (typeof v !== 'boolean') bad('boolean expected')
  return v
}

export function oneOf(v, list) {
  if (!list.includes(v)) bad('unexpected value')
  return v
}

export function arr(v, min, max) {
  if (!Array.isArray(v)) bad('array expected')
  if (v.length < min || v.length > max) bad('array length')
  return v
}

const LINK = /^monoagent:\/\/[a-z][a-z-]{0,23}\/[A-Za-z0-9._:-]{1,120}$/
export function link(v) {
  if (typeof v !== 'string' || !LINK.test(v)) bad('app link expected')
  return v
}
export const optLink = (o, k) => { const v = opt(o, k); return v === undefined ? undefined : link(v) }

export function unique(items, key) {
  const seen = new Set()
  for (const it of items) {
    if (seen.has(it[key])) bad('duplicate ' + key)
    seen.add(it[key])
  }
}

// depthOk: nesting depth of a parsed JSON value is at most max (early exit).
export function depthOk(v, max = LIM.depth, d = 1) {
  if (v === null || typeof v !== 'object') return true
  if (d > max) return false
  for (const x of Array.isArray(v) ? v : Object.values(v)) if (!depthOk(x, max, d + 1)) return false
  return true
}
