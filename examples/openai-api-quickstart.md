# OpenAI-compatible API quickstart

`monoagentcli` can serve the agent runtimes installed on your machine
(claude, codex, antigravity, …) through standard OpenAI endpoints, so any
OpenAI SDK or tool works by changing its base URL and key. This guide
creates a key, starts the server, calls it with curl and the SDKs, saves its
settings, and runs it on a headless Linux server. Paths and schemas:
`internal/httpapi/openapi.yaml`. Threat model: `SECURITY.md`.

Today it serves `GET /v1/models`, `GET /v1/models/{id}`,
`POST /v1/chat/completions` (JSON and `"stream": true`) and
`POST /v1/images/generations` (see "Make images" below). OpenAI tool calling
works on claude and codex (see "Call your own tools" below). The model `auto`
lets Jev pick the model of each request once you switch it on (see "Let Jev
pick" below); until then
asking for it is a 404 `model_not_found` that says what is missing. A `404 page
not found` in plain text instead means the server running is older than this
API and has no `/v1` at all (`monoagentcli daemon` and `httpapi` serve it only
from a build that includes it).

## 1. Create a key

```bash
monoagentcli --profile work api key create --name my-app
# sk-ma-…            ← printed once; only its SHA-256 is stored
```

A key belongs to the profile you name with `--profile`: its
requests run as that profile, add only that profile's knowledge and work in that
profile's folder (what a runtime can read on the machine is set by its
confinement class: see `SECURITY.md`). The commands below read the key from
`$KEY`. In a script, stdout is the key alone, so capture it instead (a name
is unique among a profile's active keys: use this form or the one above, not
both):

```bash
KEY=$(monoagentcli --profile work api key create --name my-app) && export KEY
```

Add `--context` to give requests made with the key excerpts of the profile's
own knowledge (its documents and captures); without it the key reaches a
plain model. By default a context key is served only by the chat-only models
(see `api models`), because the excerpts include captured web pages that
nobody vetted and a runtime with native tools could be steered by them. When
you want a coding agent on your own machine to have your notes, raise the
limit on purpose when you start the server: `--context-confinement
sandboxed` (or `any`, or `MONOAGENT_API_CONTEXT_CONFINEMENT`). It never goes
above what the listener itself serves. Manage keys with `api key list`,
`show`, `update` and `revoke` (revoking takes effect on the next request). No
GUI and no keyring are needed.

## 2. Start the server

```bash
monoagentcli httpapi        # or: monoagentcli daemon
```

The API is served at `http://127.0.0.1:9322/v1` while the HTTP API listener
is loopback, which is the default. Only one process per home (`~/.monoagent`)
serves `/v1` at a time, because the working folders are emptied around every
turn: a second `httpapi` says so and serves its other routes without `/v1`, and
a second `daemon` is refused outright (one daemon per home). Check what it
serves:

```bash
monoagentcli --profile work api status     # listeners, key count, whether they answer
monoagentcli --profile work api models     # every model, with its confinement class
```

`api models` prints the policy, a note on whose settings those are, and then
one row per model (shortened here; the ids come from each runtime's own model
list, so yours will differ):

```
Confinement policy for a loopback listener: any (keys created with --context: chat-only)
From this shell's flags, environment and saved settings: a running server may be set up differently (`monoagentcli --profile work api status` shows what a running daemon applies).

MODEL                    CONFINEMENT  VALIDATED  SERVED  CONTEXT KEY  AUTO  IMAGES
claude/default           chat-only    false      yes     yes          yes   no
claude/sonnet            chat-only    false      yes     yes          yes   no
codex/gpt-6-astra        sandboxed    false      yes     no           no    yes
antigravity/default      unconfined   false      yes     no           no    yes
```

`api models` works out what a listener with the flags, environment and saved
settings (section 5) of *this shell* would serve; it does not ask a running
server (`api status` shows what a running daemon applies). `SERVED` is whether the model is served
under that policy, `CONTEXT KEY` whether a key created with `--context`
may use it, `AUTO` whether the `auto` model may pick it and `IMAGES` whether
it makes images (see "Make images"); `--json` adds a `capabilities` list per
model. The ids are `<runtime>/<model>`, as each runtime lists them, so
use the ones this prints. A bare runtime (`codex`) is its default model, and
`agy` is accepted for `antigravity`. A model that is neither listed by the
runtime nor in your agent roster is a 404, although a runtime's aliases (such
as `claude/opus[1m]`) resolve without being listed. If a runtime's own listing
fails, a built-in list stands in for it until a later listing works, so its
ids can differ meanwhile. The listing is tried again after 30 seconds, then
after 1, 2 and 4 minutes, and every 5 minutes after that, each time a request
arrives.

## 3. Call it

```bash
curl -s http://127.0.0.1:9322/v1/models -H "Authorization: Bearer $KEY" | jq '.data[].id'

curl -s http://127.0.0.1:9322/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"claude","messages":[{"role":"user","content":"Say hello in five words."}]}' \
  | jq -r '.choices[0].message.content'

# streaming: server-sent events, ending with "data: [DONE]"
curl -sN http://127.0.0.1:9322/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"codex","stream":true,"messages":[{"role":"user","content":"Count to five."}]}'
```

