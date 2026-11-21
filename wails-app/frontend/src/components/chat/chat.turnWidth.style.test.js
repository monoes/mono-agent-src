// A turn's children must not outgrow the turn: .chat-assistant-turn is a flex column with align-items: flex-start, which sizes
// each child to its max-content, so one long unbreakable line (an html-view's source, a wide table) pushed the whole message,
// and the controls at its right edge, off the chat column. jsdom computes no layout, so the rule is read from the stylesheet.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

describe('chat turn width', () => {
  const css = readFileSync(fileURLToPath(new URL('./chat.css', import.meta.url)), 'utf8')
  it('caps every child of a turn at the turn width and lets it shrink', () => {
    const rule = /\.chat-assistant-turn\s*>\s*\*\s*\{([^}]*)\}/.exec(css)
    expect(rule).not.toBeNull()
    expect(rule[1]).toMatch(/max-width:\s*100%/)
    expect(rule[1]).toMatch(/min-width:\s*0/)
  })
})
