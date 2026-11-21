import { obj, str, optStr, opt, bool, arr, unique } from './schema.js'

const ID = /^[A-Za-z0-9_-]{1,40}$/

// {question?, options:[{id,label,hint?}], multi?}
export function validateChoices(j) {
  const o = obj(j)
  const options = arr(o.options, 2, 12).map((x) => {
    const p = obj(x)
    return { id: str(p.id, { re: ID, max: 40 }), label: str(p.label), hint: optStr(p, 'hint') }
  })
  unique(options, 'id')
  const multi = opt(o, 'multi')
  return { question: optStr(o, 'question'), options, multi: multi === undefined ? false : bool(multi) }
}

// The user message an answer is sent as (spec: "<question>: <label>", labels
// joined with ", " for multi).
export function answerText(data, labels) {
  const joined = labels.join(', ')
  return data.question ? `${data.question}: ${joined}` : joined
}