Python:

```python
import os

from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:9322/v1", api_key=os.environ["KEY"])

reply = client.chat.completions.create(
    model="claude", messages=[{"role": "user", "content": "Say hello in five words."}]
)
print(reply.choices[0].message.content)

for chunk in client.chat.completions.create(
    model="codex", stream=True, messages=[{"role": "user", "content": "Count to five."}]
):
    print(chunk.choices[0].delta.content or "", end="")
```

JavaScript:

```js
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://127.0.0.1:9322/v1", apiKey: process.env.KEY });
const reply = await client.chat.completions.create({
  model: "claude",
  messages: [{ role: "user", content: "Say hello in five words." }],
});
console.log(reply.choices[0].message.content);
```

What to expect:

- Each request is one agent turn on your own subscription or account:
  a few seconds to a minute, and billed or rate limited like any use of that
  runtime. There are no per-key quotas; at most 4 turns run at once (a full
  server answers 429 with `Retry-After`).
- Requests are stateless. Sampling parameters (`temperature`, `max_tokens`,
  `stop`, …) are accepted and ignored, because the agent runtimes have none.
  `n` above 1, `logprobs: true`, `response_format` of type `json_schema`, the
  legacy `functions` and `function_call` (use `tools`), the `function` role
  and parts that are not text (images, audio, files) are rejected with 400
  `unsupported_parameter` (`response_format: {"type":"json_object"}` works, as a
  best-effort instruction; `tools: []` and `tool_choice: "none"` are accepted).
- `curl -i` shows `X-Monoagent-Model` (the model that answered),
  `X-Monoagent-Sandbox` (how monomind sandboxed that turn: `sandboxed`,
  `scoped`, `unsupported`, …; sent on a successful non-streaming response
  only), for a `--context`
  key, `X-Monoagent-Context` (how many knowledge excerpts were added) and, for
  the model `auto`, `X-Monoagent-Auto` (who picked the model).

### Let Jev pick: the `auto` model

With `"model": "auto"`, Jev chooses the model of each request among the ones
this listener serves: a cheaper, faster one for a simple prompt, a stronger one
for a hard or long one. Jev's decision provider is
[TypeSafe Jev](https://docs.typesafe.ai/api) unless you chose another (see
"Decision models (Jev)" in `AGENTS.md`). It is off until
you switch it on for the key's profile, and it needs a decision provider it can
use: with TypeSafe, the machine-wide Jev key (one key for all profiles); with a
provider you added, that provider's own key, or none for a keyless server on
this computer (`monoagentcli --profile work jev provider use local --surface
api_auto` points just this surface at it). With TypeSafe:

```bash
printf '%s' "$TYPESAFE_KEY" | monoagentcli jev key set   # or TYPESAFE_API_KEY in the server's environment
monoagentcli --profile work jev enable api_auto    # prints what is sent and to which host, then asks (--yes in a script)
monoagentcli --profile work api models             # its last line says whether auto works
```

On a headless server, where the vault needs the file keyring and its passphrase
file, `TYPESAFE_API_KEY` in the server's environment is the simple way: the
server's agent turns do not inherit it. `api models` and `api status` run in
your shell, so for a key from the environment they report this shell's, not the
server's.

`jev enable` lists what leaves the machine: to the decision provider
(TypeSafe by default), besides the prompt
going to the runtime that answers, the first 4,000 characters of the last user
message and the names, descriptions and validated cost and latency of the
models Jev picks among. Then:

```bash
curl -si http://127.0.0.1:9322/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"What is 17 * 23?"}]}' \
  | grep -iE '^(HTTP|x-monoagent-(model|auto))'
```

