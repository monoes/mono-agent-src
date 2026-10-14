# API packages (`apipkg.v1`) — author guide

An API package is one JSON file that describes a REST API: its base URL, how it
authenticates, and every operation (method, path, query, body, pagination,
output). One Go engine (`internal/apipkg`) runs every package as a workflow
node `app.<id>`. You write JSON and test data — no Go.

This guide is complete for catalog authors. The JSON Schema is
`data/schemas/apipkg.v1.schema.json`; the shape is fixed by
`nodes-apipkg-design.md` and the additions listed at the end.

## 1. Author workflow

```bash
# 1. study the n8n node (names only — never copy n8n text or code)
ls ~/scratch/n8n-src/packages/nodes-base/nodes/<App>/

# 2. write the package and its test cases
$EDITOR internal/apipkg/catalog/<id>.json
$EDITOR internal/apipkg/catalog/testdata/<id>.json

# 3. validate + run the harness for your package (every op, against httptest).
#    A validation failure lists every problem of the file.
GOMAXPROCS=4 go test ./internal/apipkg -run 'TestCatalog/<id>$' -v

# 4. (optional) the same validation through the CLI — like every monoagentcli
#    command it needs a monoes.me sign-in, and on a branch build always a
#    scratch HOME:
env HOME=$SCRATCH_HOME go run ./cmd/monoagentcli integration validate internal/apipkg/catalog/<id>.json
#    one operation:   go test ./internal/apipkg -run 'TestCatalog/<id>/<resource>.<operation>$'

# 5. the whole package suite (also checks ids and n8n types are unique)
GOMAXPROCS=4 go test ./internal/apipkg
```

Starting from an OpenAPI
document: `monoagentcli integration import-openapi <file|url> --id <id> --out
internal/apipkg/catalog`, then edit the result (pagination and output paths are
never inferred) and write test cases.

Where packages live:

| Place | Path | Notes |
|---|---|---|
| Built-in catalog | `internal/apipkg/catalog/<id>.json` | embedded; the file name must equal `id` |
| Installed | `~/.monoagent/apipkgs/<id>/package.json` | `apipkg.InstallDir()`; replaces a catalog package with the same id |

## 2. The package file

```jsonc
{
  "$schema": "apipkg.v1",
  "id": "todoist",                         // [a-z0-9_]+, unique; = file name
  "node_type": "app.todoist",              // optional; default "app.<id>"; must start with "app."
  "extends": "",                           // "service.github" etc. — see §11; then no node_type
  "title": "Todoist",                      // palette label and `node list` title
  "description": "Tasks, projects, … in Todoist",
  "category": "app",                       // palette group (keep "app")
  "docs_url": "https://developer.todoist.com/api/v1/",
  "n8n_types": ["n8n-nodes-base.todoist"],           // n8n node types this package imports
  "n8n_trigger_types": ["n8n-nodes-base.todoistTrigger"], // trigger types (the importer maps them to webhook/schedule triggers, §12)
  "base_url": "https://api.todoist.com/api/v1",       // may use {{ cred.x }}
  "headers": { "Accept": "application/json" },        // sent on every request; templates allowed
  "credentials": [ … ],                    // §7
  "resources": [ { "value": "task", "label": "Task" } ],  // n8n "resource" options; omit when n8n has none
  "operations": [ … ]                      // §3, at least one
}
```

Unknown keys are errors (typos are caught). Rules checked by `validate`:

- `resources[].value` unique; every resource has at least one operation.
- With `resources`, each op's `resource` is one of them; without, it is `""`.
- (`resource`, `operation`) pairs are unique.
- Every `{{ p.X }}` names a field of that op, every `{{ cred.X }}` a credential
  field (or an OAuth token key: `access_token`, `refresh_token`, `token_type`,
  `scope`, `expires_at`, `instance_url`).

## 3. Operations

```jsonc
{
  "resource": "task",                 // "" when the package has no resources
  "operation": "create",              // n8n's operation value, verbatim
  "title": "Create a task",
  "description": "",                  // optional
  "method": "POST",                   // GET POST PUT PATCH DELETE HEAD
  "path": "/tasks/{{ p.taskId }}",    // starts with "/" (or is an absolute URL template)
  "base_url": "",                     // optional per-op override of the package base_url
  "query":   { "project_id": "{{ p.projectId }}" },
  "body":    { "content": "{{ p.content }}" },
  "body_type": "json",                // json | form | multipart | raw | ndjson | none
  "headers": { },
  "fields": [ … ],                    // §4
  "pagination": { … },                // §8, optional
  "output": { "path": "data", "split": true },  // §9, optional
  "binary": { … },                    // §10, optional
  "n8n_param_map": { }                // n8nParam → our field, only when names differ
}
```

**Defaults matter for n8n imports.** n8n does not save a parameter that holds
its default value, so an imported node often has no `resource` or
`operation`. The engine then uses the **first resource** in `resources` and
the **first operation listed for that resource**. Order your operations so the
first one per resource is n8n's default (in n8n's node source: the `default:`
of the `operation` property shown for that resource; usually `create` or
`get`). The same is true for fields: give a field n8n's `default` so a
missing param behaves as in n8n.

Use n8n's parameter names verbatim as field keys (`taskId`, `additionalFields`,
`returnAll`, `limit`, `updateFields`, …) so an n8n node's `parameters` object
drops in unchanged. The API's own names go in `query`/`body` keys.

## 4. Fields

Same shape as the workflow editor's `NodeSchemaField`:

```jsonc
{ "key": "taskId", "label": "Task ID", "type": "text", "required": true,
  "default": "", "placeholder": "", "help": "", "options": [], "examples": [],
  "min": 1, "max": 100, "rows": 4, "item_type": "text", "language": "json" }
```

| `type` | Holds | Editor shows |
|---|---|---|
| `text`, `textarea`, `password`, `code` | string | as named |
| `number` | number | number |
| `boolean` | bool | toggle |
| `select` | one of `options` (required) | select |
| `multiselect` | array of `options` (required) | list |
| `array` | array (`item_type`) | list |
| `datetime` | ISO 8601 string | text |
| `json` | any JSON | JSON editor |
| `collection` | object; `options` lists the allowed keys | JSON editor, keys in help |

- `key` matches `^[A-Za-z_][A-Za-z0-9_]*$`. Reserved: `resource`,
  `operation`, `credential`, `credential_id`, `on_error`, `retry`.
- `required: true` is enforced at run time, per item, before any request:
  `apipkg todoist task.create: field "content" (Content) is required`. An
  n8n resource locator (`{"__rl":true,"value":""}`) with an empty value
  counts as missing.
- `default` is applied when the config lacks the key.
- n8n `collection`/`fixedCollection` → `collection` (same key, e.g.
  `additionalFields`), with `options` = the inner n8n names.
- n8n `resourceLocator` → `text`; read it with `| rl` (§5), which accepts
  both a plain value and n8n's `{"__rl":true,"mode":"id","value":"x"}`.
- n8n `options` with values loaded from the API (`loadOptionsMethod`) →
  `text` (or `array` for `multiOptions`).

Generated node schema: a `credential_id` picker, a `resource` select, one
`operation` select per resource (`depends_on` the resource), then every op's
fields. Fields with the same key, type, label and options are merged into one
entry. Each field carries `depends_on: {"key": "operation", "values": […]}`
and an exact `visible_for: [{"resource": …, "operation": …}]`.

Every field is resolved **per input item** from workflow expressions
(`{{ $json.id }}` in the node config), so one node run makes one request per
item.

**Conditions.** `when`, `*_when`, `until`, `$when` and `to_when` are true unless the value is empty, `false`, `0` or the text `"false"`/`"0"`/`"no"`/`"off"`; any other value (an id, a mode name) is true.

## 5. Template language

Strings in `base_url`, `headers`, `path`, `query`, `body`, `binary.file_name`,
`binary.mime_type` and `inject` are templates.

Scopes:

