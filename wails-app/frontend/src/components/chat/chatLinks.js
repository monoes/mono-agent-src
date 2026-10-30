// What a chat message may make clickable. A model writes these messages, so
// every URL and path is untrusted: the checks here decide what is offered,
// and the Go side (app_open.go) checks again before touching the disk.

// isUserGesture: a click really made by the user. Events a script dispatches
// (el.click(), dispatchEvent) have isTrusted false; a native button's
// Enter/Space produces a trusted click. Defence in depth: the renderer has no
// path for a message to run script, but nothing that touches the disk or the
// browser should act on a synthetic event. A seam for tests (jsdom events are
// never trusted).
export function isUserGesture(e) { return !!e && e.isTrusted === true }

// classifyUrl: only an absolute http(s) URL without credentials is openable.
// Returns { ok, url, host } — host is the real target, for the tooltip.
export function classifyUrl(href) {
  if (typeof href !== 'string' || href === '' || href !== href.trim()) return { ok: false }
  if (!/^https?:\/\/[^/\\]/i.test(href)) return { ok: false } // WHATWG would repair 'https:///x' into host x
  let u
  try { u = new URL(href) } catch { return { ok: false } }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return { ok: false }
  if (u.username !== '' || u.password !== '') return { ok: false }
  if (u.hostname === '') return { ok: false }
  return { ok: true, url: u.href, host: u.host }
}

// asciiHost: the punycode form of a host-looking string, '' when it is not one.
function asciiHost(h) {
  try { return new URL('http://' + h).hostname.toLowerCase() } catch { return '' }
}

const DOMAIN_LIKE = /(?:[\p{L}\p{N}][\p{L}\p{N}-]*\.)+\p{L}[\p{L}\p{N}-]+/gu

// disguisedHost: the host a link's visible text claims when that is not the
// host it opens ('bank.com' written over a link to evil.test), else ''. Any
// domain-like piece of the text counts, in any script, with other words
// around it; so does a word that mixes ASCII and other letters (a lookalike
// such as Cyrillic "а" in "bаnk"). Returned in its ASCII (punycode) form
// where it is one, for display.
export function disguisedHost(text, href) {
  const real = classifyUrl(href)
  if (!real.ok || typeof text !== 'string') return ''
  const actual = new URL(real.url).hostname.toLowerCase()
  for (const m of text.matchAll(DOMAIN_LIKE)) {
    const shown = asciiHost(m[0])
    if (shown && shown !== actual && !actual.endsWith('.' + shown)) return shown
  }
  for (const word of text.split(/[^\p{L}\p{N}]+/u)) {
    if (/[A-Za-z]/.test(word) && /[^\x00-\x7f]/.test(word) && /\p{L}/u.test(word.replace(/[\x00-\x7f]/g, ''))) return asciiHost(word) || word
  }
  return ''
}

const EXTS = 'md|markdown|txt|go|js|jsx|ts|tsx|mjs|cjs|json|yaml|yml|toml|py|rs|sh|html|css|csv|log|png|jpg|jpeg|gif|svg|webp|pdf|sql|xml|ini|conf|mod|sum|lock|java|rb|c|h|cpp'
const SEG = '[\\w.@+%~-]+'
// A path token: ~/…, /abs/…, ./rel, ../rel, C:\win\…, or dir/file.ext — with an
// optional :line[:col]. Plain text only matches when it ends in a known
// extension, so prose with slashes ("and/or") stays prose.
const PATH_RE = new RegExp(
  '(?<![\\w/@:.~\\\\-])' +
  '(?:' +
    '(?:~|\\.{1,2})?/(?:' + SEG + '/)*' + SEG + '\\.(?:' + EXTS + ')' +
    '|(?:[A-Za-z]:[\\\\/])(?:' + SEG + '[\\\\/])*' + SEG + '\\.(?:' + EXTS + ')' +
    '|(?:' + SEG + '/)+' + SEG + '\\.(?:' + EXTS + ')' +
  ')(?::\\d+(?::\\d+)?)?(?![\\w/-])', 'g')

// isNetworkOrDevicePath: forms Windows reads as a UNC share, a device or a
// drive-relative path. Mirrors refuseNetworkOrDevicePath in app_open.go: they
// are never looked up, so rendering a message cannot reach out to a host.
export function isNetworkOrDevicePath(p) {
  if (/^[\\/][\\/]/.test(p)) return true
  if (p.includes('\\\\?\\') || p.includes('\\\\.\\') || p.includes('\\??\\')) return true
  return /^[A-Za-z]:(?![\\/])/.test(p)
}