The response's `model` and `X-Monoagent-Model` name the pick, and
`X-Monoagent-Auto` says who chose: `jev`, or `rule` when Jev could not (no
answer within 8 seconds, an answer that is not one of the options, or one under
the surface's threshold, which `jev enable api_auto --threshold 0.8` raises) or
when only one model was left to pick. Of the models that passed
`monoagentcli agent validate` (which records the cost and latency to compare),
the rule takes the most confined, then the cheapest, then the fastest; with none
validated, a runtime's default model, claude first. When Jev does not answer three
questions in a row, the server stops asking for 30 seconds (the log says so) and
`X-Monoagent-Auto` is `rule` until one question gets through again.

Jev picks only among models the key may use, and by default only among the
chat-only ones (claude): a prompt can steer the pick, and its author need not
be the holder of the key (see `SECURITY.md`). To let `auto` pick sandboxed or
unconfined runtimes too, start the server with `--auto-confinement sandboxed`
(or `any`, or `MONOAGENT_API_AUTO_CONFINEMENT`); it never goes above
`--confinement`, nor above the cap of a `--context` key, and a model you name
yourself is not affected. `monoagentcli --profile work api models` shows which models `auto`
may pick. `GET /v1/models` lists `auto` after the other models, so a client that
takes the first one is not moved to it, and leaves it out while it does not work;
`monoagentcli --profile work api status` says what is missing.

### Make images

`POST /v1/images/generations` has a runtime that can make images do it, and
returns the files it saved. Which runtimes can is a list, `MONOAGENT_API_IMAGE_RUNTIMES`
(default `codex,antigravity`: monomind does not say which CLIs have an image
tool; `none` switches image generation off, and every image request then says
so). `GET /v1/models` marks their models with the capability `image`, and so
does `api models --json`:

```bash
curl -s http://127.0.0.1:9322/v1/models -H "Authorization: Bearer $KEY" \
  | jq -r '.data[] | select(.monoagent.capabilities | index("image")) | .id'
```

An image turn needs a runtime that can write the file, so a `sandboxed` or
`unconfined` one: on the default loopback listener that is allowed, but a
listener started with `--confinement chat-only` (the default off loopback), or
a key created with `--context` while `--context-confinement` is chat-only,
answers 403 `policy_denied` and says what to raise. Without `model` it is the
first installed runtime of the list that the key may use. Only `b64_json` is
returned (`response_format: "url"` is a 400). Save it:

```bash
curl -s http://127.0.0.1:9322/v1/images/generations \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"codex","prompt":"A small red circle on a white background.","size":"1024x1024"}' \
  | jq -r '.data[0].b64_json' | base64 --decode > circle.img
file circle.img      # PNG image data (codex made a PNG in the probes, antigravity a JPEG)
```

The response is `{"created":…,"data":[{"b64_json":"…"}]}` and says nothing of
the format: the runtime chose it (PNG, JPEG, WebP or GIF), so look at the first
bytes. Python:

```python
import base64

# client = OpenAI(base_url="http://127.0.0.1:9322/v1", api_key=os.environ["KEY"]), as above
result = client.images.generate(
    model="codex",
    prompt="A small red circle on a white background.",
    size="1024x1024",
    response_format="b64_json",
)
data = base64.b64decode(result.data[0].b64_json)
ext = "png" if data[:4] == b"\x89PNG" else "jpg" if data[:2] == b"\xff\xd8" else "webp" if data[8:12] == b"WEBP" else "gif"
with open(f"circle.{ext}", "wb") as f:
    f.write(data)
```

JavaScript:

```js
import fs from "node:fs";

// client = new OpenAI({ baseURL: "http://127.0.0.1:9322/v1", apiKey: process.env.KEY }), as above
const result = await client.images.generate({
  model: "codex",
  prompt: "A small red circle on a white background.",
  size: "1024x1024",
  response_format: "b64_json",
});
const bytes = Buffer.from(result.data[0].b64_json, "base64");
const ext = bytes.subarray(0, 4).toString("hex") === "89504e47" ? "png" : bytes[0] === 0xff ? "jpg" : "img";
fs.writeFileSync(`circle.${ext}`, bytes);
```

What to expect:

- A request takes a minute or two and uses some 40,000 input tokens of the
  runtime, on your account (the probes: 40, 52, 75 and 105 seconds; codex and
  antigravity report no cost). The 10 minute turn timeout and the 4 turns at
  once are the same as for chat. The body is capped at 64 KiB.
- `n` is 1 to 4 and is passed on in words: a runtime may save fewer images, and
  you get what it saved (at least one). `size` is `auto` or `WxH` with each side
  from 64 to 8192, a preference the runtime may not follow. `quality`, `style`,
  `output_format`, `background` and `user` are accepted and ignored.
- The runtime makes the image with its own tool and copies the file into a
  folder the server made for the turn (its name is new every time); before it
  empties the working folder the server reads the PNG, JPEG, WebP and GIF files
  in that folder (up to 20 MiB each) and nowhere else, and never follows a link
  in it. A runtime that says it has no image tool is a 400
  `image_generation_unsupported`; one that saves nothing, or saves it
  elsewhere, is a 502 `image_generation_failed` with what it said.
- Nothing is sent until the turn is over, a minute or more: a reverse proxy has
  to wait that long (see "Behind a reverse proxy" below).
- `"model": "auto"` has Jev pick among the image models, but `auto` is held to
  chat-only until you start the server with `--auto-confinement sandboxed` (or
  `any`): image runtimes are sandboxed or unconfined, so by default it is a 404
  `model_not_found` that says what to raise. Raised, `jev enable api_auto` sends
  the decision provider (TypeSafe by default) the first 4,000 characters of the
  image prompt, as it does for chat.
- Read `SECURITY.md` ("Image generation"): the runtime may read and copy files
  from outside its folder, and the server returns any file it left in the
  turn's folder that begins like an image (the test is on the first bytes).

### Call your own tools

With `tools`, the model can ask your program to run a function and use the
result: the OpenAI tool loop, over claude and codex. The server never runs
anything of yours. It returns the call, you run it wherever you like (or refuse
to), and you send the result back. Which models serve tools is in
`GET /v1/models` (the capability `tools`) and in `api models` (a TOOLS column, and
`--json`, which the MCP tool `api_models_list` returns too); any other model
answers 400 `unsupported_parameter` and says which runtimes serve them
(`MONOAGENT_API_TOOL_RUNTIMES`, default `claude,codex`; `none` switches tool
calling off and says so):

```bash
curl -s http://127.0.0.1:9322/v1/models -H "Authorization: Bearer $KEY" \
  | jq -r '.data[] | select(.monoagent.capabilities | index("tools")) | .id'
```

A key created with `--context` is not offered `tools` (and a request that
declares them is a 403) unless the operator raised its cap above chat-only:
`--context-confinement`, and `--confinement` too on a listener that is chat-only,
which holds the key to the lower of the two (the answer names the flag that
would change it, or both).

A round trip with curl. The first request declares the function; the answer ends
at the model's call:

```bash
TOOLS='[{"type":"function","function":{"name":"get_weather","description":"Get the current weather for a city.","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]'
Q='What is the weather in Paris?'

curl -s http://127.0.0.1:9322/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "$(jq -n --arg q "$Q" --argjson tools "$TOOLS" '{model: "claude", messages: [{role: "user", content: $q}], tools: $tools}')" \
  > first.json
jq '.choices[0] | {finish_reason, tool_calls: .message.tool_calls}' first.json
```

```json
{
  "finish_reason": "tool_calls",
  "tool_calls": [
    { "id": "call_k3j2h1g4f5d6s7a8", "type": "function",
      "function": { "name": "get_weather", "arguments": "{\"city\":\"Paris\"}" } }
  ]
}
```

Run the call yourself, then send the conversation again: the assistant message as
it came, and a `tool` message with the result:

```bash
curl -s http://127.0.0.1:9322/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "$(jq -n --arg q "$Q" --argjson tools "$TOOLS" --slurpfile first first.json '
    $first[0].choices[0].message as $call
    | {model: "claude", tools: $tools, messages: [
        {role: "user", content: $q}, $call,
        {role: "tool", tool_call_id: $call.tool_calls[0].id, content: "{\"temp_c\": 21, \"conditions\": \"fog\"}"}]}')" \
  | jq -r '.choices[0].message.content'
```

The same loop with the Python SDK (it is the usual one: stop when a response has
no `tool_calls`, and cap the rounds yourself, the server does not):

```python
import json
import os

from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:9322/v1", api_key=os.environ["KEY"])

tools = [{
    "type": "function",
    "function": {
        "name": "get_weather",
        "description": "Get the current weather for a city.",
        "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]},
    },
}]


def get_weather(city: str) -> dict:  # your own code: any function, on any machine
    return {"city": city, "temp_c": 21, "conditions": "fog"}


messages = [{"role": "user", "content": "What is the weather in Paris?"}]
for _ in range(8):
    reply = client.chat.completions.create(model="claude", messages=messages, tools=tools)
    message = reply.choices[0].message
    if not message.tool_calls:
        print(message.content)
        break
    messages.append(message)  # the assistant message, as it came
    for call in message.tool_calls:
        result = get_weather(**json.loads(call.function.arguments))  # yours to run, or to refuse
        messages.append({"role": "tool", "tool_call_id": call.id, "content": json.dumps(result)})
```

Streaming works too: `stream: true` sends the words before the call as content
deltas, then the call as `delta.tool_calls` chunks (an index, then the id, type
and name, then the arguments), then a chunk that finishes with `tool_calls`, and
`[DONE]`; the SDKs' stream helpers assemble them as for any OpenAI model.

To point a coding client at it, give the client the base URL
`http://127.0.0.1:9322/v1`, a key and a model id that has the `tools` capability
(clients built on the OpenAI SDKs read `OPENAI_BASE_URL` and `OPENAI_API_KEY`).
What to expect from a tool loop:

- **One call per response.** The turn ends at the model's first call, so a model
  that wants several asks for them in successive rounds: `parallel_tool_calls` is
  accepted and treated as false. A client written for parallel calls just loops
  more.
- **Each round is a turn.** A round takes seconds (the spike's medians: about 6
  seconds to the first call on claude and 11 on codex), on the runtime's account,
  and no process waits for your result, so a slow tool costs the server nothing.
  The follow-up continues the runtime's session when the server still remembers
  the call (same key, same model, same tools and the same conversation before
  the call, within ten minutes, once: about half the price of the alternative on
  claude); otherwise, after a restart, a retry, a long pause, or a change to an
  earlier message (to what it says, not to how it is written: the arguments of a
  call with their keys in another order or their characters escaped another way
  are the same call, a value or a number written another way is not; on codex
  also to the system prompt, the tool choice or the response format: a claude
  session is told the new ones), it starts again from the transcript you send,
  which always works. Either way you send the whole conversation each time.
- **The functions are yours; the model's own tools are not.** claude's own tools
  stay denied: a tool turn requires monomind's sandbox, under which monomind lets
  only the prefixed names of your functions through, so a function called `Bash`
  cannot open claude's own, and the turn is refused (403) when the sandbox cannot
  be applied. A codex leg runs read-only (`--access read`): it cannot write files,
  so a coding client's edits must go through its declared functions, which is the
  point. A model whose runtime monomind cannot run read-only, or whose sandbox it
  cannot apply, is refused.
- `tool_choice` `required` or a named function is an instruction in the system
  prompt, not a guarantee (it worked 9 of 9 for a named function and 5 of 6 for
  `required` in the spike); `none` passes no tools.
- Put the whole JSON schema in `parameters`: monomind keeps only the top-level
  properties of it (and of each, its type and an enum of strings), so the server
  folds the schema into the function's description and names the arguments of a
  root `anyOf`, `oneOf` or `allOf`, of a local `$ref` (`#/$defs/...`), of an
  `if`, `then` or `else`, of what `dependentSchemas`, `dependencies` and
  `dependentRequired` ask for and of a `const` or an `enum` of objects at the top
  level too (optional where the schema lets a call do without them, and as any value where only a branch that may not apply
  defines them), or the call would reach you as `{}`. A key that no property
  names is not passed on.
- monomind holds a call to the type, the enum of strings and the `required`
  names of the top-level properties it was told of. A call that does not match
  them is rejected inside monomind and never reaches you: the model tries again,
  and after monomind's round cap (10) you get `200` whose content is
  `[monomind] tool-call round cap (10) reached ...` with `finish_reason:
  "length"` (and a `usage`) instead of a call, so a mismatch there looks like a
  model that gave up. What monomind cannot see comes back as the model wrote it,
  unchecked, and is yours to check before you run it: nested properties, the
  items of an array, patterns, formats, numeric ranges, lengths (the server only
  counts, in its log, the calls that do not match its own reading of the
  schema). An `enum` that is not a list of strings is left out of what the
  runtime's tool bridge gets (the model still reads it in the description).
- Some requests are refused: 400 `invalid_value`, naming the parameter and never
  what you wrote. Whatever the `tool_choice` (the tools are checked when they are declared): more
  than 128 functions; a tool that is not an object; a name that is not 1 to 64
  characters of `[A-Za-z0-9_-]`, one declared twice, or an alias that is another
  function's name (a name of 55 or more reaches the model as an alias of 54, and
  you always see your own); a `description` of more than 16 KiB; `parameters` of
  more than 64 KiB, that are not a JSON schema object, whose root `type` is not
  `object`, whose `properties` is not an object or holds a property that is not
  a schema, or whose `required` is not a list of strings; a `tool_choice` that
  is not `none`, `auto`, `required` or a function, or that names a function that
  is not declared; and in the conversation more than 64 calls in one message, a
  call with an id of no characters or of more than 128 bytes, a call whose name
  is not printable ASCII without `[ ] < > & ' "` or a backtick, a call whose
  `arguments` is not a string, a result of more than 256 KiB, and a tool message
  that answers no call of an earlier assistant message. Other codes: 400
  `unsupported_parameter` for a tool or a call of a type other than `function`,
  a `tool_choice` of another type, and a `tool_choice` that forces a call when
  no tools are declared; 400 `missing_required_parameter` for a tool with no
  `function`, a `tool_choice` that names no function, and a tool message with no
  `tool_call_id`. While the tools are passed to the model (`tool_choice` is not `none`,
  which passes none, so no argument is named and these do not apply): a function
  whose schema names no property and allows free-form keys (`additionalProperties`
  or `unevaluatedProperties` true or a schema, `patternProperties`) or whose
  references cannot be followed (`$dynamicRef`, a `$ref` that is not local to the
  schema or leads nowhere), since no argument of its calls could be passed on:
  list the arguments in `properties`; a schema whose `const` or `enum` (the root's,
  an `allOf`'s or a `$ref`'s that applies) holds a value that is not an object,
  which no call could match; a schema that nests combinators and references more
  than 8 levels deep or holds more than 2,000 schemas; and functions whose schemas
  together take more of the server than it reads to name their arguments (100,000
  steps for a request, a step being a schema read, a reference followed, a
  property or a listed name met, an enum entry compared; a reference counts once
  however it is spelled). An id that is not such a token (a bracket, a quote, a
  space, a line break: what clients really send, such as `call_abc123`,
  `toolu_01A...` and `functions.name:0`, is fine) is not refused: the model reads
  it as `call_1`, `call_2`, ... in order of appearance, and the follow-up is served
  by a replay, since only an id the server made can resume a session.
