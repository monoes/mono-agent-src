// Block nesting is recursive in the Markdown parser and the renderer: a few
// KB of "> > > …" or "- - - …" freezes the UI for seconds and overflows the
// stack. A model writes chat messages and the files they name, so nesting
// beyond what real text uses is refused up front and shown as plain text.

export const MAX_BLOCK_DEPTH = 32

// markdownTooDeep: whether any line opens more than MAX_BLOCK_DEPTH block
// containers (">" quotes and list markers, plus 2 columns of indentation per
// level before a list marker).
export function markdownTooDeep(text) {
  if (typeof text !== 'string') return false
  for (const line of text.split('\n')) {
    let i = 0
    let depth = 0
    let width = 0
    while (i < line.length) {
      const c = line[i]
      if (c === ' ') { width++; i++; continue }
      if (c === '\t') { width += 4; i++; continue }
      let n = 0
      if (c === '>') n = 1
      else if ((c === '-' || c === '*' || c === '+') && (line[i + 1] === ' ' || line[i + 1] === '\t')) n = 1
      else if (c >= '0' && c <= '9') {
        let j = i
        while (j < line.length && j - i < 10 && line[j] >= '0' && line[j] <= '9') j++
        if ((line[j] === '.' || line[j] === ')') && (line[j + 1] === ' ' || line[j + 1] === '\t')) { n = 1; i = j }
      }
      if (n === 0) break
      depth += 1 + Math.floor(width / 2)
      width = 0
      if (depth > MAX_BLOCK_DEPTH) return true
      i++
    }
  }
  return false
}
