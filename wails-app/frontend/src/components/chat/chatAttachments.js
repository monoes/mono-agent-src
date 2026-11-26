// Attachments on coder chats (C4): the files the composer pastes, drops or
// picks are read into memory until the turn is sent (then saved to the
// chat spool and passed as `chat --attach`), and the image paths a tool
// result names are shown from the chat's folder.
import i18nOr from '../../lib/i18nOr.js'

export const MAX_ATTACHMENTS = 10
export const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024

const MIME_EXT = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp', 'image/gif': 'gif' }

let nextId = 0

export function isImage(mime) {
  return typeof mime === 'string' && mime.startsWith('image/')
}

export function formatBytes(n) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

// readAttachment reads one File into { id, name, mime, size, dataUrl,
// base64 }. A pasted image has no name of its own; it gets one from its type.
export function readAttachment(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const dataUrl = String(reader.result || '')
      const mime = file.type || 'application/octet-stream'
      resolve({
        id: `att-${++nextId}`,
        name: file.name || `pasted.${MIME_EXT[mime] || 'bin'}`,
        mime,
        size: file.size,
        dataUrl,
        base64: dataUrl.slice(dataUrl.indexOf(',') + 1),
      })
    }
    reader.onerror = () => reject(reader.error || new Error(i18nOr('ui7Chat.file_unread')))
    reader.readAsDataURL(file)
  })
}

// takeFiles reads the files that fit next to `count` already attached: at
// most MAX_ATTACHMENTS in all, each at most MAX_ATTACHMENT_BYTES. skipped
// says why each other one was left out.
export async function takeFiles(count, files) {
  const added = []
  const skipped = []
  for (const f of Array.from(files || [])) {
    const name = f.name || i18nOr('ui7Chat.pasted_file')
    if (count + added.length >= MAX_ATTACHMENTS) {
      skipped.push(i18nOr('ui7Chat.file_too_many', { name, max: MAX_ATTACHMENTS }))
    } else if (f.size > MAX_ATTACHMENT_BYTES) {
      skipped.push(i18nOr('ui7Chat.file_too_big', { name }))
    } else {
      try {
        // eslint-disable-next-line no-await-in-loop
        added.push(await readAttachment(f))
      } catch (err) {
        skipped.push(`${name}: ${err?.message || err}`)
      }
    }
  }
  return { added, skipped }
}

const IMAGE_EXT = /\.(png|jpe?g|webp)$/i
// An absolute path ending in an image extension, in plain text (not a URL:
// its slashes follow a colon or a slash).
const ABSOLUTE_IMAGE_PATH = /(?:^|[\s"'`(=])((?:[A-Za-z]:)?[/\\][^\s"'`()<>|]*\.(?:png|jpe?g|webp))(?![\w-])/gi
const MAX_RESULT_IMAGES = 4

// imagePathsInResult lists the image files a tool result names (at most
// four): the string values of a JSON result (`browser screenshot --json`
// prints {"path": …}), else the absolute paths in its text.
export function imagePathsInResult(text) {
  if (typeof text !== 'string' || !/\.(png|jpe?g|webp)/i.test(text)) return []
  const found = []
  const add = p => { if (!found.includes(p) && found.length < MAX_RESULT_IMAGES) found.push(p) }
  let json
  try { json = JSON.parse(text) } catch { json = undefined }
  if (json && typeof json === 'object') {
    const walk = (v, depth) => {
      if (typeof v === 'string') { if (IMAGE_EXT.test(v)) add(v) } else if (v && typeof v === 'object' && depth < 4) {
        for (const x of Object.values(v)) walk(x, depth + 1)
      }
    }
    walk(json, 0)
    return found
  }
  for (const m of text.matchAll(ABSOLUTE_IMAGE_PATH)) add(m[1])
  return found
}

export function baseName(p) {
  return String(p || '').split(/[\\/]/).pop()
}