- Make tools with side effects idempotent. A model can ask for the same call
  again after a resume (1 of 19 single-result claude legs in the spike, none of
  18 on codex), and codex repeats an identical call two or three times within a
  leg: the server returns only the first of them, but cannot tell a repeat of a
  round from a new request for the same thing.
- A conversation that has tool messages but no `tools` (a client that dropped
  them for its last round) is answered as text.
- Read `SECURITY.md` ("Tool calling") before you let a client run calls without
  asking: a tool result, and with `--context` a captured page, can steer which
  calls the model proposes. So a key created with `--context` is refused tools
  (403 `policy_denied`, before anything starts, naming `--context-confinement`,
  `--confinement` when the server is chat-only, or both) unless the operator
  raised that cap above chat-only; use a key without
  `--context` for a client that calls tools.

## 4. Serve it beyond this machine

The main listener serves `/v1` only on loopback. To reach it from another
machine, give it its own listener. It serves nothing but `/v1` and `/health`
and, off loopback, is TLS only:

```bash
export MONOAGENT_API_TLS_CERT=/etc/monoagent/fullchain.pem
export MONOAGENT_API_TLS_KEY=/etc/monoagent/privkey.pem
monoagentcli daemon --v1-addr 0.0.0.0:9443
```

- Without a certificate in the environment, a self-signed one valid for
  `localhost` only is generated and cached under `~/.monoagent/api-tls/`;
  remote clients must trust it explicitly (`curl -k` for a test). For real use
  set the two variables, or terminate TLS in a reverse proxy.
