# Chat blocks: declarative visuals in chat

Status: spec for lane B (renderer). A later lane teaches models the syntax.

## Idea

A model embeds a visual by writing a fenced code block whose info string is the
block type and whose body is one JSON object. Trusted React components render
it. No code from the message is ever executed, no HTML, no iframes, no remote
images. It is plain message text, so it works for every runtime and replays
from history unchanged.

    ```choices
    {"question":"Which branch?","options":[{"id":"a","label":"main"},{"id":"b","label":"dev","hint":"unstable"}]}
    ```

Block types (info string, exact, lowercase): `choices`, `chart`, `table`,
`card`, `list`, `steps`, `form`.

## Fallback (the safety rule)

A block that is unknown, too big, not valid JSON, or fails validation renders
as the ordinary code block, byte for byte. Nothing partial is ever rendered.
While the message is still streaming, a block that does not parse yet shows a
skeleton instead (no error, no flicker); if it is still invalid when the turn
ends it becomes the ordinary code block.

## Global limits

- Body at most 16384 characters; JSON nesting depth at most 6.
- Every string is rendered as plain text, at most 200 characters (control
  characters removed). Longer string means invalid.
- Unknown extra keys are ignored. Wrong types, missing required keys, or any
  count over a cap make the whole block invalid.
- `link` values must match `monoagent://<kind>/<id>`: kind `[a-z][a-z-]{0,23}`,
  id `[A-Za-z0-9._:-]{1,120}`. Any other URL (http, javascript, data, ...) is
  invalid. Links render as buttons, never as anchors.
- No images; no URL in any field is fetched or rendered as media.

## Actions

Blocks cannot run anything. The only effects are:
- send a user message (`choices`, `form`), through the chat's send. Where the
  chat cannot send right now (turn streaming, no conversation, read-only
  view) the block renders disabled with a note.
- open an app link (`link` on `table` rows, `card`, `list` items) through the
  host's optional open-link handler; a no-op when absent.

An answered `choices` or `form` block shows the given answer and is locked.
The answer is stored locally (localStorage) and shown again on replay.

## Shapes

### choices
`{"question"?: s, "options": [{"id": s, "label": s, "hint"?: s}], "multi"?: bool}`
2 to 12 options, ids unique, `id` matches `[A-Za-z0-9_-]{1,40}`.
Click sends `<question>: <label>` (no question: just `<label>`). With `multi`
the user ticks options and confirms; labels are joined with `, `.

### chart
Common: `"type"`, `"title"?: s`, `"unit"?: s`.
- `bar` | `line` | `area`: `"x": [s]` (category labels, 1-60), `"series": [{"name": s, "data": [number]}]` (1-6 series; each `data` has the same length as `x`).
- `donut`: `"slices": [{"label": s, "value": number >= 0}]` (1-8, sum > 0).
- `sparkline`: `"data": [number]` (2-60).
- `stat`: `"stats": [{"label": s, "value": s|number, "delta"?: s, "trend"?: "up"|"down"|"flat", "hint"?: s}]` (1-6 tiles).

Numbers must be finite. Every plotted chart has a "table" toggle with the same data.

    {"type":"bar","title":"Runs per day","unit":"runs","x":["Mon","Tue","Wed"],"series":[{"name":"ok","data":[4,6,5]},{"name":"failed","data":[1,0,2]}]}

### table
`{"title"?: s, "columns": [{"key": s, "label": s, "align"?: "left"|"right"}], "rows": [{<key>: s|number|bool|null, "link"?: applink}]}`
1-8 columns, keys unique `[A-Za-z][A-Za-z0-9_]{0,31}` and not `link`; 1-100 rows. Click a header to sort.

### card
`{"kind": s, "title": s, "subtitle"?: s, "status"?: s, "tone"?: "ok"|"warn"|"error"|"info"|"neutral", "link"?: applink}`
`kind` is a free label (e.g. "workflow", "person"), at most 24 characters.

### list
`{"title"?: s, "items": [<card object>]}` 1-24 items.

### steps
`{"title"?: s, "steps": [{"label": s, "state": "todo"|"active"|"done"|"failed", "detail"?: s}]}` 1-30 steps.

### form
`{"title"?: s, "submitLabel"?: s, "fields": [<field>]}` 1-12 fields; field =
`{"id": s, "type": "text"|"number"|"select"|"checkbox", "label": s, "required"?: bool, "placeholder"?: s, "default"?: s|number|bool, "options"?: [{"value": s, "label": s}], "min"?: n, "max"?: n}`.
`id` matches `[A-Za-z][A-Za-z0-9_]{0,31}`, unique. `select` needs 2-20 options.
Submit sends `<title or "Form">` then one `<label>: <value>` line per field
(checkbox: yes/no).