// Windows reserved device names; mirrors reservedDeviceNames in app_open.go.
const RESERVED = new Set(['CON', 'PRN', 'AUX', 'NUL', 'CONIN$', 'CONOUT$',
  ...[1, 2, 3, 4, 5, 6, 7, 8, 9].flatMap((n) => ['COM' + n, 'LPT' + n]),
  ...['\u00b9', '\u00b2', '\u00b3'].flatMap((n) => ['COM' + n, 'LPT' + n])])

// isReservedDeviceName: Windows ignores case, trailing dots and spaces and
// anything from the first dot on when it decides a name is a device.
export function isReservedDeviceName(component) {
  const dot = component.indexOf('.')
  const stem = (dot >= 0 ? component.slice(0, dot) : component).replace(/[ .]+$/, '')
  return RESERVED.has(stem.toUpperCase())
}

// refusedPathSpelling: device names in any component and ':' anywhere but a
// drive prefix (alternate data streams), on every OS. Mirrors
// refuseStreamOrReservedName in app_open.go; `p` has no :line[:col] suffix.
// 8.3 short names (PROGRA~1) are accepted: they stay inside the chat-folder
// check the Go side applies after resolution.
function refusedPathSpelling(p) {
  const rest = /^.:/.test(p) ? p.slice(2) : p
  if (rest.includes(':')) return true
  return rest.split(/[\\/]/).some((c) => c !== '' && isReservedDeviceName(c))
}

// findPathTokens lists the path-looking tokens in plain text.
export function findPathTokens(text) {
  if (typeof text !== 'string' || text.length > 20000) return []
  const out = []
  for (const m of text.matchAll(PATH_RE)) {
    // a URL's tail ("//host/x.html" after "https:") is not a path
    // "file.md:stream" is an alternate data stream, not file.md followed by prose
    if (/^:[^\s]/.test(text.slice(m.index + m[0].length, m.index + m[0].length + 2))) continue
    if (text[m.index - 1] === ':' || isNetworkOrDevicePath(m[0]) || refusedPathSpelling(splitLine(m[0]).path)) continue
    out.push({ start: m.index, end: m.index + m[0].length, token: m[0] })
  }
  return out
}

// splitLine separates a trailing :line[:col].
export function splitLine(token) {
  const m = /^(.+?):(\d+)(?::\d+)?$/.exec(token)
  return m ? { path: m[1], line: Number(m[2]) } : { path: token, line: 0 }
}

// codeLooksLikePath: whether an inline `code` span is worth looking up. More
// permissive than plain text (a folder like `~/projects/x` qualifies), still
// one token with no shell metacharacters.
export function codeLooksLikePath(s) {
  if (typeof s !== 'string' || s.length === 0 || s.length > 300) return false
  if (/[\s<>|*?"'`$;&(){}]/.test(s) || s.startsWith('-')) return false
  const { path } = splitLine(s)
  if (isNetworkOrDevicePath(s) || isNetworkOrDevicePath(path) || refusedPathSpelling(path)) return false
  if (/^(~|\.{1,2})?\/\S+/.test(path) || /^[A-Za-z]:[\\/]\S+/.test(path)) return path !== '/' && !path.startsWith('//')
  return new RegExp('^(?:' + SEG + '/)*' + SEG + '\\.(?:' + EXTS + ')$').test(path)
}

// remarkChatPaths turns path-looking pieces of plain text into inline-code
// nodes marked data-chat-path-text; ChatMarkdown renders the ones that exist
// as buttons and everything else as the text it was.
export function remarkChatPaths() {
  const skip = new Set(['code', 'inlineCode', 'link', 'linkReference', 'definition', 'html', 'image'])
  const walk = (node) => {
    if (!node.children) return
    const next = []
    for (const child of node.children) {
      if (skip.has(child.type)) { next.push(child); continue }
      if (child.type !== 'text') { walk(child); next.push(child); continue }
      const hits = findPathTokens(child.value)
      if (hits.length === 0) { next.push(child); continue }
      let at = 0
      for (const h of hits) {
        if (h.start > at) next.push({ type: 'text', value: child.value.slice(at, h.start) })
        next.push({ type: 'inlineCode', value: h.token, data: { hProperties: { 'data-chat-path-text': 'true' } } })
        at = h.end
      }
      if (at < child.value.length) next.push({ type: 'text', value: child.value.slice(at) })
    }
    node.children = next
  }
  return (tree) => walk(tree)
}