- An off-loopback listener defaults to `--confinement chat-only`, so it
  lists and serves only the chat-only runtimes (claude here).
  `--confinement sandboxed` adds the sandboxed ones (codex and copilot here)
  and `--confinement any` adds the rest. Read `SECURITY.md` first: a key then
  lets its holder use that runtime's native tools on this machine.
- Behind a reverse proxy, bind the listener to loopback
  (`--v1-addr 127.0.0.1:9443`) and set `--confinement` explicitly, since a
  loopback bind defaults to `any`. `--confinement` is one value for the whole
  process, so it limits the loopback main listener too. Unset the two
  certificate variables for a proxy that forwards plain HTTP: while they are
  set, the listener speaks TLS even on a loopback bind. Turn proxy buffering
  off for streaming (the server already sends `X-Accel-Buffering: no`).
- Raise the proxy's read timeout above the turn time. An image request sends no
  byte while its turn runs, a minute or more (40 to 105 seconds in the probes, up
  to `MONOAGENT_API_TURN_TIMEOUT`, 10 minutes by default), and nginx's default
  `proxy_read_timeout` is 60 seconds: the client would get a 504 from the
  proxy while the turn is still running. For nginx, `proxy_read_timeout 11m;`
  on the location of `/v1/`.

## 5. Save the server's settings

