// The media vault holds images, audio and video and, since vault.save, files
// the extension saved (PDF, text, Markdown, CSV). Only images have a thumbnail
// or can be picked as an image; audio and video play in the detail view; the
// rest show as files.

// The image extensions the vault treats as images (internal/imagescan
// AllowedExtensions).
const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico'])

// isVaultImage reports whether a vault item is an image, by its stored file name.
export function isVaultImage(item) {
  const name = String(item?.filename || item?.path || '')
  const dot = name.lastIndexOf('.')
  return dot >= 0 && IMAGE_EXTENSIONS.has(name.slice(dot + 1).toLowerCase())
}

// The audio and video extensions (internal/imagescan AudioExtensions and
// VideoExtensions).
const AUDIO_EXTENSIONS = new Set(['mp3', 'wav', 'm4a', 'aac', 'ogg', 'oga', 'opus', 'flac'])
const VIDEO_EXTENSIONS = new Set(['mp4', 'webm', 'mov', 'm4v', 'mkv', 'ogv'])

// vaultKind is 'image', 'audio' or 'video' for a media item, '' for any other
// file. The CLI sends it as item.kind; an item without one is judged by name.
export function vaultKind(item) {
  if (item?.kind) return item.kind
  const name = String(item?.filename || item?.path || '')
  const dot = name.lastIndexOf('.')
  const ext = dot >= 0 ? name.slice(dot + 1).toLowerCase() : ''
  if (IMAGE_EXTENSIONS.has(ext)) return 'image'
  if (AUDIO_EXTENSIONS.has(ext)) return 'audio'
  if (VIDEO_EXTENSIONS.has(ext)) return 'video'
  return ''
}

// vaultFileKind is a short label for a non-image item ("PDF", "MD"), or "FILE".
export function vaultFileKind(item) {
  const name = String(item?.filename || '')
  const dot = name.lastIndexOf('.')
  const ext = dot >= 0 ? name.slice(dot + 1).toUpperCase() : ''
  return ext && ext.length <= 4 ? ext : 'FILE'
}
