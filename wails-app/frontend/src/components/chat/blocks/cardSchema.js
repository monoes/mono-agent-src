import { obj, str, optStr, optLink, opt, oneOf, arr } from './schema.js'

export const MAX_LIST_ITEMS = 24
export const TONES = ['ok', 'warn', 'error', 'info', 'neutral']

// One entity: {kind, title, subtitle?, status?, tone?, link?}
export function validateEntity(j) {
  const o = obj(j)
  const tone = opt(o, 'tone')
  return {
    kind: str(o.kind, { max: 24 }),
    title: str(o.title),
    subtitle: optStr(o, 'subtitle'),
    status: optStr(o, 'status', { max: 40 }),
    tone: tone === undefined ? 'neutral' : oneOf(tone, TONES),
    link: optLink(o, 'link'),
  }
}

export const validateCard = validateEntity

// list: {title?, items:[entity]}
export function validateList(j) {
  const o = obj(j)
  return { title: optStr(o, 'title'), items: arr(o.items, 1, MAX_LIST_ITEMS).map(validateEntity) }
}