A flag or an environment variable is read when a server starts, so the daemon
that the login service starts (`daemon install`) has neither. Save the settings
in the database instead, and every start reads them. There are ten, named by
these keys:

| Key | What it sets | Server flag | Environment variable |
|---|---|---|---|
| `v1_addr` | the dedicated listener | `--v1-addr` | `MONOAGENT_API_V1_ADDR` |
| `tls_cert_file`, `tls_key_file` | the PEM files of that listener, both or neither | none | `MONOAGENT_API_TLS_CERT`, `MONOAGENT_API_TLS_KEY` |
| `confinement` | the strongest class served | `--confinement` | `MONOAGENT_API_CONFINEMENT` |
| `context_confinement` | the strongest class a `--context` key may use | `--context-confinement` | `MONOAGENT_API_CONTEXT_CONFINEMENT` |
| `auto_confinement` | the strongest class `auto` may pick | `--auto-confinement` | `MONOAGENT_API_AUTO_CONFINEMENT` |
| `max_concurrent` | turns at once, 1 to 64 | `--max-concurrent` | `MONOAGENT_API_MAX_CONCURRENT` |
| `turn_timeout` | the cap on one turn, at least `10s` | none | `MONOAGENT_API_TURN_TIMEOUT` |
| `image_runtimes`, `tool_runtimes` | the runtimes that make images and that serve tools, or `none` | none | `MONOAGENT_API_IMAGE_RUNTIMES`, `MONOAGENT_API_TOOL_RUNTIMES` |

```bash
monoagentcli api config set --max-concurrent 8 --turn-timeout 15m
# Saved: max_concurrent, turn_timeout.
# No daemon is running: the server reads these settings when it starts.
```

`set` changes only the settings it is given and checks each value as the flag
or the variable would (a bad one is exit 3 and names the setting). A saved value
is held to two more rules, because it outlives the command and is shown to people
and to agents: no control character in it, and a TLS file is an absolute path (the
daemon starts in another folder, and nothing expands a `~`).
`unset max_concurrent` or `unset --all` removes saved values. For each setting a
server uses the first of: its flag, its environment variable, the saved value,
the default. A variable in the service's own environment therefore holds
whatever is saved. `api models` and `api status` read the saved settings too,
so what they say is what a server started now would do.

`api config show` says, for each setting, what is saved, what a server started
from this shell would use and, when a daemon is running, what that daemon
started with and where each value came from. Here the daemon started with
`max_concurrent` 8, and 12 was saved afterwards:

```
SETTING              SAVED  EFFECTIVE (this shell)  RUNNING (daemon)             STATE
v1_addr              -      -                       - (default)                  applied
tls_cert_file        -      -                       - (default)                  applied
tls_key_file         -      -                       - (default)                  applied
confinement          -      -                       - (default)                  applied
context_confinement  -      chat-only               chat-only (default)          applied
auto_confinement     -      chat-only               chat-only (default)          applied
max_concurrent       12     12 (saved)              8 (saved)                    restart needed
turn_timeout         15m    15m (saved)             15m (saved)                  applied
image_runtimes       -      codex,antigravity       codex,antigravity (default)  applied
tool_runtimes        -      claude,codex            claude,codex (default)       applied
```

