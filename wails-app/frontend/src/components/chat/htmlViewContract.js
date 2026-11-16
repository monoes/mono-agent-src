// Contract for the sandboxed HTML view (spike). Model output is hostile: the
// frame can say anything, so every message is validated here, host-side.

export const FLAG_KEY = 'chat.htmlViews'
export const MAX_HTML_BYTES = 64 * 1024
export const MAX_SEND_CHARS = 4000
export const MIN_HEIGHT = 40
export const MAX_HEIGHT = 600
export const GESTURE_TTL_MS = 2000
export const RATE_WINDOW_MS = 1000
export const RATE_MAX = 30

// Default OFF. Anything but the exact string 'true' is off.
export function htmlViewsEnabled() {
  try { return localStorage.getItem(FLAG_KEY) === 'true' } catch { return false }
}

export const CSP = [
  "default-src 'none'", "style-src 'unsafe-inline'", "script-src 'unsafe-inline'",
  'img-src data:', "connect-src 'none'", "frame-src 'none'", "form-action 'none'",
  "base-uri 'none'", "object-src 'none'",
].join('; ')

// The bridge is the only API the frame gets. It runs inside the opaque-origin
// frame, so it is NOT a trust boundary: hostile scripts can post whatever they
// like. The host re-validates everything.
const BRIDGE = `(function(){
function post(m){try{parent.postMessage(m,'*')}catch(e){}}
var host=Object.freeze({
resize:function(h){post({v:1,t:'resize',h:Math.round(Number(h))})},
send:function(s){post({v:1,t:'send',text:String(s)})},
open:function(u){post({v:1,t:'open',href:String(u)})}
});
Object.defineProperty(window,'host',{value:host});
document.addEventListener('click',function(e){if(e.isTrusted)post({v:1,t:'gesture'})},true);
})();`

// CSP meta is the very first element, before the bridge and any model markup.
export function buildSrcdoc(html) {
  return '<!doctype html><html><head>'
    + `<meta http-equiv="Content-Security-Policy" content="${CSP}">`
    + `<meta charset="utf-8"><script>${BRIDGE}</script></head><body>${html}</body></html>`
}

export function htmlByteLength(s) {
  return new TextEncoder().encode(String(s)).length
}

export function htmlTooLarge(html) {
  return htmlByteLength(html) > MAX_HTML_BYTES
}

const APP_LINK = /^monoagent:\/\/[a-z][a-z0-9-]{0,31}\/[A-Za-z0-9._:-]{1,128}$/

export function isAppLinkHref(href) {
  return typeof href === 'string' && APP_LINK.test(href)
}

const KEYS = {
  resize: ['v', 't', 'h'], send: ['v', 't', 'text'], open: ['v', 't', 'href'], gesture: ['v', 't'],
}

// Returns a normalized message or null. Strict: plain object, exact keys.
export function validateFrameMessage(d) {
  if (d === null || typeof d !== 'object' || Array.isArray(d)) return null
  if (Object.getPrototypeOf(d) !== Object.prototype) return null
  if (d.v !== 1 || typeof d.t !== 'string' || !Object.hasOwn(KEYS, d.t)) return null
  const keys = Object.keys(d)
  const want = KEYS[d.t]
  if (keys.length !== want.length || !keys.every((k) => want.includes(k))) return null
  switch (d.t) {
    case 'resize':
      if (!Number.isFinite(d.h)) return null
      return { t: 'resize', h: Math.min(MAX_HEIGHT, Math.max(MIN_HEIGHT, Math.round(d.h))) }
    case 'send':
      if (typeof d.text !== 'string' || !d.text.trim() || d.text.length > MAX_SEND_CHARS) return null
      return { t: 'send', text: d.text }
    case 'open':
      if (!isAppLinkHref(d.href)) return null
      return { t: 'open', href: d.href }
    default:
      return { t: 'gesture' }
  }
}

// Host-side gate: source check, validation, rate limit, one-shot gestures.
// A frame-sent 'gesture' is spoofable by hostile script, so it only counts
// while the host itself saw the pointer over the frame (pointerOver()).
export function createHostGate({ getFrameWindow, pointerOver = () => true, now = Date.now }) {
  let stamps = []
  let gestureAt = -Infinity
  return function handle(event) {
    const fw = getFrameWindow()
    if (!fw || event.source !== fw) return null
    const t = now()
    stamps = stamps.filter((s) => t - s < RATE_WINDOW_MS)
    if (stamps.length >= RATE_MAX) return null
    stamps.push(t)
    const m = validateFrameMessage(event.data)
    if (!m) return null
    if (m.t === 'gesture') { if (pointerOver()) gestureAt = t; return null }
    if (m.t === 'resize') return m
    const fresh = t - gestureAt <= GESTURE_TTL_MS
    gestureAt = -Infinity // consumed (or stale): a replay needs a new click
    return fresh ? m : null
  }
}
