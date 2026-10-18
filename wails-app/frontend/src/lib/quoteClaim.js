// quoteClaim shows what an agent says about itself (a name, a role) as a
// quoted string a reader can't mistake for the card's own words: JSON
// quoting escapes quotes and newlines, the characters that change how text
// reads without being seen (Unicode format and control characters, line and
// paragraph separators and every space but the ASCII one: U+202E
// right-to-left override, U+200B zero-width space, U+0085, U+2028, U+3000)
// are shown as \uXXXX, or \u{XXXXX} above U+FFFF (a tag character such as
// U+E0041). So are variation selectors (U+FE0F), private-use characters
// (U+E000) and unassigned code points and noncharacters (U+10FFFF), which
// can carry text a reader doesn't see; other combining marks stay, since
// scripts such as Devanagari and Thai need them, and so does one U+FE0F
// right after an emoji or a keycap's digit (❤️, 1️⃣), which only picks how
// that character looks, and a zero-width joiner between two emoji
// (👨‍👩‍👧, 🏳️‍🌈), which only joins them into one. A claim longer than max
// characters is cut with "…".
const emoji = (ch) => /^\p{Extended_Pictographic}$/u.test(ch ?? '')

// emojiBefore: the character before at, past a presentation selector or a
// skin tone, is an emoji.
function emojiBefore(all, at) {
  const cs = Array.from(all.slice(Math.max(0, at - 6), at))
  while (cs.length && /^[\uFE0F\u{1F3FB}-\u{1F3FF}]$/u.test(cs[cs.length - 1])) cs.pop()
  return emoji(cs.pop())
}

export function quoteClaim(s, max = 80) {
  let text = String(s ?? '')
  const chars = Array.from(text)
  if (chars.length > max) text = chars.slice(0, max).join('') + '…'
  return JSON.stringify(text).replace(/[\p{Cf}\p{Cc}\p{Co}\p{Cn}\p{Zl}\p{Zp}\p{Variation_Selector}]|(?! )\p{Zs}/gu,
    (c, at, all) => {
      if (c === '\uFE0F') {
        const prev = Array.from(all.slice(Math.max(0, at - 2), at)).pop() ?? ''
        if (/^[\p{Extended_Pictographic}0-9#*]$/u.test(prev)) return c
      }
      if (c === '\u200D' && emojiBefore(all, at) && emoji(Array.from(all.slice(at + 1, at + 3))[0])) return c
      const hex = c.codePointAt(0).toString(16).toUpperCase()
      return hex.length > 4 ? '\\u{' + hex + '}' : '\\u' + hex.padStart(4, '0')
    })
}