A server reads its settings only when it starts. `restart needed` is a saved
value that the running daemon does not have yet; `overridden` is a setting the
daemon was given as a flag or a variable of its own, so what is saved for it has
no effect until that is removed; `listener not up` (`not_serving` in the JSON, only
for `v1_addr` and the two TLS files) is a daemon that took the address and the
certificate but could not bring the dedicated listener up (it could not bind the
address, or load the certificate): its log says which, and restarting does not
help until the setting is corrected. `--json` gives the same as a document
(`restart_needed`, and per setting `saved`, `effective`, `source`, `running`,
`running_source` and `state`).

```bash
monoagentcli daemon restart
```

restarts the daemon through the service it is registered with (a LaunchAgent, a
systemd user unit or a Scheduled Task; `daemon install` registers it) so that it
reads them. **That interrupts whatever the daemon is running**, workflows and org
runs included, and the command says so first. A daemon that is not registered is
not restarted by it: it exits 3 and tells you to stop the daemon and start it
again. If you started one by hand while the service is registered, stop that one
first: the service's daemon would find the home taken and exit. It reads the saved
settings first and restarts nothing if they cannot be used (exit 3 and the message
`api config show` gives): a daemon that cannot use them would start without the
API, and says why in its log, while it runs everything else.

A change that makes the server reach further than it did needs `--yes`: a
listener beyond this machine (or on another host, or on every interface where it
was one host), a higher confinement class, a runtime list that gains a runtime it
did not have, or the removal of a saved row that cannot be read. Without it the command refuses
(exit 3) and says why; there is no prompt, so a script has to mean it. `--dry-run`
shows the reasons and saves nothing:

```
$ monoagentcli api config set --v1-addr 0.0.0.0:9443 --tls-cert-file /etc/monoagent/fullchain.pem --tls-key-file /etc/monoagent/privkey.pem --confinement chat-only --dry-run
Dry run: nothing was saved. It would change: v1_addr, tls_cert_file, tls_key_file, confinement.
This change makes the server reach further, so applying it needs --yes:
  - The dedicated /v1 listener would listen on 0.0.0.0:9443, beyond this machine, and serve runtimes up to chat-only; it did not listen beyond this machine before.
```

Raising `confinement` to `sandboxed` or `any` always needs `--yes`, even with no
listener beyond this machine saved, because the daemon's own environment may name
one. Removing a value that held the server below its default (`unset confinement`
of a `chat-only`, an `image_runtimes` of `none` or of `codex`, which leaves
antigravity out) is the same kind of change. So is
removing a saved row that cannot be read (not JSON, a bad version, a field of the
wrong type): every `api config` command, and `httpapi`, stop at it with exit 3 and
a message that starts `the saved settings are damaged` (the daemon starts without
the API instead, and says why), and `api config unset --all
--yes` removes it, because what the row limited cannot be told.
The settings are in the database of the user the daemon runs as
(`~/.monoagent/monoagent.db`), so run `api config` as that user.

An agent host can do the same through `monoagentcli mcp --allow-mutations`:
`api_config_get` shows what `api config show` shows, `api_config_set` saves and
removes settings with the same checks, and `api_config_apply` restarts the
daemon (which interrupts what it is running). A change that makes the server
reach further than it did is refused there unless you started the MCP server
with `--allow-api-exposure`: a model cannot confirm it, and its refusal says
which setting and why, and that you can run `api config set ... --yes` yourself.
`api_auto_set` is held by the same flag when it switches the `auto` model on,
since prompts then go to the decision provider (TypeSafe by default): what leaves the machine is your decision too.
That flag guards those two tools, not the machine: a host that also lets the
model run shell commands as you lets it run `--yes` too, and `--allow-mutations`
itself is such a host: it also serves `workflow_node_add` (which accepts the node
type `system.execute_command`), `workflow_set_active` and `workflow_run`, so a
model that can use them can have a workflow run `--yes` as you. To give a model
the API's tools and nothing else, start the server with `--api-only`:

```bash
monoagentcli mcp --api-only --allow-mutations --allow-api-exposure
```

It serves the ten `api_*` tools and no workflow, vault, secret, person, org or
documentation tool, so the model has nothing that runs a command as you, and the
exposure flag is then a boundary for what it can reach through this server (not
against a host that gives it tools of its own besides). Leave out
`--allow-api-exposure` to keep it from widening the server or switching `auto`
on, and `--allow-mutations` to leave it only the four read-only tools.

## 6. A headless Linux server

1. Install monomind and the agent CLIs you want to serve, and sign them in as
   the user that will run the service. `monoagentcli --profile work doctor` shows what is
   missing. Use a dedicated unprivileged OS user: a key is the runtime's
   capabilities, and a runtime with a shell runs it as that user.
2. Create a key: `monoagentcli --profile work api key create --name app`. It needs no
   keyring and no GUI.
