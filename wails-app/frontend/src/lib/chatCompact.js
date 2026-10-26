// Compact (summarize, then continue in a fresh conversation) shared by the
// assistant panel and the coder bubbles.

export const COMPACT_PROMPT = 'Summarize our conversation so far so we can continue in a fresh context: the goal, decisions made, key facts and file paths, and what is still open. Reply with the summary only.'

// compactSummary is the text of a finished compact turn, or '' when the turn
// did not complete or said nothing.
export function compactSummary(turn) {
  if (!turn || turn.terminal?.status !== 'completed') return ''
  return (turn.parts || []).filter(p => p.kind === 'text').map(p => p.text).join('\n').trim()
}

// compactSeed is the first message of the conversation a compact starts.
export function compactSeed(summary) {
  return `Summary of our earlier conversation, carried over to continue in a fresh context:\n\n${summary}\n\nAcknowledge in one short line and wait for my next instruction.`
}
