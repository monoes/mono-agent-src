import { LIM, Bad, depthOk } from './schema.js'

// The block type is the first word of the fence info string ("language-x"
// from react-markdown's className). Exact, lowercase.
export function infoFromClassName(className) {
  const m = /(?:^|\s)language-(\S+)/.exec(typeof className === 'string' ? className : '')
  return m ? m[1] : ''
}

// parseBlock -> { status, ... }
//   none       not a block type: render the ordinary code block
//   invalid    too big / too deep / fails the schema: ordinary code block
//   incomplete JSON does not parse (yet): a skeleton while streaming
//   ok         { type, data } validated, safe to render
// registry: { [type]: (json) => data } where the validator throws Bad.
export function parseBlock(info, body, registry) {
  if (!Object.prototype.hasOwnProperty.call(registry, info)) return { status: 'none' }
  if (typeof body !== 'string' || body.length > LIM.body) return { status: 'invalid' }
  let json
  try { json = JSON.parse(body) } catch { return { status: 'incomplete' } }
  if (!depthOk(json)) return { status: 'invalid' }
  try {
    return { status: 'ok', type: info, data: registry[info](json) }
  } catch (e) {
    if (e instanceof Bad) return { status: 'invalid' }
    throw e
  }
}