3. Install the service: `monoagentcli daemon install` writes the systemd user
   unit `monoagent-daemon.service`. The unit has no environment or flags, so
   save the server's settings (section 5) as the user that runs it, and restart
   the service:

   ```bash
   monoagentcli api config set --v1-addr 0.0.0.0:9443 \
     --tls-cert-file /etc/monoagent/fullchain.pem \
     --tls-key-file /etc/monoagent/privkey.pem \
     --confinement chat-only --yes
   monoagentcli daemon restart
   ```

   `--yes` is there because the listener is beyond this machine; `--dry-run`
   in its place says why. `--confinement` is one value for the whole
   process, so `chat-only` here also limits `/v1` on the daemon's loopback main
   listener. `daemon restart` runs `systemctl --user restart
   monoagent-daemon.service` and interrupts what the daemon is running.

   The same variables in a systemd drop-in work too (`systemctl --user edit
   monoagent-daemon`, then `[Service]` and `Environment=MONOAGENT_API_V1_ADDR=…`),
   and a variable of the unit wins over a saved value: `api config show` marks
   such a setting `overridden`. A drop-in is where an `Environment=PATH=…` line
   goes if monomind or an agent CLI is not found under systemd's minimal `PATH`.
   As root, `loginctl enable-linger <user>` so the service starts at boot. Logs:
   `journalctl --user -u monoagent-daemon -f`.
4. Check that the listener came up: `monoagentcli --profile work api status`, run as the same
   user, lists it and says whether it answers `/v1`; `monoagentcli api config
   show` says whether the daemon runs what is saved. The daemon only logs a
   warning when it cannot start the listener (an unreadable certificate, a
   port in use) and keeps running, so the unit looks healthy either way. A
   listener it could not start is missing from the list, and `api status` says
   so (`v1 none: the daemon reports no dedicated /v1 listener …`): read
   `journalctl --user -u monoagent-daemon` for the reason.
5. From another machine: `curl https://server:9443/v1/models -H "Authorization:
   Bearer $KEY"`.

## Errors

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_json`, `invalid_value`, `missing_required_parameter`, `unsupported_parameter` | The body is not JSON, or a parameter is missing, invalid or not supported (`tools` for a model that does not serve them, a function schema that names no argument, `n > 1`, `json_schema` output, parts that are not text, …). For an image request also a `model` that cannot make images (or image generation switched off), `n` outside 1 to 4, a bad `size`, `response_format: "url"` and `stream: true` |
| 400 | `image_generation_unsupported` | The runtime replied `NO_IMAGE_TOOL`: it has no image tool |
| 401 | `invalid_api_key` | Missing, unknown or revoked key. The legacy HTTP API token is not a key |
| 403 | `policy_denied` | A completion or an image request names a model whose confinement class is above the listener's `--confinement`, or above `--context-confinement` for a key created with `--context`; a completion that declares `tools` (without `tool_choice` `none`) with a key created with `--context` while its cap, the lower of the listener's `--confinement` and `--context-confinement`, is chat-only; an image request without a model when the key's policy allows no runtime that can write a file (the message says what to raise); or its sandbox could not be applied; or the runtime started with less confinement than the policy allows |
| 404 | `model_not_found` | Unknown model. `GET /v1/models/{id}` also answers 404 for a model the listener does not serve (a completion for it is a 403). `auto` is a 404 too while it is not set up for the key's profile: the message says what is missing (the `api_auto` Jev surface, a Jev key or a usable decision provider for that surface, or a model the policy allows). For an image request without a model also: no runtime of `MONOAGENT_API_IMAGE_RUNTIMES` can make images here (the message says, for each, not installed or installed but chat-only), or image generation is switched off (`none`) |
| 413 | `request_too_large` | Body over 2 MiB (64 KiB for an image request) |
| 429 | `rate_limit_exceeded`, `insufficient_quota` | The server is full (`Retry-After: 2`), or the runtime is rate limited or out of quota |
| 500 | `internal_error` | An internal failure. The message is generic; the detail is in the server log under the response's `X-Request-Id` |
| 502 | `runtime_error` | The runtime reported an error, or ended the turn without finishing it. The message is generic too, and the runtime's own words reach you only for a sign-in hint, a rate limit, quota or a timeout, on one line and at most 300 characters |
| 502 | `image_generation_failed` | An image turn ended without an image to return. The message has what the runtime replied, on one line and at most 300 characters |
| 503 | `runtime_not_available` | monomind or the runtime is not installed or not signed in, or the server is shutting down |
| 504 | `timeout` | The turn exceeded 10 minutes (`MONOAGENT_API_TURN_TIMEOUT`) |

Every error of the four routes has the body
`{"error":{"message","type","param","code"}}` and an `X-Request-Id` header. A
path or method the API does not have (for example `GET /v1/embeddings`) gets
Go's plain-text 404 or 405 instead. A stream starts (status 200) on its first
content or after 5 seconds of silence, and from then on an error arrives as a
`data: {"error": …}` event followed by `data: [DONE]`, not as a status from
this table: a runtime that does not stream (codex, for one) is usually past
that point before it fails.
