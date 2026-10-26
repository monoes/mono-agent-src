// Mission templates (plan 10, Phase 6): prompt skeletons a person fills in
// before a coding chat plans a mission. Pure constants; nothing here runs a
// task or talks to the CLI. The text is what the lead reads, so it stays
// English; the labels are translated (bubbles.mission.template.<id>).

const skeleton = (goalHint, extra = []) => [
  `Goal: ${goalHint}`,
  'Constraints: (what must not change, tools or libraries to use or avoid)',
  ...extra,
  'Acceptance: (how we will know it is done: tests that pass, behaviour to see)',
  'Non-goals: (what this mission must not touch)',
].join('\n')

export const MISSION_TEMPLATES = [
  { id: 'feature', text: skeleton('(the feature, in one sentence)', ['Users and flows: (who uses it and how)']) },
  { id: 'bugfix', text: skeleton('(the bug, in one sentence)', ['Reproduction: (steps, and what you see versus what you expect)']) },
  { id: 'refactor', text: skeleton('(the code to restructure and the shape you want)', ['Behaviour to keep: (tests that must stay green)']) },
  { id: 'research', text: skeleton('(the question to answer)', ['Sources: (code, docs or sites to read)', 'Deliverable: (a written report, a comparison, a recommendation)']) },
  { id: 'migration', text: skeleton('(what moves, from what to what)', ['Order and rollback: (steps in order, how to undo each)']) },
]

// templateText is a template's skeleton, or '' for an unknown id.
export const templateText = (id) => MISSION_TEMPLATES.find(t => t.id === id)?.text || ''

// insertTemplate puts a template into the composer: it starts an empty draft,
// and goes after what is already typed (kept as the goal's first line) otherwise.
export function insertTemplate(draft, id) {
  const text = templateText(id)
  if (!text) return draft
  const d = String(draft || '').trim()
  return d ? `${d}\n\n${text}` : text
}

const VERBS = /\b(implement|build|create|add|migrate|refactor|rewrite|redesign|port|integrate|set up|investigate|research|fix all|overhaul)\b/i

// looksLikeMission is a cheap guess that a first message is a big job: long,
// or a work verb with some substance. It only decides whether the template
// picker is offered; it never starts anything.
export function looksLikeMission(text) {
  const s = String(text || '').trim()
  if (!s) return false
  const words = s.split(/\s+/).length
  if (words >= 40) return true
  return words >= 12 && VERBS.test(s)
}