| Root | What |
|---|---|
| `p.<field>` | the op params (node config after workflow expressions; defaults applied) |
| `cred.<key>` | the credential (the connection's fields, or OAuth tokens) |
| `item.<path>` | the current input item's JSON |
| `el.<path>` | the current element inside a `$map` (`el` alone is the element) |
| `up.<path>` | inside a nested `$map`, the enclosing `$map`'s element (`{{ up \| get:el }}` reads the outer object at the key the inner list holds) |
| `res.<path>` | the main request's decoded response, inside `also` steps |
| `items` | every input item's JSON as a list, in `batch` operations |
| `pre.<path>` | the decoded response of the op's (last) `before` request |
| `bin.file_name`, `bin.mime_type`, `bin.file_extension`, `bin.base64`, `bin.file_size` | the uploaded binary's name and type (ops with `binary.upload_field`); raw/multipart uploads default their Content-Type to it |
| `hdr.<Header-Name>` | the main response's headers, case-insensitive (`{{ hdr.Location }}`, `{{ hdr.Resource-ID }}`); with an empty body, `output.to`/`expand` still run on `{}` so 201-with-headers ops can return `{id, uri}` |
| `main.<path>` | the main request's response, unchanged while `poll` repeats rebind `res` (also set in `also` steps) |
| `pres.<i>.<path>` | the response of `before` step `i` (0-based) when `before` is a list |
| `now` | the current UTC time (RFC 3339): `{{ now \| epoch \| add:p.duration \| date:'YYYY-MM-DDTHH:mm:ssZ' }}` |
| `batch` | in a `bulk` request, the chunk's items as `{p, item, index}` (§9) |
| `idx` | the element's position inside `$map`/`$entries` (pivot parallel lists: `{{ p.mrr \| get:(idx \| string) }}`) |
| `uuid` | a new random UUID each time (e.g. a Calendar conference `requestId`) |

Paths walk objects by key (a key that itself contains dots, like `@odata.nextLink`, is found too) and arrays by index; `-1` is the last element
(`p.list.-1.id`). A missing path is `nil`. Only `cred.*` is available in
`inject` and the package `base_url`/`headers`.

**Typed vs interpolated.** A string that is exactly one `{{ … }}` keeps the
typed value (number, bool, object, array): `"labels": "{{ p.labels }}"` sends
a JSON array. Anything else is string-interpolated: `"Bearer {{ cred.token }}"`;
`nil` interpolates as `""`, objects/arrays as JSON.

**Literal braces.** `\{{` and `\}}` stand for a literal `{{` and `}}` (in JSON:
`"\\{{ticket.id\\}}"` → `{{ticket.id}}`), for APIs whose own placeholders use
braces. They combine with expressions: `"\\{{{{ el }}\\}}"` → `{{ticket.id}}`
when `el` is `ticket.id` (Zendesk trigger bodies).

**Omit empty.** In `query` and `body` objects a key whose value resolves to
`nil`, `""`, `[]` or `{}` is left out. `0` and `false` are kept. A nested
object that ends up empty is left out too. Array elements that resolve
empty are dropped. Write `"!key"` to send the value even when empty (a `$map`/`$concat` under `"!key"` sends `[]`)
(`"!description": "{{ p.description }}"` sends `""`/`null`). A forced object or
array template is sent as `{}`/`[]` when everything inside it is empty
(`"!configVars": {"$merge": "{{ p.vars }}"}` sends `"configVars": {}`), at any
depth, and a `"!key"` inside a literal `$merge` object survives the spread.

**`$merge`.** `"$merge": "{{ p.additionalFields }}"` spreads an object into
the parent (its empty values are dropped). Explicit keys override merged
ones, but an explicit key that resolves empty leaves the merged value in
place. `$merge` may be a list: `"$merge": ["{{ p.a }}", "{{ p.b }}"]`. Merging
a non-object is an error. Use `$merge` only when n8n's keys already equal the
API's; otherwise map each key explicitly:
`"due_string": "{{ p.options.dueString }}"`.

**Filters** (pipe, left to right). All but `default` and `then` pass `nil`
through, so an unset param stays unset:

| Filter | Does |
|---|---|
| `json` | value → JSON string |
| `csv` | array → `"a,b,c"` (a scalar → its string) |
| `int` | number/numeric string → integer (`""` → nil); error otherwise |
| `float` | → float |
| `bool` | bool, `"true"/"false"/"1"/"0"/"yes"/"no"`, numbers ≠ 0 |
| `string` | → string |
| `lower`, `upper` | case |
| `default:"x"` | `x` when the value is empty; the argument may be a quoted string (`'x'` or `"x"`), a number, `true`/`false`/`null`, or a path (`default:p.other`, `default:cred.region`) |
| `then:'x'` | `x` when the value is set, else nil — empty, `false` and the text `"false"` count as unset (n8n toggles); `0` is a value |
| `rl` | n8n resource locator → its `value`; a `"mode": "url"` locator holding a Google Docs/Sheets/Slides/Drive URL yields the file id; other values pass |
| `find:'path',value` | first list element whose value at `path` equals `value` (text compare), else nil: `{{ pre.sheets \| find:'properties.title',p.sheetName \| get:'properties.sheetId' }}` |
| `find_row:column,value` | value rows (first row = header) → 1-based row number of the first data row whose `column` cell equals `value` (an object value, e.g. `item`, is read at `column`), else nil: `{{ pre.values \| find_row:p.columnToMatchOn,item }}` |
| `get:'path'` | walk into the value: `{{ pre.payload.headers \| find:'name','Message-ID' \| get:'value' }}` |
| `add:n`, `sub:n`, `mul:n` | arithmetic with a number or path (`{{ p.startIndex \| sub:1 }}`, `{{ p.minutes \| mul:60000 }}`); whole results are integers |
| `numeric` | a numeric string → number (integer when whole); anything else passes unchanged |
| `lines` | text → list of its non-empty trimmed lines |
| `matches:'regex'` | `true` when the text matches, else nil (chain into `then`; SOQL date literals) |
| `in:'a,b'` | `true` when the value is one of the listed (comma text, list or path), else nil |
| `unflatten` | dotted keys → nested objects (`{"customFields.x": 1}` → `{"customFields": {"x": 1}}`; TheHive 5) |
| `flatten` | lists and objects of lists → one flat list (object keys sorted; Rocket.Chat `{update: […], remove: […]}`) |
| `snake` / `snake:'ascii'` | change-case v5 snake_case as n8n uses it (`"Due Date"`, `"DueDate"` → `due_date`, `"Q1 Goals"` → `q1_goals`: letters and digits stay together; accented letters kept, `"Prénom"` → `prénom`); `'ascii'` first turns every non-ASCII character into a separator (`pr_nom`, n8n Notion before node version 3) |
| `values` | object → list of its values, by sorted key (maps keyed by id: Philips Hue lights) |
| `hex` | hex text → integer (`#ff0000` → 16711680; `#`/`0x` stripped) |
| `pluck:'path'` | list of objects → list of their values at `path` (empties dropped): `{{ p.options.fields.field \| pluck:'name' \| csv }}` |
| `zip:headers[,'hkey'[,'vkey']]` | pair a header list with the value list into an object: GA4 `{{ el.dimensionValues \| zip:res.dimensionHeaders,'name','value' }}` |
| `replace:'from','to'` | replace every occurrence: `{{ p.clientCustomerId \| replace:'-','' }}` |
| `re:'pattern'` | first capture group of the first match (whole match without groups); no match → the value unchanged: `{{ p.review \| rl \| re:'reviews/([^/]+)' }}` |
| `escape_html` | escape `& < > " '`; on a list, each element (also `prefix`/`suffix`) |
| `join:'sep'` | list → text joined by `sep`: `{{ p.bodyPlainText \| lines \| escape_html \| prefix:'<p>' \| suffix:'</p>' \| join:'' }}` |
| `colnum`, `colname` | spreadsheet column letter ↔ 1-based number (`"C"` ↔ 3, `"AA"` ↔ 27) |
| `path` / `path:'query'` | in a `path` template: escape each segment but keep `/` (Google resource names `spaces/A/messages/B`, `accounts/1/locations/2`); `'query'` also keeps a `?query` suffix as is (Confluence `"/wiki{{ el.downloadLink \| path:'query' }}"`) |
| `entries` / `entries:'column,value'` | object → `[{key, value}, …]` sorted by key (names optional): Coda `"cells": "{{ el \| entries:'column,value' }}"`; `get:p.keyName` picks a key named by a param |
| `sheetgid` | n8n sheet locator → sheet id text: `"gid=0"` (list mode), `…#gid=123` (url mode), `123` (id mode/expression), or the locator object itself |
| `gid` | Google file id from a URL (`…/d/<id>/edit`, `…/folders/<id>`, `?id=<id>`); bare ids and non-Google values pass (n8n Docs `documentURL`) |
| `eq:'x'`, `ne:'x'` | `true` when the value's string equals (`eq`) / differs from (`ne`) the argument, else nil, so they chain into `then`: `"$merge": "{{ p.dataToSend \| eq:'autoMapInputData' \| then:item }}"` |
| `kv:'keyField,valueField'` / `kv:'keyField,valueField,prefix'` | n8n key/value rows `[{fieldId: "a", fieldValue: 1}]` → object `{"a": 1}` (with a prefix: `kv:'fieldId,fieldValue,field_'` → `{"field_12": …}`). A single-key wrapper (`{"fieldValues": [...]}`) is unwrapped; rows with an empty key are skipped |
| `fromjson` | JSON string → value (n8n `jsonParameters` / `*Json` params: `"$merge": "{{ p.additionalFieldsJson \| fromjson }}"`); non-strings pass |
| `"$when": "<template>"` (object key) | the object is omitted when the template renders empty/false, and kept even with all other keys empty when it passes (Google Docs `"endOfSegmentLocation": {}`) |
| `prefix:'x'`, `suffix:'x'` | add text before/after a NON-empty value (empty stays empty → omitted): `{{ p.rrule \| prefix:'RRULE:' }}`, `{{ p.contactId \| prefix:'people/' }}` |
| `base64`, `base64url` | encode the value's text (`base64url` is unpadded, for Gmail `raw`) |
| `row:<path>` / `row:<path>,'null'` | object → list of its values ordered by a header list: `{{ item \| row:pre.values }}` (a list of rows uses its first row; missing keys → `""`, or `null` with the second argument, which the Sheets API leaves unchanged on update) |
| `split` / `split:';'` | `"a, b,c"` → `["a","b","c"]` (default separator `,`; parts trimmed, empty ones dropped; lists pass) |
| `date:'YYYY-MM-DD'` | date or epoch (seconds, or ms from 1e11) → formatted text in UTC; tokens `YYYY`/`yyyy`, `YY`, `MM`, `DD`/`dd`, `HH`, `hh`, `mm`, `ss`, `SSS`, `Z` (`Z` for UTC), `ZZ` (`+00:00`), `A`; other characters are literal |
| `epoch_ms`, `epoch` | date (RFC 3339, `2006-01-02T15:04:05`, `2006-01-02`, …) → Unix milliseconds / seconds; numbers pass |
| `pick:'a,b'` | object with only the listed keys (comma string, list or path: `{{ item \| pick:p.columns }}`) |
| `firestore_value` | a JSON value → a Firestore Value (stringValue, integerValue/doubleValue, booleanValue incl. "true"/"false", timestampValue for RFC 3339 text, nullValue, arrayValue, mapValue) |
| `firestore_fields` / `firestore_fields:p.columns` | object → Firestore `fields` (all keys, or the listed columns with missing ones as nullValue) |
| `omit:'a,b'` | object without the listed keys; the argument may be a path to a comma string or list (`omit:p.inputsToIgnore`) |
| `len` | number of elements of a list, keys of an object or characters of a text: `{{ p.data \| fromjson \| len }}` (Excel raw append spans that many rows) |
| `md5`, `sha1`, `sha256` / `…:'base64'` | hex digest of the value's text; with `'base64'` of the base64-decoded bytes, so a file's hash is `{{ bin.base64 \| sha256:'base64' }}` (Cortex attachment hashes) |
| `mod:n` | remainder with the sign of `n` (`-1` with `mod:7` → `6`; `{{ p.offset \| mod:7 }}`) |
| `weekday` | date or epoch → ISO weekday in UTC, 1 = Monday … 7 = Sunday. Monday of last week: `{{ (now \| date:'YYYY-MM-DD' \| epoch) \| sub:((now \| weekday \| add:6) \| mul:86400) \| date:'YYYY-MM-DD' }}` (GA4 `lastCalendarWeek`) |
| `dotnet_date` | .NET JSON date `/Date(1586833770780)/` (optional `+0100` offset) → `2020-04-14T03:09:30.780Z`; objects and lists are converted deeply, other values pass. A whole response: `"output": {"to": "{{ el \| dotnet_date }}"}` (Unleashed) |
| `wrap:n` | text broken into lines of at most `n` characters joined by CRLF (base64 bodies in MIME messages: `{{ p.message \| base64 \| wrap:76 }}`) |
| `mime_parts:'boundary'` | `bin.files` → one base64 attachment part per file (Content-Type with the file's MIME type and name, Content-Disposition attachment, lines wrapped at 76) plus the closing `--boundary--`; an empty list is nil, a file without data fails naming its property. The template writes the `multipart/mixed` header and the text part (Gmail send/reply/draft) |
| `sealbox:key` | the value's text sealed for a base64 X25519 public key (libsodium `crypto_box_seal`), base64: Venafi `"{{ p.privateKeyPassphrase \| sealbox:pres.1.key }}"` |
| `unique` / `unique:'path'` | list without repeats, first kept, order unchanged; elements compare by their JSON, or objects by their value at `path` (elements without it are kept). On a builder write `"$unique": true` (or a path) beside `$concat`/`$map`: SeaTable `{"$concat": ["{{ pres.2 \| get:p.col }}", {"$map": …}], "$unique": true}` |
| `set:key,value` | the object (nil counts as `{}`) with `key` set to `value` (an empty key changes nothing): Excel upsert appends `{{ p.fieldsUi.values \| kv:'column,fieldValue' \| set:p.columnToMatchOn,p.valueToMatchOn }}` |
| `not` | `true` when the value is unset (as for `then`), else nil: `"/{{ p.pit \| not \| then:p.indexId }}{{ p.pit \| not \| then:'/' }}_search"` |
| `bind:params` | n8n query parameters: every string value or object key that is exactly `$1`, `$2`, … is replaced by that element of `params` (a list or its JSON text) — a parameter never adds structure. A key placeholder must give a plain name not starting with `$` that the object doesn't already have; a placeholder without a value or an unused parameter is an error; an empty list changes nothing: `{{ p.options.query \| fromjson \| bind:p.options.queryParameters }}` (Elasticsearch) |
| `base64_chunks:n` | base64 data → list of its `n`-byte pieces, each base64 (chunked uploads: `"for_each": "{{ bin.files.0.base64 \| base64_chunks:5242880 }}"` with `idx` as the segment index); a list such as `bin.files` gives one flat list of `{item, segment, data, file}` segments across its files |
| `map:'from=to,…'` | a value found in the table → its replacement, others unchanged; a list is mapped per element (n8n event names → the vendor's: `{{ p.event \| map:'balanceCredit=balances#credit' }}`) |
| `urlencode` | object → `k=v&k2=v2` (keys sorted, each part percent-encoded like `encodeURIComponent`); any other value → its text percent-encoded: Azure `"x-ms-tags": "{{ p.options.tags \| kv:'tagName,tagValue' \| urlencode }}"` |

**n8n `dataToSend` pattern.** `autoMapInputData` sends the input item, `defineBelow` sends the key/value rows:
```json
"body": { "$merge": [
  "{{ p.dataToSend | eq:'autoMapInputData' | then:item }}",
  "{{ p.fieldsUi.fieldValues | kv:'fieldId,fieldValue' }}"
] }
```
(`then:item` passes the whole item; combine with `omit:p.inputsToIgnore` on a `{{ item | omit:p.inputsToIgnore }}` expression when the n8n node offers it.)

**Positional lists (`$list`).** `{"$list": [ … ]}` keeps every element, empty ones included (`""`, `[]`, `{}`, null), for APIs with positional arguments (Odoo `execute_kw`). An empty object as a key's value is kept with `"!key": {}` (Notion `{"!past_week": {}}`).

**Expression heads.** An expression may start with a parenthesised sub-expression: `{{ (p.name | lower) | prefix:'x-' }}`.

**Joined text (`$join`).** `{"$join": [ <template>, … ] | <template/builder giving a list>, "$sep": " AND ", "$prefix": "\"", "$suffix": "\""}` renders the parts, drops empty ones, joins the rest with `$sep` and wraps them in `$prefix`/`$suffix`; nothing left → omitted. Freshservice filters `"name:'Ada' AND department_id:5"`; Grist sort `{"$join": {"$map": "{{ p.sort }}", "$to": "{{ el.direction | eq:'desc' | then:'-' }}{{ el.field }}"}, "$sep": ","}`.

**JSON text of a built value (`$json`).** `{"$json": { … }}` renders the inner template (with `$concat`, `$map`, …) and returns it as a JSON string (Confluence ADF bodies).

**Objects with built keys (`$entries`).** `{"$entries": "{{ p.filters.date }}", "$key": "date[{{ el.operation }}]", "$value": "{{ el.value | epoch }}"}` → `{"date[after]": 1704067200}`; empty keys/values are skipped (works inside `query`, `body` and `$merge`).

**Lists of objects (`$map`, `$concat`).** `{"$map": "{{ p.emailOptions.emailProperties }}", "$to": {"name": "email", "subtype": "{{ el.subtype }}", "value": "{{ el.email }}"}}` renders `$to` once per element (empty results dropped; an empty list is omitted). `{"$concat": [ <template>, … ]}` joins parts into one list (lists are spread, empty parts skipped). Together they build arrays like `properties` from several n8n collections:
```json
"properties": { "$concat": [
  { "$map": "{{ p.emailOptions.emailProperties }}", "$to": { "type": "SYSTEM", "name": "email", "subtype": "{{ el.subtype }}", "value": "{{ el.email }}" } },
  { "$map": "{{ p.phoneOptions.phoneProperties }}", "$to": { "type": "SYSTEM", "name": "phone", "value": "{{ el.number }}" } }
] }
```

**Expression arguments.** A filter argument may be a parenthesised expression with its own filters: `{{ pre.sheets | find:'properties.sheetId',(p.sheetName | sheetgid) | get:'properties.title' }}`.

**Credential templates may read params.** `inject` values can use `p.*` (any op field), e.g. Contentful's token choice: `"Authorization": "Bearer {{ p.source | eq:'previewApi' | then:cred.preview_token | default:cred.access_token }}"`.

**Templated output keys.** `output.path` and `output.each` may be templates over `p` (n8n `rawData` / `simple` toggles): `"each": "{{ p.additionalFields.rawData | ne:'true' | then:'fields' }}"` (empty = no unwrap).

**Paths are escaped.** In `path`, each expression's value is URL-escaped
(`a b/c` → `a%20b%2Fc`). Literals the author wrote as a `then`/`default`
argument are not, so an optional path suffix works:
`"/tasks{{ p.filters.filter | then:'/filter' }}"`.

**Bracket query style.** `"query_style": "brackets"` (on the package or an op) encodes nested query and form values PHP-style — `search_criteria[filter_groups][0][filters][0][field]=sku` (Magento); lists are indexed, empty values dropped. Pagination params may be such flat bracket keys (`"page_param": "search_criteria[current_page]"`).

**Query encoding.** Arrays repeat the key (`tags=a&tags=b`; use `| csv` for
`tags=a,b`), objects are sent as JSON, everything else as its string.

## 6. Request building

1. URL = `op.base_url` or `base_url` (rendered) + rendered `path`. A path that
   renders to an absolute `http(s)://` URL is used as is.
2. `query` is rendered and added; pagination params next (§8).
3. Body by `body_type` (default: `json` when `body` is set; `raw` for a binary
   upload with no body; else `none`):
   - `json` — the rendered body (any JSON value); `Content-Type: application/json`.
   - `form` — object → `application/x-www-form-urlencoded` (encoded like query).
   - `multipart` — object fields as form fields plus the binary file part (§10).
   - `raw` — the binary upload, or the rendered body as text.
   - `ndjson` — the body renders to a list; each element is one JSON line (an element that is itself a list gives one line per its elements, so a `$map` can emit an action line and a document line per item), with a final newline; `Content-Type: application/x-ndjson` (Elasticsearch `_bulk`).
   - `none` — no body (`body` must be unset).
4. Package `headers`, then op `headers` (rendered; empty values not sent).
   `Accept: application/json` unless set (or a download).
   Header **names** may be templates; a header whose name renders empty is
   not sent (`"{{ p.options.hdr }}": "{{ p.options.hdrValue }}"`). A
   `"$merge"` entry spreads an object of headers (rendered first, so explicit
   headers override it; empty names/values skipped; a non-object is an
   error) — Azure custom metadata from an n8n key/value collection:
   `"$merge": "{{ p.options.metadata | kv:'fieldName,fieldValue,x-ms-meta-' }}"`.
   The same works in `also`/`before` step headers.
5. Auth is injected last (§7).

Responses: 2xx bodies are read up to 64 MiB (larger is an error); a non-2xx
status is an error carrying up to 64 KiB of the body:
`apipkg todoist task.get: HTTP 404: {"error":"…"}`. Error handling in
workflows is the node's normal `on_error`.

**Errors in a 2xx body (`error_when`).** Some APIs answer HTTP 200 with an
error inside (JSON-RPC `{"error": …}`, Slack `{"ok": false}`, GraphQL
`{"errors": […]}`). A package-level `error_when` template is checked on every
2xx response of the package (main request, pages, `before`/`also`/`poll`
steps, downloads) with `res` = the decoded body and `hdr` = its headers;
when it renders true the item fails with `error_message` (a template in the
same scope; empty → the body):
```json
"error_when": "{{ res.jsonrpc | then:res.error }}",
"error_message": "{{ res.error.data.message | default:res.error.message }}"
```
→ `apipkg odoo contact.get: the API reported an error: Access Denied`. An
operation's own `error_when`/`error_message` replace the package's (use
`"error_when": "false"` to switch the check off for one op). Use `eq:'false'`
for flags that are only an error when explicitly false (`{{ res.success | eq:'false' }}`).

A step (`before`, `also`, `poll`, `pagination.next`) may carry its own `error_when`/`error_message`; they replace the op's (and package's) for that step's responses. Dropcontact's poll gets `{"success": false}` while the result is not ready: the op says `"{{ res.success | eq:'false' }}"`, the poll only `"{{ res.error | eq:'true' }}"`, so pending replies keep polling and real errors fail.

`method` may be a template (`"{{ p.sendResume | then:'POST' | default:'GET' }}"`), upper-cased and checked at run time. A `path` that is a single expression yielding an absolute `http(s)://` URL is used unescaped.

WebDAV methods MKCOL, MOVE, COPY, PROPFIND and PROPPATCH are allowed (NextCloud). XML responses decode to JSON: elements by local name (repeats become lists), attributes as `@name`, text as `#text` or the plain string — a PROPFIND reads as `res.multistatus.response`.

## 7. Credentials and auth injection

```jsonc
"credentials": [{
  "platform": "todoist",              // connection platform id, registered at run time
  "name": "Todoist",                  // shown in `connect` listings
  "n8n_credential": "todoistApi",     // n8n credential type, for the importer
  "n8n_credential_alt": ["todoistOAuth2Api"],    // more n8n types mapping here
  "n8n_field_map": { "apiKey": "access_token" }, // n8n credential field → our field key
  "methods": ["bearer", "oauth"],     // api_key | bearer | basic | oauth | oauth1 | client_credentials | none
  "fields": [ { "key": "access_token", "label": "API token", "secret": true, "required": true, "help": "…" } ],
  "oauth": { "auth_url": "…", "token_url": "…", "scopes": ["a", "b"], "pkce": true, "extra_params": {} },
  "inject": {
    "headers":   { "Authorization": "Bearer {{ cred.access_token }}" },
    "query":     { "key": "{{ cred.api_key }}" },
    "basic":     { "username": "{{ cred.email }}", "password": "{{ cred.api_token }}" },
    "aws_sigv4": { "service": "s3", "region": "{{ cred.region }}" }
  },
  "reuse_platform": ""                // use an existing built-in platform instead (e.g. "google_drive")
}]
```

- Each credential's `platform` is registered into the connection registry,
  so `monoagentcli connect <platform>` (API key form or OAuth with PKCE),
  `connect save`, `connect oauth`, `connect list --all` and credential
  resolution in workflows all work. `api_key`/`bearer` collect `fields` (the
  API-key form), `basic` collects `fields` as the username/password form,
  `oauth` runs the browser flow and stores `access_token`, `refresh_token`,
  `expires_at` (silent refresh works). OAuth client ids come from
  `connect set-oauth-client` or `MONOAGENT_<PLATFORM>_CLIENT_ID/_SECRET`.
- **Name the token field `access_token`** when an API takes the same bearer
  token from an API key and from OAuth: one `inject` then covers both methods.
- PKCE is always used. `scopes` are space-joined; for an API that wants
  another separator put the whole string in `extra_params.scope`
  (Todoist: `"extra_params": {"scope": "data:read_write,data:delete"}`).
- A `platform` may not be a built-in platform id (`github`, `notion`,
  `slack`, … — `connect list --all`). To use a built-in connection, set
  `reuse_platform` and still write `inject` for it.
- `none` (alone or with others) makes the credential optional.
- Credentials are read **only** from `config["credential"]` (the engine puts
  the resolved connection there). With several credentials the engine picks
  the first whose required fields are all present (an OAuth connection
  satisfies them with `access_token`), else the first.
- A header whose expressions all render empty (e.g. `"Bearer "`) is not sent.
- `aws_sigv4` signs with AWS Signature V4 (after everything else). Keys
  default to `cred.access_key_id`, `cred.secret_access_key`,
  `cred.session_token`; override with `access_key_id`,
  `secret_access_key`, `session_token` templates. S3 also gets
  `X-Amz-Content-Sha256`.
- `inject` must not be empty unless the only method is `none`.

**Scope groups (`scope_group`).** A credential on an umbrella connection (`"reuse_platform": "google"` / `"microsoft"`) names the scope group it needs: `"scope_group": "calendar"`. Groups (`monoagentcli connect --help` lists them under `--scopes`): google — sheets, gmail, drive, youtube, calendar, docs, forms, tasks, contacts, opt-in translate, naturallanguage, chat, bigquery, cloudstorage, firebase, ads, analytics, books, businessprofile, admin; microsoft — mail, calendar, files, sites, teams, tasks (To Do / Planner), contacts, opt-in teams_admin (channel create/settings/delete, reading channel messages), directory (Entra users and groups), security (Graph Security). Opt-in groups are granted only when named in `--scopes` (or `all`): their scopes need a Google review or a Microsoft tenant admin's consent, so a plain `connect` never asks for them. Before an item's request the engine checks the connection grants the group and otherwise fails with the exact `monoagentcli connect … --scopes …` command (non-umbrella connections are not judged).

An **operation** may set its own `"scope_group"`, which replaces the credential's for that op — one package, several groups: Outlook's credential says `mail`, its calendar/event ops `"scope_group": "calendar"` and contact ops `"contacts"`; Teams channel management `teams_admin`, Planner tasks `tasks`. Names are validated against the umbrella's groups when the package has an umbrella credential.

`inject.body: {"key": "{{ cred.api_key }}"}` merges credential values (typed: a single expression keeps booleans/numbers) into JSON/form/multipart bodies (Mandrill sends its key in every body).

**Azure signers.** `inject.azure_shared_key: {"account": "{{ cred.account }}", "key": "{{ cred.key }}"}` signs Azure Storage requests (SharedKey; sets `x-ms-date`; send `x-ms-version` as an op header). `inject.azure_cosmos: {"key": "{{ cred.key }}"}` signs Cosmos DB requests with the master key (resource type and link derived from the path). Keys are base64.

**HMAC request signature (`inject.hmac`).** `{"header": "api-auth-signature", "secret": "{{ cred.api_key }}", "message": "{{ req.query }}", "alg": "SHA256", "encoding": "base64", "prefix": ""}` — `req.method`, `req.path`, `req.query` (the encoded query string) and `req.body` describe the built request (Unleashed).

**Self-signed certificates.** A connection field `allow_unauthorized_certs` that is true skips TLS verification for that connection's requests (n8n's credential option; self-hosted Venafi, TheHive).

**Signed JWT per request (`inject.jwt`).** For APIs that want a short-lived token you sign (Ghost Admin, Zoom JWT apps, App Store Connect): `"jwt": {"header": "Authorization", "prefix": "Ghost ", "alg": "HS256", "secret": "{{ cred.api_key | split:':' | get:'1' }}", "secret_encoding": "hex", "kid": "{{ cred.api_key | split:':' | get:'0' }}", "claims": {"aud": "/admin/"}, "ttl_s": 300}`. `alg`: HS256/384/512 (secret as `text` (default), `hex` or `base64`), RS256 or ES256 (secret = a PEM private key). `iat` and `exp` (= iat + ttl_s, default 300) are added; header defaults to Authorization with prefix "Bearer ".

`oauth.token_fields: ["organizer_key", "account_key"]` copies those keys of the token response onto the connection at connect and refresh (readable as `cred.organizer_key`; GoTo Webinar).

**Client-credentials grant (`"methods": ["client_credentials"]`).** For APIs where every call needs a token fetched with the OAuth client-credentials grant (Bitwarden, PayPal, many enterprise APIs). The user's connection holds the client id/secret (fields `client_id` / `client_secret`, or name them with `oauth.client_id_field` / `client_secret_field`). Before the first request the engine POSTs a form `grant_type=client_credentials` (+ `scope` from `oauth.scopes`, + `oauth.extra_params`) to `oauth.token_url`, sends the client id/secret in the form (or as Basic auth with `"client_auth": "basic"`), caches the token until just before `expires_in`, and exposes it as `cred.access_token` to `inject`. `token_url` and `extra_params` are templates over `cred.*` (self-hosted: `"https://{{ cred.domain }}/identity/connect/token"`). `auth_url` is not needed.
```json
{ "platform": "bitwarden", "methods": ["client_credentials"],
  "fields": [ {"key":"client_id","label":"Client ID","required":true}, {"key":"client_secret","label":"Client secret","secret":true,"required":true},
              {"key":"environment","label":"Domain (self-hosted)","help":"empty for bitwarden.com"} ],
  "oauth": { "token_url": "https://{{ cred.environment | default:'identity.bitwarden.com' }}/connect/token", "scopes": ["api.organization"],
             "extra_params": { "deviceName": "mono-agent", "deviceType": "2", "deviceIdentifier": "mono-agent" } },
  "inject": { "headers": { "Authorization": "Bearer {{ cred.access_token }}" } } }
```

**OAuth 1.0a (`"methods": ["oauth1"]`, `inject.oauth1`).** Trello and Twitter/X v1.1-style user tokens. Every request gets an `Authorization: OAuth …` header signed per RFC 5849 over the method, URL, query and (for form bodies) the form fields. `inject.oauth1` keys are templates defaulting to `cred.consumer_key`, `cred.consumer_secret`, `cred.oauth_token`, `cred.oauth_token_secret`; `signature_method` (HMAC-SHA1 default, HMAC-SHA256, HMAC-SHA512, PLAINTEXT) and `realm` are optional, so `"oauth1": {}` is the usual inject. The credential declares those four fields and `oauth.request_token_url`, `oauth.auth_url` (the authorization page; it may carry its own query) and `oauth.token_url` (the access-token endpoint). `monoagentcli connect <platform>` then offers the three-legged flow (asks the consumer key/secret, opens the authorization page, stores the token pair) and the API-key form for pasting all four values.
```json
{ "platform": "trello_oauth1", "methods": ["oauth1"], "n8n_credential": "trelloOAuth1Api",
  "fields": [ {"key":"consumer_key",…}, {"key":"consumer_secret",…}, {"key":"oauth_token",…}, {"key":"oauth_token_secret",…} ],
  "oauth": { "request_token_url": "https://trello.com/1/OAuthGetRequestToken",
             "auth_url": "https://trello.com/1/OAuthAuthorizeToken?scope=read,write&expiration=never&name=mono-agent",
             "token_url": "https://trello.com/1/OAuthGetAccessToken" },
  "inject": { "oauth1": {} } }
```

**Per-tenant OAuth (`oauth.connect_fields`).** `oauth.auth_url`, `token_url`, `scopes` and `extra_params` may be templates over `cred.*` when the endpoints depend on the account (Zendesk subdomain, self-managed GitLab, Microsoft tenant, Zoho data centre). List the credential fields they read in `connect_fields`: `connect` asks for them before opening the browser, stores them on the connection, and renders the endpoints from them at connect and at every token refresh (a reconnect keeps the earlier values). Zendesk:
`"oauth": {"auth_url": "https://{{ cred.subdomain }}.zendesk.com/oauth/authorizations/new", "token_url": "https://{{ cred.subdomain }}.zendesk.com/oauth/tokens", "scopes": ["read", "write"], "connect_fields": ["subdomain"]}`.

**Account lookup after connect (`oauth.metadata`).** `{"url": "https://login.mailchimp.com/oauth2/metadata", "headers": {"Authorization": "OAuth {{ cred.access_token }}"}, "fields": {"dc": "dc", "api_endpoint": "api_endpoint"}}` — after the OAuth flow the engine GETs `url` with the new token and stores the response values at the `fields` paths on the connection (readable as `cred.dc`). A connection that lacks them (connected earlier, or saved by hand) gets the same lookup before its first request, cached per token.

**AWS AssumeRole.** Any `aws_sigv4` credential whose connection has `role_arn` (n8n's "AWS (Assume Role)" credential, `awsAssumeRole`) first calls STS AssumeRole with the connection's keys — `external_id` and `role_session_name` (default `mono-agent`) when set, on the regional STS endpoint of `region` (else the global one) — caches the temporary credentials until a minute before they expire, and signs with them: `cred.access_key_id`, `secret_access_key` and `session_token` are replaced by the role's. Packages need nothing beyond `"n8n_credential_alt": ["awsAssumeRole"]` and the field map (`roleArn` → `role_arn`, `externalId` → `external_id`, `roleSessionName` → `role_session_name`, `sts*` keys → the access keys).

## 8. Pagination

```jsonc
"pagination": {
  "type": "cursor",                 // cursor | offset | page | link_header | next_url
  "cursor_param": "cursor",         // cursor: request param carrying the cursor
  "cursor_path": "next_cursor",     // cursor: response path of the next cursor ("items.-1.token" ok)
  "has_more_path": "has_more",      // cursor: optional bool; false stops
  "offset_param": "offset",         // offset
  "page_param": "page",             // page
  "start_page": 1,                  // page: first page number (default 1)
  "limit_param": "limit",           // page-size param (all types except link_header/next_url pages 2+)
  "page_size": 100,                 // default 100
  "next_url_path": "paging.next",   // next_url: response path of the next URL (relative ok)
  "results_path": "data",           // where each page's array is (default: output.path; "" = whole body)
  "return_all_field": "returnAll",  // default "returnAll"
  "limit_field": "limit",           // default "limit"
  "param_in": "query",              // "body": send page params in the JSON body
  "max_pages": 1000                 // safety stop; items then get pagination_truncated=true
}
```

- If the op has a `returnAll` field: `returnAll: true` fetches every page,
  otherwise the run stops at `limit` items (field default, else 50) and
  requests `min(limit, page_size)` per page. Without a `returnAll` field the
  op fetches every page (n8n "get many" ops with no limit).
- Stops: cursor — empty/repeated cursor, empty page or `has_more` false;
  offset/page — empty page, or fewer than `page_size` results when
  `limit_param` is set; link_header — no `rel="next"`; next_url — empty path.
- `link_header`/`next_url` follow the server's URL as given (op query is not
  re-added; auth is).
- Paginated results are always split into one item per element.

`next_url_prefix` (template) is prepended to a next URL that starts with `/` when its base is not the request host (Confluence v2: `"next_url_prefix": "https://api.atlassian.com/ex/confluence/{{ p.site | rl }}"`).

With `"param_in": "body"`, the page param names may be dot paths, nested into the JSON body (GraphQL relay paging): `"cursor_param": "variables.after"`, `"limit_param": "variables.first"`.

`"values": true` (pagination): the results at `results_path` are an object keyed by id (Mautic `{"contacts": {"12": {…}}}`); its values are the page's list (numeric keys in numeric order, else sorted), and offsets advance by their count. The `values` filter uses the same order.

`pagination.when` (template): when false, the op makes one plain request with no page params (Strapi v3 vs v4: `"when": "{{ cred.api_version | eq:'v4' }}"`). `cursor_param`, `offset_param`, `limit_param` and `page_param` may be templates.

`"param_in": "path"` puts the page params into the path template as `page.<param>` instead of the query (`"path": "/SalesOrders/{{ page.pageNumber }}"`). `param_in: "body"` works with json, form and multipart bodies.

`"type": "header_cursor"`: the cursor comes from the response header named by `cursor_path` and is sent back as the request header named by `cursor_param` (Cosmos DB `x-ms-continuation`).

With `param_in: "body"`, a cursor that is an object or list goes back unchanged (DynamoDB `LastEvaluatedKey` → `ExclusiveStartKey`).

**Continuation requests (`pagination.next`, cursor only).** Some APIs page through another endpoint with another body. `next` is a step (`method`, `path`, `base_url`, `query`, `body`, `body_type`, `headers`, `when`, `auth`, `error_when`) sent for every page after the first instead of the op's request, with `res` = the previous page and `page.cursor` = its cursor (the value at `cursor_path`, objects and lists unchanged); `cursor_param` is then not needed and neither the op's query nor `limit_param` is re-added. Paging stops as for `cursor`. Dropbox:
```json
"body": { "!path": "{{ p.path }}", "limit": "{{ p.returnAll | eq:'true' | then:2000 | default:p.limit }}" },
"pagination": { "type": "cursor", "cursor_path": "cursor", "has_more_path": "has_more", "results_path": "entries",
  "next": { "method": "POST", "path": "https://api.dropboxapi.com/2/files/list_folder/continue", "body": { "cursor": "{{ page.cursor }}" } } }
```
Elasticsearch point-in-time paging sends `"search_after": "{{ page.cursor }}"` with `cursor_path: "hits.hits.-1.sort"` and the PIT id of the previous page (`"pit": {"id": "{{ res.pit_id }}"}`); a `before` step opens the PIT and an `also` step closes it.

## 9. Output

`"output": {"path": "data", "split": true}` — `path` (dot path, `-1` ok)
selects part of the JSON response; `split` (default true) turns an array into
one item per element. Objects are one item; scalars become `{"data": v}`; a
non-JSON body becomes `{"data": "<text>"}`; an empty body (e.g. 204) becomes
`{"success": true}`; a path that resolves to nothing yields no items.

**Follow-up requests (`also`).** Some n8n operations make several calls (update fields, then tags, then star). List them in order under `also`; each runs after the main request with `res` bound to the main response (on a paginated op: `res` = the last page, `main` = the first), auth injected the same way, and is skipped when its `when` template renders empty or false. The output is still the main response.
```json
"also": [
  { "when": "{{ p.updateFields.tags }}", "method": "PUT", "path": "/contacts/{{ res.id }}/tags", "body": { "tags": "{{ p.updateFields.tags }}" } },
  { "when": "{{ p.updateFields.starValue }}", "method": "PUT", "path": "/contacts/{{ res.id }}/star", "body": { "star_value": "{{ p.updateFields.starValue | int }}" } }
]
```
`"for_each": "{{ p.tagIds }}"` on an `also` step sends it once per list element with `el` bound (`"path": "/contacts/{{ res.id }}/tags/{{ el }}"`). A step with `"upload": true` and `"body_type": "multipart"` sends its `body` fields plus the file part (S3-style presigned POST: request a slot with `{{ bin.file_name }}`/`{{ bin.mime_type }}` — `bin` is filled for every step when the op declares `binary.upload_field` — then upload, then create; set `binary.upload_when: "false"` so only the step uploads). A step with `"upload": true` sends the op's binary (`binary.upload_field`/`property`) as its raw body with the binary's Content-Type (LinkedIn: register an upload, PUT the bytes to `{{ pre.value.uploadUrl }}`, then create the post). A step with `"auth": false` is sent without the credential (presigned S3/GCS/Azure SAS URLs, e.g. a Transcribe transcript file). Step keys: `when`, `for_each`, `upload`, `auth`, `method`, `path`, `base_url`, `query`, `body`, `body_type`, `headers`. A step error fails the item like the main request.

**Bulk variant (`bulk`).** n8n's per-item ops with a "bulk operation" option send every item in one request instead. `"bulk": {"when": "{{ p.options.bulkOperation }}", "size": 50, "method": "POST", "path": "/_bulk", "body_type": "ndjson", "body": …, "output": {…}, "error_when": …, "error_message": …}` — when `when` renders true for the first item, every item's params are resolved and checked as usual, and the op sends this request once per `size` items (default: all in one), with `batch` = the chunk's items as `{p, item, index}` (their params, JSON and position), `items` = their JSON, `p` = the chunk's first params. `output` and `error_when` shape the bulk response (the op's `output`, `before`, `also`, pagination and binary don't apply). Elasticsearch create:
```json
"body": { "$map": "{{ batch }}", "$to": { "$list": [
  { "index": { "_index": "{{ el.p.indexId }}", "_id": "{{ el.p.additionalFields.documentId }}" } },
  { "$merge": "{{ el.p.fieldsUi.fieldValues | kv:'fieldId,fieldValue' }}" } ] } },
"output": { "path": "items", "to": { "$merge": ["{{ el.index }}", "{{ el.create }}", "{{ el.update }}", "{{ el.delete }}"] } },
"error_when": "{{ res.errors }}"
```

**Batch operations (`batch: true`).** n8n's batchCreate/batchUpdate/batchDelete (with `autoMapInputData`) send ONE request for all input items. Mark the op `"batch": true`: it runs once, params come from the first item, and `items` is the list of every item's JSON:
`"body": { "items": { "$map": "{{ items }}", "$to": { "fields": "{{ el | omit:p.inputsToIgnore }}" } } }`.

**Collecting before steps.** A `before` step with `for_each` runs once per element (`el`, `idx`; with `upload: true` and a list of binary property names, each element is uploaded) and its `pres.<i>`/`pre` entry is the LIST of responses (Twist uploads each attachment, then sends `attachments: {{ pre | json }}`).

**Before request(s) (`before`).** One step — or a list of steps run in order, each seeing the earlier responses (`pres.0`, `pres.1`, …; `pre` = the latest) — sent before the main one (same keys as an `also` step); its decoded response is the `pre` root. Google Sheets append reads the header row, then orders the item by it:
```json
"before": { "method": "GET", "path": "/v4/spreadsheets/{{ p.documentId | rl }}/values/{{ p.sheetName | rl }}!1:1" },
"body": { "values": [ "{{ item | row:pre.values }}" ] }
```
**Output shaping** (applied in this order, also to paginated results; a single-object response is shaped as a one-element list):
- `"each": "invoice"` replaces each element by its value at that path (Chargebee `[{"invoice": {…}}]` → the invoices).
- `"expand": {method, path, query, …}` sends one request per element (`el` = the element) and replaces it with the response (Gmail lists ids, then GETs each message with `format`).
- In `to`, `match` and `expand` templates `res` is the whole decoded response (the first page for paginated ops), so per-row shaping can read response-level headers.
- `"schema_rows": "schema.fields"` (a path in the WHOLE response) turns BigQuery rows `{f: [{v}]}` at `output.path` into objects keyed by the schema's field names (RECORD recurses, REPEATED → list, BOOLEAN → bool); `"numbers": "{{ p.options.returnAsNumbers }}"` casts INTEGER/NUMERIC/FLOAT.
- `"firestore": "{{ p.simple }}"` decodes Firestore documents into n8n's simple shape `{_name, _id, _createTime, _updateTime, …fields}` (use `"each": "found"` / `"document"` first for batchGet / runQuery).
- `"from": "{{ res | values }}"` (template over `res`) replaces the decoded response before `path`/`split` (npm versions: `"{{ res.time | entries:'version,published_at' }}"`).
- `"merge": true` folds a list of objects into one object (Hue update: `"each": "success", "merge": true`).
- `"skip": 1` drops the first n elements (NextCloud lists the folder itself first); `"sort": "time"` orders by a path (numbers numerically, else as text), `"sort_desc": true` descending (npm getVersions).
- `"limit": true` applies n8n's `returnAll`/`limit` params to a list the API returns whole (client-side slicing; replaces the `__no_next_page__` pagination trick).
- `expand` with `"expand_merge_key": "transcript", "expand_merge_path": "callTranscripts.0.transcript"` sets that key on the element instead of replacing it.
- `"to": {"id": "{{ el.id }}", "$merge": "{{ el.values }}"}` reshapes each element (`el` = element; same rules as `$map`'s `$to`); `"to_when": "{{ p.options.rawData | ne:'true' }}"` skips the reshape when it renders empty/false. Runs after `match`.
- `"match": {"rows": "{{ p.filtersUI.values }}", "column": "lookupColumn", "value": "lookupValue", "combine": "{{ p.combineFilters }}", "first": "{{ p.options.returnFirstMatch }}"}` keeps elements whose field named by each row's `column` key equals its `value` key — all rows (AND, default) or any (`combine` = OR); `first` keeps only the first match. No rows → everything is kept. `rows` may give one object for a single condition: `"rows": "{{ p | pick:'lookupColumn,lookupValue' }}"`.
- `"where": "{{ el.file }}"` keeps only the elements for which the template (`el` = the element) renders true — client-side filtering for APIs without a server-side filter (Graph delta: files only, no folders or the root). Runs after `download`, before `match`.
- `"match_after_to": true` runs `match` after `to` instead of before, so it filters the reshaped elements (Excel table rows `{values: [[…]]}` zipped with their column names, then matched by column; `first` = `{{ p.options.returnAllMatches | ne:'true' }}`).

**Files per element (`output.download`).** Fetches files for every output
element (after `each`/`expand`, before `match`/`to`) and puts them on that
element's item: the bytes in the item's binary data and
`{fileName, mimeType, fileSize}` under `json.binary.<property>`. The element
JSON stays the item (n8n's "download attachments" options). Keys: those of a
step (`when`, `auth`, `method`, `path`, `base_url`, `query`, `headers`, …;
no `for_each`) plus `files`, `property`, `file_name`, `mime_type`.
```json
"output": { "path": "fields.attachment", "download": {
  "when": "{{ p.download }}", "method": "GET",
  "path": "/api/3/attachment/content/{{ el.id }}",
  "property": "{{ p.binaryProperty }}", "file_name": "{{ el.filename }}", "mime_type": "{{ el.mimeType }}" } }
```
- `when` and the templates see `el` = the element. A step `base_url` is not
  inherited from the op: repeat a per-op `base_url` when the path is relative.
- `files` (a template or builder giving a list; nested lists are spread one
  level) downloads several files per element: each entry becomes `el`, the
  element `up` and its position `idx`. NocoDB, one binary per attachment
  named `<field>_<index>`:
  `"files": {"$map": "{{ p.downloadFieldNames | split }}", "$to": {"$map": "{{ up.fields | get:el | fromjson }}", "$to": {"url": "{{ el.signedUrl | default:el.url }}", "property": "{{ up }}_{{ idx }}"}}}`
  with `"path": "{{ el.url }}", "property": "{{ el.property }}"`.
- `property` defaults to `data`, `mime_type` to the response's Content-Type,
  `file_name` to its Content-Disposition name. The download is sent without
  `Accept: application/json`; `"auth": false` leaves the credential off
  (presigned file URLs: Notion files, Google Slides thumbnails).

**Tree walks (`output.walk`).** Some n8n operations return a whole sub-tree: the API lists the records below an id only to a fixed depth, so the records at that depth are listed again until nothing new turns up. `walk` appends what it finds to the main result (the main response stays first; a list result keeps its elements):
```json
"output": { "walk": {
  "when": "{{ p.includeDescendants }}",
  "start": "{{ res.id }}",
  "request": { "method": "GET", "path": "/wiki/api/v2/pages/{{ el }}/descendants", "query": { "depth": 10 } },
  "pagination": { "type": "next_url", "next_url_path": "_links.next", "limit_param": "limit", "page_size": 250 },
  "results_path": "results",
  "descend": "{{ el.type | in:'page,folder' | then:(el.depth | eq:'10') }}",
  "keep": "{{ el.type | eq:'page' }}",
  "max": "{{ p.maxPages | sub:1 }}",
  "fetch": { "method": "GET", "path": "/wiki/api/v2/pages", "query": { "id": "{{ el | csv }}" }, "chunk": 250, "results_path": "results" } } }
```
- `start` (a template or builder; `res` = the main response, or on a paginated op the list of every result) gives the first frontier: an id or a list of ids. Outlook folders with children: `{"$map": "{{ res }}", "$to": "{{ el.childFolderCount | ne:'0' | then:el.childFolderCount | then:el.id }}"}`. Each round sends `request` once per frontier id (`el` = the id), every page of it when `pagination` is set (any §8 type; `results_path` says where the records are). `request` and `fetch` take a step's `method`, `path`, `base_url`, `query`, `body`, `body_type`, `headers` and `auth`.
- Per record (`el` = the record): `id` (default `{{ el.id }}`) — records with an empty or already seen id are skipped (start ids count as seen, so cycles end); `descend` — its id joins the next round's frontier; `keep` (default true) — it is collected. The walk ends when a round finds nothing to descend into or `max` records are kept; it stops mid-page at the cap. `max` empty = no cap; `0` or less walks nothing.
- `fetch` (optional) replaces the kept records by full objects: the ids go out in chunks of `chunk` (default 100) with `el` = the chunk's list of ids, the objects at its `results_path` are matched back by `key` (default `{{ el.id }}`), in walk order; ids the API does not return are dropped.
- The walked records go through the rest of `output` (`each`, `to`, …) with the main result.

**Header-row output.** `"output": {"path": "values", "header_row": true}` turns `[[h1,h2],[v1,v2],…]` into one item per row keyed by the first row.

**Polling (`poll`).** After the main request, `"poll": {"method": "GET", "path": "/projects/{{ p.projectId }}/queries/{{ res.jobReference.jobId }}", "until": "{{ res.jobComplete }}", "interval_ms": 1000, "max_wait_ms": 300000}` repeats the request (with `res` = the latest response) until `until` is true; the last response becomes the op's response. Defaults: 1 s interval, 5 min max wait. `"max_wait_seconds": "{{ p.timeout }}"` (a template giving seconds) takes the wait from a param instead. `main` stays the main request's reply, also in `output` templates (Airtop agent runs: `"invocationId": "{{ main.invocationId }}"` beside the polled result).

**Waiting steps (`repeat`).** A `before` or `also` step with `"repeat": {"until": "{{ res.data.status | eq:'available' }}", "interval_ms": 1000, "max_wait_ms": 300000}` is resent (with `res` = its latest response) until `until` is true; that response is the step's (`pre`/`pres.<i>`). Same defaults and `max_wait_seconds` as `poll`. Use it when a later step must wait for an earlier one's result (upload a file, wait until it is processed, then attach it).

**Event streams (`stream`).** An operation or a `before`/`also` step with `"stream": {"until": "{{ res.event | eq:'file_status' }}", "timeout_ms": 300000}` reads its response as server-sent events (`text/event-stream`; `Accept` is set to it) instead of one body. Each event's `data:` lines (joined with newlines) are decoded like a body (JSON, else text) and bound to `res`; `event:`, `id:`, `retry:` and comment lines are ignored. The first event for which `until` renders true becomes the response — the op's (for `output`) or the step's (`pre`/`pres.<i>`) — and is checked by `error_when` (so `"error_when": "{{ res.status | eq:'upload_failed' }}"` fails on a failure event). The request fails when the server closes the stream first or after the timeout (default 5 min; `"timeout_seconds": "{{ p.additionalFields.timeout | default:30 }}"` takes it from a param). Not with pagination, `bulk` or a binary download. Airtop waits for a session's file event:
```json
"method": "GET", "path": "/sessions/{{ p.sessionId }}/events", "query": { "all": "true" },
"stream": { "until": "{{ res.event | eq:'file_status' | then:(res.status | eq:'available') }}", "timeout_seconds": "{{ p.additionalFields.timeout | default:30 }}" },
"output": { "to": { "fileId": "{{ res.fileId }}", "downloadUrl": "{{ res.downloadUrl }}" } }
```

## 10. Binary data

Workflow items carry files in `item.binary[<property>]` (n8n's
"binary property", default `data`).

```jsonc
"binary": {
  "download": true,                    // response body is a file
  "property_field": "binaryPropertyName", // op field naming the output property (default "data")
  "upload_field": "binaryPropertyName",   // op field naming the input property to send
  "multipart_field": "file",           // multipart: form field of the file part (default "file")
  "file_name": "{{ p.fileName }}",     // multipart: file name (default: n8n fileName or the property)
  "mime_type": "{{ p.mimeType }}"      // part / raw Content-Type (default application/octet-stream)
}
```

- Download: the output item is `{"mime_type", "file_name", "size",
  "binary_property"}` with the bytes in `binary[<property>]`.
- Upload: bytes come from the input item's `binary[<property>]`, else a
  base64 string at `item.json[<property>]` or n8n's
  `item.json.binary.<property>.data`. With `body_type: multipart` the file is
  one part beside the body fields; otherwise (`raw`) it is the whole body.
- `upload_field`/`property_field` must be declared op fields (n8n uses
  `binaryPropertyName`).

`binary.download_when` (template) decides per run whether the response is a file (GCS `alt=media`; for a file that a JSON response links to, see `output.download` in §9); it overrides `download`. `binary.upload_text` (template): when it renders non-empty, that text (Content-Type text/plain) is uploaded instead of the item's binary (GCS create from text).

`binary.upload_url` (template): when it renders an http(s) URL, the engine first downloads that file (GET, without the credential) and uses it as the upload instead of the item's binary — name from the URL's last segment, type from the response's Content-Type (n8n's "upload from URL" sources; Airtop `"upload_url": "{{ p.source | ne:'binary' | then:p.url }}"`).

`binary.upload_when` (template) decides per run whether to upload (n8n `sendBinaryData`); without the upload a multipart op sends its `body` as JSON (or nothing). `binary.multipart_field` may be a template, and `binary.property` (template) overrides the property read from `upload_field` — n8n's `"formField:binaryProperty"` values: `"multipart_field": "{{ p.binaryPropertyName | split:':' | get:'-2' | default:'file' }}"`, `"property": "{{ p.binaryPropertyName | split:':' | get:'-1' }}"`.

**Files inside JSON.** Upload: `bin.base64` is the upload's bytes base64-encoded, for APIs that take file content in a JSON body (GitHub/GitLab `"content": "{{ p.binaryData | then:bin.base64 | default:(p.fileContent | base64) }}"` with `upload_when: "{{ p.binaryData }}"`). Download: `binary.download_base64` (template over `res`, e.g. `"{{ res.content }}"`) decodes a base64 file from the JSON response onto the item's binary (property from `property_field`, name from the `file_name` template, MIME type from `mime_type` or the name) and keeps the JSON as the item; gate it with `download_when`.

**Several base64 files in one response (`download_each`).** `"download_each": "data"` (a response path to a list) turns each element into one item: `download_base64`, `file_name` and `mime_type` render with `el` = the element, the item's JSON is the element without the key that held the base64 text, plus `binary_property`, `file_name`, `mime_type` and `size` (OpenAI image generations: `"download_base64": "{{ el.b64_json }}"`). A file download (`download`) of an op that uploads nothing takes its name and type from `file_name`/`mime_type` when the response's headers give none.

**Fields inside collections.** `upload_field` and `property_field` may be dotted to read a key of a collection field (`"upload_field": "options.binaryPropertyNameReference"`); `binary.property` (a template) also names a download's output property (`"{{ p.options.binaryPropertyOutput | default:'data' }}"`).

`binary.fallback_body_type` (json/form/multipart/raw/none) is the body type when `upload_when` turns the upload off (LINE Notify stays a form without an image).

**Large files in chunks (`upload_session`).** `"binary": {"upload_field": "binaryPropertyName", "upload_session": {"create": {"method": "POST", "path": "/items/{{ p.parentId }}:/{{ p.fileName }}:/createUploadSession", "body": {"item": {"@microsoft.graph.conflictBehavior": "replace"}}}}}` — when the upload is larger than `min_size` bytes (default 4 MiB) the engine sends `create` (a step, with auth), reads the upload URL at `url_path` (default `uploadUrl`) of its response, and PUTs the file there in `chunk_size` pieces (default 10 MiB; Graph wants multiples of 320 KiB) with `Content-Range: bytes a-b/total` and no credential (the URL is pre-authorised). The last chunk's response is the op's response. Smaller files go in the op's single request as before (OneDrive / SharePoint upload).

**Several files.** `binary.properties` (a template giving a list or comma text, e.g. n8n's `"attachments": "data,data2"`) uploads each property: in a multipart body one part per file, with `el = {property, index}` in the `multipart_field` template (`"attachment[{{ el.index }}]"`); for JSON bodies `bin.files` is the list of `{property, file_name, mime_type, file_extension, base64, file_size}` (`{"$map": "{{ bin.files }}", "$to": {"name": "{{ el.file_name }}", "content": "{{ el.base64 }}"}}`). `binary.properties` without `upload_field` only fills `bin.files` (nothing is uploaded), for bodies that embed the files themselves — Gmail builds a MIME message: `"binary": {"properties": "{{ p.options.attachmentsUi.attachmentsBinary | pluck:'property' | join:',' }}"}` with `{{ bin.files | mime_parts:'monoagent_mixed' }}` in its raw body.

## 11. `extends` (apps with a native Go node)

`{"id": "github_more", "extends": "service.github", …}` registers no node
type. The native node's schema gets the package's `resource` select (first
option `""` = the native operations), per-resource `operation` selects and
fields appended after the native fields; without `resources` the package's
operations are appended to the native `operation` select's options. At run
time the native node calls `apipkg.ExecuteExtension(ctx, "service.github",
input, config)` from its `default:` branch when `config["resource"]` is set
or the operation is unknown (`apipkg.HasExtension(nativeType, resource, op)`
tells whether a package handles it). Wave 2 adds that 3-line hook per native
node. Only one package may extend a given native node.

An `extends` package's operation without a resource must not reuse one of the native node's operation names (validation rejects it): it would take that native operation over at run time.

## 12. n8n compatibility

- `n8n_types` — n8n node types imported as this package; `n8n_trigger_types`
  — trigger types of the same app. The importer rebuilds them from its
  trigger tables (`internal/n8nimport/app_triggers_*.go`): a webhook trigger
  becomes `trigger.webhook`, a polling trigger `trigger.schedule` → this
  package's list operation → `core.remove_duplicates`. A new trigger type
  needs a line in one of those tables (`TestEveryCatalogTriggerIsMapped`
  checks). A type may appear in one package only.
- `apipkg.N8nIndex()` gives the importer, per n8n type: package id,
  `node_type`/`extends`, default resource and default operation per resource,
  credential mappings (n8n credential type → platform + field map) and per
  op `param_map` (= `n8n_param_map`) and field keys.
- Only add `n8n_param_map` when you had to rename an n8n param.

`n8n_operation_param` (package): the n8n param that selects the operation when it isn't `operation` (FileMaker uses `action`). A config without `operation` takes that param's value, so imported nodes run unchanged; `N8nIndex` exposes it as `operation_param`.

Harness: requests after `before` steps are checked with `inject` rendered against those steps' `responses` (`pre`/`pres`), so session-token auth (`"Bearer {{ pre.response.token }}"`) is tested as it runs.

When a config has no `resource` and the default resource lacks the operation, an operation name that exactly one resource has is used (n8n hides `resource` in some modes, e.g. Discord webhook `sendLegacy`).

**Trigger-only apps** (n8n has only a `…Trigger` node, e.g. Eventbrite, Figma): write a package with `"operations": []` and the app's `n8n_trigger_types` (plus its credentials). It registers no action node; it records the triggers for the importer and the trigger work.

`n8n_version_defaults` (package): `{"1": {"apiVersion": "v4"}}` — per n8n node `typeVersion`, defaults the importer fills in for params an exported node omits (n8n doesn't export defaults, and some changed between node versions). A key may be a comparison (`<3`, `<=2.2`, `>=2`, `>1`) that applies to every matching version; an exact key wins over comparisons. A package can thus give an imported node a param n8n never had, to reproduce version-specific behaviour: Notion declares a `keyCase` field (`snake` default) used as `{{ el.key | snake:p.keyCase }}` and `{"<3": {"keyCase": "ascii"}}`.

`n8n_resource_param` (package): the n8n param that selects the resource when it isn't `resource` (uProc: `group`).

## 13. Testing a package

`catalog/testdata/<id>.json` has one case per operation, keyed
`"<resource>.<operation>"` (just `"<operation>"` without resources):

```jsonc
{
  "task.create": {
    "params": { "project": "2203306141", "content": "Buy milk", "options": { "priority": 4 } },
    "credential": { "access_token": "x" },   // optional; default: every credential field = "test-<key>"
    "input": { "id": 1 },                    // optional input item JSON
    "response": { "id": "1" },               // optional JSON for the first response (default {})
    "response_status": 200,                  // optional
    "expect": {
      "method": "POST",
      "path": "/api/v1/tasks",               // full path as the server sees it (base_url path included), escaped
      "query": { "limit": "50" },            // EVERY query param sent (auth query params excepted); arrays for repeats
      "body": { "content": "Buy milk", "project_id": "2203306141", "priority": 4 },
      "headers": { "X-Custom": "v" },        // optional, checked when given
      "items": 1                             // optional: number of output items
    }
  }
}
```

`TestCatalog/<id>` checks, for the package:

1. it parses and validates; its `id` equals the file name;
2. every operation has a case and every case names an operation;
3. per case, the first request's method, path, query (exact), body (exact
   JSON; form → `{"k": "v"}`; multipart → fields plus
   `"file:<name>:<content>"`; when `body` is absent, no body may be sent
   beyond `{}`), headers you list, and the item count;
4. auth: every `inject.headers`/`inject.query` value arrived rendered with
   the case's credential; Basic and SigV4 `Authorization` headers present;
5. required fields: dropping each required field without a default fails
   with an error naming the field, and no request is sent.

All traffic goes to an `httptest` server (any host is rerouted); nothing
leaves the machine. The server answers the first request with `response`
and later ones with `{}`, so paginated ops stop after page one. Failure
messages name the package, op and what differed, e.g.
`todoist · task.getAll: query: got {"limit":"50","query":"x"}, want {"query":"x"}`.

**Multi-request ops** (`before`, `also`, `output.expand`, pagination): `"responses": [ {…}, {…} ]` answers the requests in order (later ones get `{}`; overrides `response`). `expect` checks the MAIN request — the first one with the expected method and path (so `before` steps skipped by their `when` don't shift it; otherwise the one after the `before` steps). A path segment `<any>` in an expected `path` matches any non-empty segment (`/send/m.room.message/<any>` for `{{ uuid }}` ids). A value of `"<any>"` in expect `query`/`body` matches any present non-empty value (`{{ uuid }}`, timestamps). To check every request in order use `"expect_requests": [ {method, path, query, body, headers}, … ]`. A response `{"$sse": [{…}, {…}]}` is sent as a `text/event-stream` with one `data:` event per element (for `stream` ops and steps).
```json
"values.append": {
  "params": { "documentId": "abc", "sheetName": "Main" },
  "input": { "name": "ada" },
  "responses": [ { "values": [["name", "email"]] }, { "updates": {} } ],
  "expect": { "method": "POST", "path": "/v4/spreadsheets/abc/values/Main:append", "body": { "values": [["ada", ""]] } }
}
```

## 14. Worked example

`catalog/acme.json`:

```json
{
  "$schema": "apipkg.v1",
  "id": "acme",
  "title": "Acme",
  "description": "Widgets in Acme",
  "category": "app",
  "n8n_types": ["n8n-nodes-base.acme"],
  "base_url": "https://{{ cred.subdomain }}.acme.test/api",
  "credentials": [{
    "platform": "acme", "name": "Acme", "n8n_credential": "acmeApi",
    "n8n_field_map": { "apiKey": "access_token", "subdomain": "subdomain" },
    "methods": ["bearer"],
    "fields": [
      { "key": "subdomain", "label": "Subdomain", "required": true },
      { "key": "access_token", "label": "API key", "secret": true, "required": true }
    ],
    "inject": { "headers": { "Authorization": "Bearer {{ cred.access_token }}" } }
  }],
  "resources": [ { "value": "widget", "label": "Widget" } ],
  "operations": [
    {
      "resource": "widget", "operation": "create", "title": "Create a widget",
      "method": "POST", "path": "/widgets",
      "fields": [
        { "key": "name", "label": "Name", "type": "text", "required": true },
        { "key": "additionalFields", "label": "Additional fields", "type": "collection",
          "default": {}, "options": ["color", "size"] }
      ],
      "body": { "name": "{{ p.name }}", "$merge": "{{ p.additionalFields }}" },
      "output": { "path": "widget" }
    },
    {
      "resource": "widget", "operation": "getAll", "title": "Get many widgets",
      "method": "GET", "path": "/widgets",
      "fields": [
        { "key": "returnAll", "label": "Return all", "type": "boolean", "default": false },
        { "key": "limit", "label": "Limit", "type": "number", "default": 50 },
        { "key": "filters", "label": "Filters", "type": "collection", "default": {}, "options": ["color"] }
      ],
      "query": { "color": "{{ p.filters.color | lower }}" },
      "pagination": { "type": "page", "page_param": "page", "limit_param": "per_page",
                      "page_size": 100, "results_path": "widgets" }
    }
  ]
}
```

`catalog/testdata/acme.json`:

```json
{
  "widget.create": {
    "params": { "name": "Sprocket", "additionalFields": { "color": "red", "size": "" } },
    "response": { "widget": { "id": 1 } },
    "expect": { "method": "POST", "path": "/api/widgets",
                "body": { "name": "Sprocket", "color": "red" }, "items": 1 }
  },
  "widget.getAll": {
    "params": { "limit": 5, "filters": { "color": "RED" } },
    "response": { "widgets": [ { "id": 1 }, { "id": 2 } ] },
    "expect": { "method": "GET", "path": "/api/widgets",
                "query": { "color": "red", "page": "1", "per_page": "5" }, "items": 2 }
  }
}
```

Then `go test ./internal/apipkg -run 'TestCatalog/acme$' -v` (this example
passes as written). The shipped
`todoist.json` (cursor pagination, `| rl`, `then`, `$merge`, OAuth),
`typeform.json` (page and `items.-1.token` cursor pagination, triggers) and
`calendly.json` (`output.path`, cursor in a nested path) are full examples.

## 15. Checklist

- [ ] `id` = file name; `n8n_types` / `n8n_trigger_types` filled from the n8n node.
- [ ] Field keys are n8n's parameter names; first op per resource = n8n default.
- [ ] Every n8n default you rely on is a field `default`.
- [ ] Credential platform is new (not built-in) or uses `reuse_platform`;
      `n8n_credential`, `_alt` and `n8n_field_map` set.
- [ ] Lists paginate and honour `returnAll`/`limit`; `output.path` set where
      the API wraps results.
- [ ] Every op has a test case with an exact `expect.query` and `expect.body`.
- [ ] `go test ./internal/apipkg -run 'TestCatalog/<id>$'` passes.

## 16. Additions to the design contract

Optional keys the engine added (the design's keys are unchanged):

| Where | Key | Meaning |
|---|---|---|
| package | `n8n_trigger_types` | n8n trigger types of the app (imported as webhook/schedule triggers) |
| credential | `n8n_credential_alt` | further n8n credential types |
| credential | `n8n_field_map` | n8n credential field → our field |
| resource | `description` | help text |
| operation | `description`, `base_url` | help text; per-op base URL |
| pagination | `page_size`, `start_page`, `max_pages`, `param_in` | §8 |
| binary | `property_field`, `multipart_field`, `file_name`, `mime_type` | §10 |
| inject.aws_sigv4 | `access_key_id`, `secret_access_key`, `session_token` | key templates |
| templates | filters `float`, `string`, `upper`, `rl`, `then`; `default:<path>`; `$merge` lists | §5 |
| templates | filters `eq`, `ne`, `kv`, `omit`, `fromjson`, `epoch_ms`, `epoch`; roots `el`, `res`; `$map`/`$to`, `$concat` | §5 |
| credential | `scope_group` (umbrella scope check) | §7 |
| operation | `before` (pre-request → `pre`); output `header_row`, `each`, `expand`, `match` | §9 |
| operation | `poll` (repeat until done); output `schema_rows`, `numbers` | §9 |
| templates | filters `pick`, `firestore_value`, `firestore_fields`; output `firestore`; binary `download_when`, `upload_text` | §5, §9, §10 |
| templates | filter `entries`; output `to`/`to_when` | §5, §9 |
| templates | parenthesised expression arguments; filter `sheetgid`; `row` null mode; `p.*` in `inject`; templated `output.path`/`each` | §5 |
| templates | multi-argument filters `find`, `find_row`; filters `get`, `add`, `sub`, `colnum`, `colname`; `$entries`/`$key`/`$value` | §5 |
| templates | filters `prefix`, `suffix`, `base64`, `base64url`, `row`; roots `pre`, `uuid` | §5 |
| credential | method `client_credentials`; `oauth.client_id_field`, `client_secret_field`, `client_auth` (`auth_url` optional for it) | §7 |
| operation | `also` (follow-up requests), `batch` (one request for all items) | §9 |
| templates | filters `split`, `date`; `kv` key prefix; root `items` | §5 |
| templates | filters `len`, `urlencode`; templated header names, headers `$merge` | §5, §6 |
| operation | `scope_group` (overrides the credential's) | §7 |
| output | `match_after_to`; `match.rows` may be one object | §9 |
| binary | `upload_session` (`create`, `min_size`, `chunk_size`, `url_path`) | §10 |
| package, operation | `error_when`, `error_message` (fail a 2xx body that reports an error) | §6 |
| output | `download` (per-element files: `files`, `property`, `file_name`, `mime_type`) | §9 |
| templates | root `up`; filters `md5`, `sha1`, `sha256`, `mod`, `weekday`, `dotnet_date`; `snake:'ascii'`, `path:'query'`; `snake` follows change-case v5 | §5 |
| schema output | `visible_for`, `apipkg`, `extended_by` | exact op visibility; package id; extending packages |
| credential | method `oauth1`, `inject.oauth1`, `oauth.request_token_url`, `oauth.signature_method` | §7 OAuth 1.0a |
| credential | `oauth.connect_fields` (templated per-tenant OAuth endpoints), `oauth.metadata` | §7 |
| credential | AWS AssumeRole via connection `role_arn` (`external_id`, `role_session_name`) | §7 |
| templates, binary | filters `wrap`, `mime_parts`; `binary.properties` without `upload_field` | §5, §10 |
| output | `where` (keep elements by a template) | §9 |
| templates | filter `sealbox` | §5 |
| output | `walk` (tree walk over a growing id frontier, optional chunked `fetch`) | §9 |
| templates | filters `unique`, `set`, `not`, `bind`, `base64_chunks`; builder key `$unique` | §5 |
| operation, request | `bulk` (one request per chunk of items, root `batch`); body type `ndjson` | §6, §9 |
| pagination | `next` (continuation request for pages 2+, `page.cursor`); `also` runs after paging | §8, §9 |
| steps | `error_when` / `error_message` per step | §6 |
| package | `n8n_version_defaults` comparison keys (`<3`) | §12 |
| operation, steps | `stream` (read server-sent events until one matches); step `repeat`; poll `max_wait_seconds`; binary `upload_url` | §9, §10 |
| package | `webhook` (app trigger registration: `fields`, `create`, `id`, `extra`, `delete`, `check`, `verify`); root `hook`; filter `map` | §17 |
| templates | literal braces `\{{` / `\}}`; `hdr` = the previous webhook step's response headers | §5, §17 |
| binary | `download_each` (one item per base64 file in a list); dotted `upload_field`/`property_field`; `property` for downloads; download `file_name`/`mime_type` fallback | §10 |

## 17. App webhooks (`webhook`)

Most n8n app triggers register a webhook with the vendor's API when the
workflow is activated and remove it when it is deactivated. A package's
`webhook` block does the same: apipkg registers a `workflow.WebhookRegistrar`
named after the package id, and a `trigger.webhook` with
`"register_with": "<id>"` uses it. On activation the engine serves the
webhook route, then calls `create`; on deactivation, deletion or a trigger
change it calls `delete`. The hook id and `extra` values are kept in the
`app_webhooks` table, so any process can remove the hook later. The vendor
must reach the URL: the node's `public_url` or `MONOAGENT_PUBLIC_URL` is the
public base (e.g. a tunnel), else activation fails and says so.

The engine's built-in registrars (`typeform`, `calendly`) keep their names:
a package with one of those ids gets no registrar, so leave its block out.
A package installed while the app runs is registered on the next start; an
updated block of an already registered package is used at once.

```jsonc
"webhook": {
  "fields": [                                  // the register config keys: n8n's trigger param names
    { "key": "events", "label": "Events", "type": "array", "required": true },
    { "key": "scope", "label": "Scope", "type": "text", "default": "team" }
  ],
  "create": [                                  // one or more steps, in order (pre/pres see earlier ones)
    { "method": "POST", "path": "/hooks",
      "body": { "url": "{{ hook.url }}", "events": "{{ p.events }}", "secret": "{{ hook.secret }}" } }
  ],
  "id": "{{ res.id }}",                        // the hook id to keep; empty → create fails
  "extra": { "secret": "{{ res.secret }}" },   // more values to keep (hook.extra.secret)
  "delete": [ { "method": "DELETE", "path": "/hooks/{{ hook.id }}" } ],
  "check": { "method": "GET", "path": "/hooks/{{ hook.id }}" },   // optional
  "verify": { "signature": "{{ hdr.X-Hub-Signature-256 }}", "prefix": "sha256=" }  // optional
}
```

Steps are the operation step shape (§9 `also`): `method`, `path`,
`base_url`, `query`, `body`, `body_type`, `headers`, `when`, `for_each`,
`auth` (false: send without the credential, e.g. a login step or an API on
another host with its own header), `error_when`. They are sent with the
package's credential injection, so OAuth 2/OAuth 1/basic/SigV4/header
credentials all work, and client-credentials tokens, assumed roles and
metadata are fetched first as for operations.

Template roots:

| Root | What |
|---|---|
| `p.<key>` | the register config: the trigger node's config with `register_config` merged on top, field defaults filled in; every `p.X` must be one of `fields` |
| `cred.<key>` | the node's connection (`credential_id`) |
| `hook.url`, `hook.path`, `hook.secret` | the public URL the vendor calls, its `/webhook/` path, and a random secret, stable per node (give it to vendors that sign deliveries) |
| `hook.id`, `hook.extra.<key>` | the stored registration (delete, check, verify) |
| `hook.workflow_id`, `hook.node_id` | the node |
| `pre`, `pres.<i>`, `el`, `idx` | as in `before` steps: earlier create steps, the `for_each` element |
| `res`, `hdr` | in `id`/`extra`: the last create response (a list of responses when that step has `for_each`) and its headers (`{{ hdr.Resource-Id }}`); in `check.exists_when`: the check response |
| `hdr` in a step | the previous step's response headers — a login step's session token: Form.io's `"inject": {"headers": {"x-jwt-token": "{{ hdr.X-Jwt-Token }}"}}` after a `POST /user/login` step with `auth: false` |

- **delete** runs every step; a 404 or 410 counts as already removed, other
  errors are reported (the engine logs the hook id so it can be removed by
  hand). One hook per element: `"for_each": "{{ p.listIds | split }}"` on
  create, `"extra": {"ids": "{{ res | pluck:'id' }}"}`, and
  `"for_each": "{{ hook.extra.ids }}"` on delete (Flow).
- **check** (optional) tells whether the stored hook still exists when a
  workflow is activated again with unchanged settings: 2xx yes (or
  `exists_when`, a template over `res`), 404/410 no — the engine then
  registers again. Without `check` the stored hook is reused.
- **verify** (optional) checks each delivery; a delivery that fails is
  answered 401 and starts no run:

| Key | Meaning |
|---|---|
| `signature` | template over `hdr` (the delivery's headers), `req.body` (its raw body as text), `hook`, `cred`, `p`: the signature to check (`{{ hdr.Stripe-Signature \| re:'v1=([0-9a-f]+)' }}`) |
| `message` | template for the signed text; default: the raw body (Stripe: `{{ hdr.Stripe-Signature \| re:'t=([0-9]+)' }}.{{ req.body }}`) |
| `secret` | template; default `{{ hook.secret }}`; `{{ hook.extra.secret }}` for a vendor-issued secret, `{{ cred.signing_secret }}` for an account one. An empty secret skips the check |
| `alg` | `sha256` (default), `sha1`, `sha512` — HMAC; `token`: the signature must equal the secret (GitLab `X-Gitlab-Token`, Telegram, a secret echoed in the body: `{{ req.body \| fromjson \| get:'secret' }}`) |
| `encoding` | `hex` (default, case-insensitive) or `base64` |
| `prefix` | stripped from the signature; a signature without it fails (`sha256=`) |
| `secret_encoding` | `hex` or `base64`: decode the secret into the HMAC key (Onfleet) |

URL checks some vendors send before delivering (Asana/Keap `X-Hook-Secret`,
Meta `hub.challenge`, SNS confirmations, …) are answered by the webhook
server itself; a block needs nothing for them.

**Testing.** `catalog/testdata/webhook/<id>.json` is required for every
package with a block; `TestCatalogWebhooks/<id>` runs it against the mock
server:

```jsonc
{
  "params": { "events": ["push"] },              // the register config
  "credential": { "access_token": "t" },         // optional, as in §13
  "create_responses": [ { "id": 314 } ],         // answers to the create steps, in order
  "response_headers": { "Resource-ID": "880" },  // optional, sent with every answer
  "expect_create": [ { "method": "POST", "path": "/repos/o/r/hooks", "body": { … } } ],
  "hook_id": "314",                              // what Register must return
  "extra": { "secret": "whsec_x" },              // optional
  "expect_delete": [ { "method": "DELETE", "path": "/repos/o/r/hooks/314" } ],
  "check": { "response": { "id": 314 }, "exists": true, "expect": { "method": "GET", "path": "…" } },
  "verify": { "headers": { "X-Hub-Signature-256": "sha256=…" }, "body": "{…}" }
}
```

The hook is `https://hooks.example.test/webhook/wh-path` with secret
`test-secret`, workflow `wf-1`, node `node-1`. Every request is compared as
in §13 (method, path, exact query and body, listed headers, credential
injection — pin `"Authorization"` in `headers` for steps with `auth: false`).
The harness also checks that a 404 on delete counts as removed, that a 404
on check means gone, that the signed `verify` sample passes and that the
same sample with a changed body fails.

The n8n importer sets `register_with`, `register_config` (the trigger's
params named by `fields`) and `credential_id` on the `trigger.webhook` it
writes for the package's `n8n_trigger_types`.
